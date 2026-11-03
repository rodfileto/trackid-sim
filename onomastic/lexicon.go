// Package onomastic generates synthetic Brazilian personal names with realistic
// structure: compound given names, two-part surnames inherited from mother and
// father, siblings sharing parents, and controlled record corruption.
//
// The bundled lexicon is a PLACEHOLDER (approximate ordering of common names,
// Zipf-weighted by rank). For publishable results load real frequency tables
// with LoadLexicon, e.g. the Censo 2022 names in data/ibge2022 written by
// "trackid-sim fetch-lexicon" (see ibge.go); nothing else in the simulator
// depends on which lexicon is used.
package onomastic

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

//go:embed data/*.txt
var seedFS embed.FS

// zipfExponent shapes the rank -> weight curve for names without an explicit weight.
const zipfExponent = 1.0

// sampler draws a value with probability proportional to its weight.
type sampler struct {
	values []string
	cum    []float64
}

func newSampler(values []string, weights []float64) (sampler, error) {
	if len(values) == 0 || len(values) != len(weights) {
		return sampler{}, fmt.Errorf("onomastic: empty or mismatched name list")
	}
	cum := make([]float64, len(weights))
	total := 0.0
	for i, w := range weights {
		if w <= 0 || math.IsNaN(w) || math.IsInf(w, 0) {
			return sampler{}, fmt.Errorf("onomastic: invalid weight %v for %q", w, values[i])
		}
		total += w
		cum[i] = total
	}
	return sampler{values: values, cum: cum}, nil
}

func (s sampler) draw(r *rand.Rand) string {
	x := r.Float64() * s.cum[len(s.cum)-1]
	return s.values[sort.SearchFloat64s(s.cum, x)]
}

// Lexicon holds the weighted name pools a Generator draws from.
type Lexicon struct {
	female, male, surnames sampler
}

// SeedLexicon returns the bundled placeholder lexicon.
func SeedLexicon() *Lexicon {
	lex, err := loadFrom(seedFS, "data")
	if err != nil {
		panic(err) // the embedded files are fixed; a failure is a programming error
	}
	return lex
}

// LoadLexicon reads given_female.txt, given_male.txt and surnames.txt from dir.
// Each line is "name" (weight by rank, Zipf) or "name,weight".
func LoadLexicon(dir string) (*Lexicon, error) {
	return loadFrom(os.DirFS(dir), ".")
}

func loadFrom(fsys fs.FS, dir string) (*Lexicon, error) {
	read := func(name string) (sampler, error) {
		f, err := fsys.Open(filepath.ToSlash(filepath.Join(dir, name)))
		if err != nil {
			return sampler{}, err
		}
		defer f.Close()
		return parseList(bufio.NewScanner(f), name)
	}
	female, err := read("given_female.txt")
	if err != nil {
		return nil, err
	}
	male, err := read("given_male.txt")
	if err != nil {
		return nil, err
	}
	surnames, err := read("surnames.txt")
	if err != nil {
		return nil, err
	}
	return &Lexicon{female: female, male: male, surnames: surnames}, nil
}

func parseList(sc *bufio.Scanner, label string) (sampler, error) {
	var values []string
	var weights []float64
	rank := 0
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		rank++
		name, wtext, hasWeight := strings.Cut(line, ",")
		name = strings.TrimSpace(name)
		w := 1 / math.Pow(float64(rank), zipfExponent)
		if hasWeight {
			parsed, err := strconv.ParseFloat(strings.TrimSpace(wtext), 64)
			if err != nil {
				return sampler{}, fmt.Errorf("onomastic: %s line %q: %w", label, line, err)
			}
			w = parsed
		}
		values = append(values, name)
		weights = append(weights, w)
	}
	if err := sc.Err(); err != nil {
		return sampler{}, err
	}
	return newSampler(values, weights)
}
