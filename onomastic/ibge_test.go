package onomastic

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"testing"
)

func TestLocalidade(t *testing.T) {
	for in, want := range map[string]string{"0": "0", "SP": "35", "sp": "35", "35": "35", "3550308": "3550308"} {
		if got, err := Localidade(in); err != nil || got != want {
			t.Errorf("Localidade(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "XX", "355", "Nordeste"} {
		if _, err := Localidade(in); err == nil {
			t.Errorf("Localidade(%q): want an error", in)
		}
	}
}

// fakeAPI serves a ranking of 7 entries, 3 per page, for a locality of 1,000,000
// people, the way the IBGE names API shapes its pages.
func fakeAPI(t *testing.T, requests *int) *httptest.Server {
	counts := []int64{500, 200, 100, 50, 20, 10, 5}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		if r.URL.Path != "/0/ranking/nome" || r.URL.Query().Get("sexo") != "F" {
			t.Errorf("unexpected request %s", r.URL)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		type item struct {
			Nome       string  `json:"nome"`
			Percent    float64 `json:"percent"`
			Frequencia int64   `json:"frequencia"`
			Rank       int     `json:"rank"`
		}
		var items []item
		for i := (page - 1) * 3; i < min(page*3, len(counts)); i++ {
			items = append(items, item{"n" + strconv.Itoa(i+1), float64(counts[i]) / 1e6 * 100, counts[i], i + 1})
		}
		next := page + 1
		if page*3 >= len(counts) {
			next = 0
		}
		json.NewEncoder(w).Encode(map[string]any{"page": page, "nextPage": next, "items": items})
	}))
}

func TestFetchRankingStopsAtFloor(t *testing.T) {
	var requests int
	srv := fakeAPI(t, &requests)
	defer srv.Close()
	c := Client{BaseURL: srv.URL}

	// 20 per million in a locality of one million: entries down to 20 people, two pages.
	got, err := c.FetchRanking(context.Background(), "0", GivenRanking, "F", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[4].Count != 20 || got[4].Rank != 5 || got[0].Name != "n1" {
		t.Fatalf("got %+v", got)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}

	// A floor below the last entry reads every page.
	requests = 0
	got, err = c.FetchRanking(context.Background(), "0", GivenRanking, "F", 1)
	if err != nil || len(got) != 7 || requests != 3 {
		t.Fatalf("floor 1: %d entries, %d requests, %v", len(got), requests, err)
	}
}

func rank(entries ...any) []RankEntry {
	var out []RankEntry
	for i := 0; i < len(entries); i += 2 {
		out = append(out, RankEntry{Rank: i/2 + 1, Name: entries[i].(string), Count: int64(entries[i+1].(int))})
	}
	return out
}

func TestGivenNames(t *testing.T) {
	female := rank("maria", 1000, "ariel", 30, "jose", 5)
	male := rank("jose", 995, "ariel", 70, "maria", 10)
	kept, dropped := GivenNames("given_female", female, male, 0.01)
	wantKept := []WeightedName{{"Maria", 1000}, {"Ariel", 30}}
	wantDropped := []Dropped{{"given_female", "Jose", 5, 995, "sex share"}}
	if !reflect.DeepEqual(kept, wantKept) || !reflect.DeepEqual(dropped, wantDropped) {
		t.Fatalf("kept=%v dropped=%v", kept, dropped)
	}
	// Maria is 0.99% male: dropped at 1%, kept with the rule off.
	if kept, _ := GivenNames("given_male", male, female, 0.01); len(kept) != 2 {
		t.Fatalf("male at 1%%: %v", kept)
	}
	if kept, _ := GivenNames("given_male", male, female, 0); len(kept) != 3 {
		t.Fatalf("male at 0: %v", kept)
	}
}

func TestSurnames(t *testing.T) {
	ranking := rank("silva", 3000, "junior", 1100, "maria", 970, "rosa", 1200, "henrique", 510)
	first := FirstNameCounts(rank("maria", 12000, "rosa", 150), rank("henrique", 400))
	kept, dropped := Surnames(ranking, first, 2)
	wantKept := []WeightedName{{"Silva", 3000}, {"Rosa", 1200}, {"Henrique", 510}}
	wantDropped := []Dropped{
		{"surnames", "Junior", 1100, 0, "suffix"},
		{"surnames", "Maria", 970, 12000, "middle name"},
	}
	if !reflect.DeepEqual(kept, wantKept) || !reflect.DeepEqual(dropped, wantDropped) {
		t.Fatalf("kept=%v dropped=%v", kept, dropped)
	}
	if kept, _ := Surnames(ranking, first, 0); len(kept) != 4 {
		t.Fatalf("ratio 0 keeps all but suffixes: %v", kept)
	}
}

// Written lists load as a lexicon, and ranking snapshots round-trip.
func TestWrittenFilesLoad(t *testing.T) {
	dir := t.TempDir()
	for name, list := range map[string][]WeightedName{
		"given_female.txt": {{"Maria", 990}, {"Ana", 10}},
		"given_male.txt":   {{"Jose", 1}},
		"surnames.txt":     {{"Silva", 5}},
	} {
		var buf bytes.Buffer
		if err := WriteList(&buf, []string{"test"}, list); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	lex, err := LoadLexicon(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(lex.female.values, []string{"Maria", "Ana"}) || lex.female.cum[1] != 1000 {
		t.Fatalf("female sampler = %v %v", lex.female.values, lex.female.cum)
	}

	r := []RankEntry{{1, "silva", 34030104, 16.7569}, {2, "santos", 21367475, 10.5217}}
	var buf bytes.Buffer
	if err := WriteRanking(&buf, r); err != nil {
		t.Fatal(err)
	}
	back, err := ReadRanking(&buf)
	if err != nil || !reflect.DeepEqual(back, r) {
		t.Fatalf("round trip = %v, %v", back, err)
	}
}
