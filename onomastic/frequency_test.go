package onomastic

import (
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

const ibgeLexicon = "data/ibge2022"

// weights recovers each value's weight from a sampler's cumulative sums.
func (s sampler) weights() map[string]float64 {
	out := make(map[string]float64, len(s.values))
	prev := 0.0
	for i, v := range s.values {
		out[v] += s.cum[i] - prev
		prev = s.cum[i]
	}
	return out
}

// The generator's names follow the committed Censo 2022 lexicon: each of the most
// frequent first given names and surnames is drawn within 5 standard errors of its
// lexicon share.
func TestNamesFollowLexicon(t *testing.T) {
	lex, err := LoadLexicon(ibgeLexicon)
	if err != nil {
		t.Fatal(err)
	}
	const n = 200000
	for _, tc := range []struct {
		label string
		pool  sampler
		draw  func(g *Generator) string
	}{
		{"female", lex.female, func(g *Generator) string { return g.given(Female)[0] }},
		{"male", lex.male, func(g *Generator) string { return g.given(Male)[0] }},
		{"surname", lex.surnames, func(g *Generator) string { return lex.surnames.draw(g.r) }},
	} {
		g := NewGenerator(lex, rng(11))
		drawn := map[string]int{}
		for i := 0; i < n; i++ {
			drawn[tc.draw(g)]++
		}
		w := tc.pool.weights()
		total := tc.pool.cum[len(tc.pool.cum)-1]
		for _, name := range tc.pool.values[:20] {
			p := w[name] / total
			got := float64(drawn[name]) / n
			se := math.Sqrt(p * (1 - p) / n)
			if math.Abs(got-p) > 5*se {
				t.Errorf("%s %s: drawn %.4f, lexicon %.4f (5 s.e. = %.4f)", tc.label, name, got, p, 5*se)
			}
		}
	}
}

// The committed lexicon is exactly what fetch-lexicon's defaults (-min-sex-share 0.01,
// -middle-name-ratio 2) make of the committed IBGE ranking snapshots.
func TestLexiconMatchesSourceRankings(t *testing.T) {
	read := func(name string) []RankEntry {
		f, err := os.Open(filepath.Join(ibgeLexicon, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		r, err := ReadRanking(f)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	female, male, surnames := read("ranking_given_female.csv"), read("ranking_given_male.csv"), read("ranking_surnames.csv")
	wantF, _ := GivenNames("given_female", female, male, 0.01)
	wantM, _ := GivenNames("given_male", male, female, 0.01)
	wantS, _ := Surnames(surnames, FirstNameCounts(female, male), 2)

	lex, err := LoadLexicon(ibgeLexicon)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		label string
		pool  sampler
		want  []WeightedName
	}{{"female", lex.female, wantF}, {"male", lex.male, wantM}, {"surnames", lex.surnames, wantS}} {
		var got []WeightedName
		w := tc.pool.weights()
		for _, v := range tc.pool.values {
			got = append(got, WeightedName{v, w[v]})
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: lexicon (%d names) differs from the rankings (%d names)", tc.label, len(got), len(tc.want))
		}
	}
}
