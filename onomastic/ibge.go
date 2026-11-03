package onomastic

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Censo 2022 name frequencies from the IBGE names API (the service behind
// censo2022.ibge.gov.br/nomes/rankings). Three rankings are used:
//
//   - given names (first name only), one per sex;
//   - surnames, where IBGE counts every word after the first given name, so the list
//     also holds middle given names ("Maria", "Henrique") and generational suffixes
//     ("Junior", "Filho"). Particles ("de", "da") are not counted.
//
// Accents are stripped by the source. A locality is Brazil (0), a state or a
// municipality; the API has no regions.

// IBGENamesAPI is the Censo 2022 names API base URL.
const IBGENamesAPI = "https://servicodados.ibge.gov.br/api/v3/nomes/2022/localidade"

// Ranking kinds.
const (
	GivenRanking   = "nome"
	SurnameRanking = "sobrenome"
)

// ufCodes maps state abbreviations to IBGE locality codes.
var ufCodes = map[string]string{
	"RO": "11", "AC": "12", "AM": "13", "RR": "14", "PA": "15", "AP": "16", "TO": "17",
	"MA": "21", "PI": "22", "CE": "23", "RN": "24", "PB": "25", "PE": "26", "AL": "27", "SE": "28", "BA": "29",
	"MG": "31", "ES": "32", "RJ": "33", "SP": "35",
	"PR": "41", "SC": "42", "RS": "43",
	"MS": "50", "MT": "51", "GO": "52", "DF": "53",
}

// Localidade resolves "0" (Brazil), a state abbreviation ("SP"), or an IBGE state or
// municipality code to the API's locality code.
func Localidade(s string) (string, error) {
	s = strings.TrimSpace(s)
	if code, ok := ufCodes[strings.ToUpper(s)]; ok {
		return code, nil
	}
	if _, err := strconv.Atoi(s); err != nil || (s != "0" && len(s) != 2 && len(s) != 7) {
		return "", fmt.Errorf("localidade %q: want 0, a state abbreviation, or a 2-digit state / 7-digit municipality code", s)
	}
	return s, nil
}

// RankEntry is one row of a ranking.
type RankEntry struct {
	Rank    int
	Name    string // lower case, as the API returns it
	Count   int64
	Percent float64 // share of the locality's whole population, in percent
}

// Client reads rankings from the IBGE names API.
type Client struct {
	BaseURL string        // default IBGENamesAPI
	HTTP    *http.Client  // default http.DefaultClient
	Pause   time.Duration // wait between requests, to go easy on the service
}

// FetchRanking pages through one ranking (kind GivenRanking with sex "F" or "M", or
// SurnameRanking with sex "") until the count falls below minPerMillion people per
// million inhabitants of the locality, and returns the entries at or above it.
func (c Client) FetchRanking(ctx context.Context, localidade, kind, sex string, minPerMillion float64) ([]RankEntry, error) {
	var out []RankEntry
	floor := -1.0
	for page := 1; ; page++ {
		p, err := c.page(ctx, localidade, kind, sex, page)
		if err != nil {
			return nil, err
		}
		for _, e := range p.Items {
			if floor < 0 {
				if e.Percent <= 0 {
					return nil, fmt.Errorf("ibge names api: first entry %q has no percent", e.Nome)
				}
				population := float64(e.Frequencia) / e.Percent * 100
				floor = minPerMillion * population / 1e6
			}
			if float64(e.Frequencia) < floor {
				return out, nil
			}
			out = append(out, RankEntry{Rank: e.Rank, Name: e.Nome, Count: e.Frequencia, Percent: e.Percent})
		}
		if p.NextPage == 0 || len(p.Items) == 0 {
			return out, nil
		}
		if c.Pause > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(c.Pause):
			}
		}
	}
}

type apiPage struct {
	NextPage int `json:"nextPage"`
	Items    []struct {
		Nome       string  `json:"nome"`
		Percent    float64 `json:"percent"`
		Frequencia int64   `json:"frequencia"`
		Rank       int     `json:"rank"`
	} `json:"items"`
}

func (c Client) page(ctx context.Context, localidade, kind, sex string, page int) (apiPage, error) {
	base := c.BaseURL
	if base == "" {
		base = IBGENamesAPI
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	u := fmt.Sprintf("%s/%s/ranking/%s?page=%d", base, localidade, kind, page)
	if sex != "" {
		u += "&sexo=" + sex
	}

	var body []byte
	var err error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return apiPage{}, ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		body, err = get(ctx, client, u)
		if err == nil || errors.Is(err, errPermanent) {
			break
		}
	}
	if err != nil {
		return apiPage{}, fmt.Errorf("ibge names api %s: %w", u, err)
	}
	var p apiPage
	if err := json.Unmarshal(body, &p); err != nil {
		return apiPage{}, fmt.Errorf("ibge names api %s: %w", u, err)
	}
	return p, nil
}

var errPermanent = errors.New("permanent")

func get(ctx context.Context, client *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errPermanent, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	switch {
	case resp.StatusCode == http.StatusOK:
		return body, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	default:
		return nil, fmt.Errorf("%w: status %d", errPermanent, resp.StatusCode)
	}
}

// WeightedName is one line of a lexicon file.
type WeightedName struct {
	Name   string
	Weight float64
}

// Dropped is a ranking entry left out of the lexicon, and why.
type Dropped struct {
	List       string // "given_female", "given_male" or "surnames"
	Name       string
	Count      int64 // in this list's ranking
	OtherCount int64 // in the ranking that decided it (other sex, or first names)
	Reason     string
}

// GivenNames builds one sex's given-name list from its ranking. A name is left out
// when under minSexShare of the people carrying it are of this sex, per the two sex
// rankings -- mostly data-entry noise (a few women recorded as "Jose"); 0 keeps all.
func GivenNames(list string, own, other []RankEntry, minSexShare float64) (kept []WeightedName, dropped []Dropped) {
	otherCount := counts(other)
	for _, e := range own {
		o := otherCount[e.Name]
		if float64(e.Count) < minSexShare*float64(e.Count+o) {
			dropped = append(dropped, Dropped{list, titleCase(e.Name), e.Count, o, "sex share"})
			continue
		}
		kept = append(kept, WeightedName{titleCase(e.Name), float64(e.Count)})
	}
	return kept, dropped
}

// suffixes are generational suffixes IBGE counts as surnames. They are not drawn as
// surnames: a "Junior" is named after a parent, which the generator does not model.
var suffixes = map[string]bool{"junior": true, "filho": true, "neto": true, "sobrinho": true}

// Surnames builds the surname list from its ranking. Besides suffixes, it leaves out
// words far more common as a first name than as a surname -- middle given names such
// as "Maria" -- when firstNames[name] >= middleNameRatio * surname count; the
// generator already draws compound given names. 0 disables that rule.
func Surnames(ranking []RankEntry, firstNames map[string]int64, middleNameRatio float64) (kept []WeightedName, dropped []Dropped) {
	for _, e := range ranking {
		f := firstNames[e.Name]
		switch {
		case suffixes[e.Name]:
			dropped = append(dropped, Dropped{"surnames", titleCase(e.Name), e.Count, f, "suffix"})
		case middleNameRatio > 0 && float64(f) >= middleNameRatio*float64(e.Count):
			dropped = append(dropped, Dropped{"surnames", titleCase(e.Name), e.Count, f, "middle name"})
		default:
			kept = append(kept, WeightedName{titleCase(e.Name), float64(e.Count)})
		}
	}
	return kept, dropped
}

// FirstNameCounts adds up given-name rankings (both sexes) by name.
func FirstNameCounts(rankings ...[]RankEntry) map[string]int64 {
	out := map[string]int64{}
	for _, r := range rankings {
		for n, c := range counts(r) {
			out[n] += c
		}
	}
	return out
}

func counts(r []RankEntry) map[string]int64 {
	out := make(map[string]int64, len(r))
	for _, e := range r {
		out[e.Name] += e.Count
	}
	return out
}

func titleCase(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// WriteList writes names in the "name,weight" format LoadLexicon reads, after the
// given comment lines.
func WriteList(w io.Writer, comments []string, names []WeightedName) error {
	for _, c := range comments {
		if _, err := fmt.Fprintf(w, "# %s\n", c); err != nil {
			return err
		}
	}
	for _, n := range names {
		if _, err := fmt.Fprintf(w, "%s,%.0f\n", n.Name, n.Weight); err != nil {
			return err
		}
	}
	return nil
}

// WriteDropped writes the entries left out of a lexicon as CSV.
func WriteDropped(w io.Writer, dropped []Dropped) error {
	cw := csv.NewWriter(w)
	cw.Write([]string{"list", "name", "count", "other_count", "reason"})
	for _, d := range dropped {
		cw.Write([]string{d.List, d.Name, strconv.FormatInt(d.Count, 10), strconv.FormatInt(d.OtherCount, 10), d.Reason})
	}
	cw.Flush()
	return cw.Error()
}

// WriteRanking writes a ranking snapshot as "rank,name,count,percent" CSV.
func WriteRanking(w io.Writer, r []RankEntry) error {
	cw := csv.NewWriter(w)
	cw.Write([]string{"rank", "name", "count", "percent"})
	for _, e := range r {
		cw.Write([]string{strconv.Itoa(e.Rank), e.Name, strconv.FormatInt(e.Count, 10), strconv.FormatFloat(e.Percent, 'f', -1, 64)})
	}
	cw.Flush()
	return cw.Error()
}

// ReadRanking reads a snapshot written by WriteRanking.
func ReadRanking(r io.Reader) ([]RankEntry, error) {
	rows, err := csv.NewReader(r).ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 || strings.Join(rows[0], ",") != "rank,name,count,percent" {
		return nil, fmt.Errorf("ranking: unexpected header")
	}
	out := make([]RankEntry, 0, len(rows)-1)
	for _, rec := range rows[1:] {
		rank, err1 := strconv.Atoi(rec[0])
		count, err2 := strconv.ParseInt(rec[2], 10, 64)
		pct, err3 := strconv.ParseFloat(rec[3], 64)
		if err := errors.Join(err1, err2, err3); err != nil {
			return nil, fmt.Errorf("ranking %q: %w", rec, err)
		}
		out = append(out, RankEntry{Rank: rank, Name: rec[1], Count: count, Percent: pct})
	}
	return out, nil
}
