// Package banks builds the bank branches of a municipality with coordinates:
// the Banco Central's register of branches (addresses, no coordinates), each
// geocoded through trackid's geocode package (Nominatim, OpenStreetMap). They
// are real places for the occurrences that happen at banks: FRAUD at a branch or
// ATM, withdrawal robberies, express kidnapping, ATM robbery.
//
// Both sources are ODbL: the branch register (BCB open data) and the
// coordinates (OpenStreetMap). What is built from them is a derived database
// under the same licence, crediting "Banco Central do Brasil" and
// "© OpenStreetMap contributors".
package banks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/rodfileto/trackid-sim/isp"
	"github.com/rodfileto/trackid/geocode"
)

const (
	// BranchesURL is the BCB Olinda OData service: every branch in Brazil. (Its
	// $filter rejected a municipality filter, so the municipality is picked here.)
	BranchesURL = "https://olinda.bcb.gov.br/olinda/servico/Informes_Agencias/versao/v1/odata/Agencias?$format=json&$top=100000"

	BranchesSource = "Banco Central do Brasil, Informes_Agencias (dadosabertos.bcb.gov.br/dataset/agencias)"
	Licence        = "ODbL: branch register © Banco Central do Brasil; coordinates © OpenStreetMap contributors (Nominatim)"
)

// Raw is one branch as the BCB publishes it.
type Raw struct {
	CnpjBase       string `json:"CnpjBase"`
	CnpjSequencial string `json:"CnpjSequencial"`
	CnpjDv         string `json:"CnpjDv"`
	NomeIf         string `json:"NomeIf"`
	Segmento       string `json:"Segmento"`
	NomeAgencia    string `json:"NomeAgencia"`
	Endereco       string `json:"Endereco"`
	Numero         string `json:"Numero"`
	Complemento    string `json:"Complemento"`
	Bairro         string `json:"Bairro"`
	Cep            string `json:"Cep"`
	MunicipioIbge  string `json:"MunicipioIbge"`
	Municipio      string `json:"Municipio"`
	UF             string `json:"UF"`
	Posicao        string `json:"Posicao"`
}

// FetchRaw downloads every branch in Brazil (about 14k, 3 MB).
func FetchRaw(ctx context.Context, c *http.Client) ([]Raw, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, BranchesURL, nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("banks: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("banks: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("banks: %s", resp.Status)
	}
	var v struct {
		Value []Raw `json:"value"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return nil, nil, fmt.Errorf("banks: %w", err)
	}
	return v.Value, body, nil
}

// IsBank reports whether a register segment is a bank (with branches the
// public walks into), not a broker, exchange house or credit fintech.
func IsBank(segment string) bool {
	return strings.HasPrefix(segment, "Banco") || segment == "Caixa Econômica Federal"
}

// Branch is a branch with its point.
type Branch struct {
	ID            string  `json:"id"` // full CNPJ: base, sequence, check digits
	Bank          string  `json:"bank"`
	Segment       string  `json:"segment"`
	Name          string  `json:"name"`
	Street        string  `json:"street"`
	Number        string  `json:"number,omitempty"`
	Neighbourhood string  `json:"neighbourhood"`
	Postcode      string  `json:"postcode"`
	Lon           float64 `json:"lon"`
	Lat           float64 `json:"lat"`
	Precision     string  `json:"precision"`
	CISP          int     `json:"cisp"`
	MatchedBy     string  `json:"matchedBy"` // which query found it (see Geocode)
	OSM           string  `json:"osm"`       // the OpenStreetMap object matched
	OSMName       string  `json:"osmName"`
}

// Failure is a branch no query placed inside the municipality.
type Failure struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Reason  string `json:"reason"`
}

// The number may carry a suffix ("384-A", "74/76"): the leading digits are kept.
var numberInStreet = regexp.MustCompile(`(?i)[, ]+(?:N[º°O]?\.?|NUM\.?|NUMERO)\s*(\d+)[-/\w]*\s*$|,\s*(\d+)[-/\w]*\s*$`)

// splitNumber returns the street, its abbreviations expanded, and the house
// number, taken from the street text ("RUA X, NUM 381") when the Numero field
// is empty.
func splitNumber(r Raw) (street, number string) {
	street, number = strings.TrimSpace(r.Endereco), strings.TrimSpace(r.Numero)
	if m := numberInStreet.FindStringSubmatch(street); m != nil {
		if number == "" {
			number = m[1] + m[2]
		}
		street = strings.TrimSpace(street[:len(street)-len(m[0])])
	}
	return expand(street), number
}

// abbreviations are the register's street-type and title abbreviations, which
// Nominatim does not match ("R.SEN.DANTAS" is Rua Senador Dantas). A word is
// matched whole, with or without its trailing dot.
var abbreviations = map[string]string{
	"R": "Rua", "AV": "Avenida", "AVN": "Avenida", "EST": "Estrada", "ESTR": "Estrada",
	"TV": "Travessa", "TRAV": "Travessa", "AL": "Alameda", "PC": "Praça", "PCA": "Praça", "PÇA": "Praça",
	"LGO": "Largo", "LG": "Largo", "ROD": "Rodovia", "PRAIA": "Praia",
	"SEN": "Senador", "PRES": "Presidente", "DR": "Doutor", "GAL": "General", "GEN": "General",
	"MAL": "Marechal", "CEL": "Coronel", "CAP": "Capitão", "TEN": "Tenente", "ALM": "Almirante",
	"VISC": "Visconde", "CONS": "Conselheiro", "CONSELH": "Conselheiro", "PROF": "Professor",
	"ENG": "Engenheiro", "DEP": "Deputado", "GOV": "Governador", "MIN": "Ministro",
	"STA": "Santa", "STO": "Santo", "S": "São", "N": "Nossa", "SRA": "Senhora", "NSA": "Nossa",
	"BR": "Barão", "BAR": "Barão", "COND": "Conde", "DQ": "Duque", "DUQ": "Duque", "PE": "Padre",
	"FREI": "Frei", "D": "Dom", "INF": "Infante", "IMP": "Imperador",
}

var abbrevWord = regexp.MustCompile(`[\pL]+\.?`)

var doubledWord = regexp.MustCompile(`(?i)^(\pL+)\s+(\pL+)\b`)

func expand(street string) string {
	return dedupe(expandWords(street))
}

// dedupe drops a street type written twice ("AV AVENIDA X" -> "Avenida X").
func dedupe(street string) string {
	if m := doubledWord.FindStringSubmatch(street); m != nil && strings.EqualFold(m[1], m[2]) {
		return m[1] + street[len(m[0]):]
	}
	return street
}

func expandWords(street string) string {
	// "R.SEN.DANTAS" -> "R. SEN. DANTAS": a dot glued to the next word separates.
	street = regexp.MustCompile(`\.(\pL)`).ReplaceAllString(street, ". $1")
	first := true
	return abbrevWord.ReplaceAllStringFunc(street, func(w string) string {
		bare := strings.TrimSuffix(w, ".")
		full, ok := abbreviations[strings.ToUpper(bare)]
		// A one-letter word is an abbreviation only with its dot, or as the
		// street type ("R SENADOR DANTAS").
		if ok && len([]rune(bare)) == 1 && !strings.HasSuffix(w, ".") && !first {
			ok = false
		}
		first = false
		if ok {
			return full
		}
		return w
	})
}

// Cache remembers geocoder answers by query, so a rerun asks nothing twice (as
// Nominatim's usage policy requires).
type Cache interface {
	Get(key string) ([]geocode.Result, bool, bool) // results, notFound, ok
	Put(key string, res []geocode.Result, notFound bool) error
}

// Geocode places one branch, trying ever coarser queries until a result falls
// inside one of cisps' boundaries:
//
//	street+postcode  street and number, city, state and CEP
//	street           the same without the CEP
//	postcode         the CEP alone
//	neighbourhood    "<bairro>, <city>, <state>"
func Geocode(ctx context.Context, g geocode.Geocoder, cache Cache, cisps *geojson.FeatureCollection, r Raw, city, state string) (Branch, error) {
	street, number := splitNumber(r)
	full := strings.TrimSpace(street + " " + number)
	b := Branch{
		ID: r.CnpjBase + r.CnpjSequencial + r.CnpjDv, Bank: r.NomeIf, Segment: r.Segmento, Name: r.NomeAgencia,
		Street: street, Number: number, Neighbourhood: strings.TrimSpace(r.Bairro), Postcode: strings.TrimSpace(r.Cep),
	}
	tries := []struct {
		name string
		q    geocode.Query
	}{
		{"street+postcode", geocode.Query{Street: full, City: city, State: state, Postcode: b.Postcode, Limit: 3}},
		{"street", geocode.Query{Street: full, City: city, State: state, Limit: 3}},
		{"postcode", geocode.Query{Postcode: b.Postcode, City: city, State: state, Limit: 3}},
		{"neighbourhood", geocode.Query{Text: b.Neighbourhood + ", " + city + ", " + state, Limit: 3}},
	}
	for _, t := range tries {
		if t.name == "postcode" && b.Postcode == "" || t.name == "neighbourhood" && b.Neighbourhood == "" {
			continue
		}
		key, _ := json.Marshal(t.q)
		res, notFound, ok := cache.Get(string(key))
		if !ok {
			var err error
			res, err = g.Search(ctx, t.q)
			notFound = errors.Is(err, geocode.ErrNotFound)
			if err != nil && !notFound {
				return b, err
			}
			if err := cache.Put(string(key), res, notFound); err != nil {
				return b, err
			}
		}
		for _, x := range res {
			// A municipality centroid would fall in some CISP only by
			// accident: not a place for the branch.
			if x.Precision == geocode.PrecisionMunicipality {
				continue
			}
			if c := isp.CISPAt(cisps, orb.Point{x.Lon, x.Lat}); c != 0 {
				b.Lon, b.Lat, b.Precision, b.CISP = x.Lon, x.Lat, x.Precision, c
				b.MatchedBy, b.OSM, b.OSMName = t.name, x.Source, x.DisplayName
				return b, nil
			}
		}
	}
	return b, ErrUnplaced
}

// ErrUnplaced is returned by Geocode when no query placed the branch inside the
// municipality.
var ErrUnplaced = errors.New("banks: no result inside the municipality")

// Built is the committed result.
type Built struct {
	Source       string    `json:"source"`
	Licence      string    `json:"licence"`
	Attribution  string    `json:"attribution"`
	BranchesURL  string    `json:"branchesUrl"`
	BranchesAsOf string    `json:"branchesAsOf"` // the register's Posicao
	Geocoder     string    `json:"geocoder"`
	Built        string    `json:"built"`
	Municipality string    `json:"municipality"`
	Branches     []Branch  `json:"branches"`
	Failures     []Failure `json:"failures"`
}

// Now is the build date, a variable for tests.
var Now = func() string { return time.Now().Format("2006-01-02") }
