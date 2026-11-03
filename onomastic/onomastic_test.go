package onomastic

import (
	"math/rand/v2"
	"strings"
	"testing"
)

func rng(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, seed+1)) }

func TestSeedLexiconLoads(t *testing.T) {
	lex := SeedLexicon()
	if len(lex.surnames.values) < 50 || len(lex.female.values) < 30 || len(lex.male.values) < 30 {
		t.Fatalf("seed lexicon unexpectedly small: %d/%d/%d", len(lex.female.values), len(lex.male.values), len(lex.surnames.values))
	}
}

func TestSamplerIsZipfSkewed(t *testing.T) {
	lex := SeedLexicon()
	r := rng(1)
	counts := map[string]int{}
	for i := 0; i < 20000; i++ {
		counts[lex.surnames.draw(r)]++
	}
	first, last := lex.surnames.values[0], lex.surnames.values[len(lex.surnames.values)-1]
	if counts[first] < 5*counts[last] {
		t.Fatalf("expected heavy skew: %s=%d vs %s=%d", first, counts[first], last, counts[last])
	}
}

func TestGeneratorDeterministic(t *testing.T) {
	gen := func() string {
		g := NewGenerator(SeedLexicon(), rng(42))
		f := g.Family()
		return g.Child(f, Female, true).Full() + "|" + f.Mother.Full() + "|" + f.Father.Full()
	}
	if a, b := gen(), gen(); a != b {
		t.Fatalf("same seed produced different output:\n%s\n%s", a, b)
	}
}

func TestChildInheritsParentSurnames(t *testing.T) {
	g := NewGenerator(SeedLexicon(), rng(7))
	for i := 0; i < 500; i++ {
		f := g.Family()
		known := g.Child(f, Male, true)
		for _, s := range known.Surnames {
			if s != f.Mother.lastSurname() && s != f.Father.lastSurname() {
				t.Fatalf("surname %q is from neither parent (%v / %v)", s, f.Mother.Surnames, f.Father.Surnames)
			}
		}
		unknownFather := g.Child(f, Male, false)
		if len(unknownFather.Surnames) != 1 || unknownFather.Surnames[0] != f.Mother.lastSurname() {
			t.Fatalf("undeclared father: child must carry only the mother's surname, got %v", unknownFather.Surnames)
		}
	}
}

func TestProfileKeepsFirstTokenAndStrips(t *testing.T) {
	r := rng(3)
	in := "José Carlos Conceição da Silva"
	for i := 0; i < 500; i++ {
		out := Police.Apply(r, in)
		if out != strings.ToUpper(out) {
			t.Fatalf("Police must uppercase: %q", out)
		}
		if out != StripAccents(out) {
			t.Fatalf("Police must strip accents: %q", out)
		}
		// The first token may take a typo (only ever after its first letter) but is
		// never dropped or abbreviated.
		if !strings.HasPrefix(out, "J") || strings.HasPrefix(out, "J.") {
			t.Fatalf("first token dropped or abbreviated: %q", out)
		}
	}
}

func TestProfileCorruptsAtRoughlyTheConfiguredRate(t *testing.T) {
	r := rng(9)
	in := "Maria Aparecida Souza Lima"
	changed := 0
	const n = 5000
	for i := 0; i < n; i++ {
		if Registry.Apply(r, in) != in {
			changed++
		}
	}
	// 4 tokens at ~2-6% each -> a modest but clearly non-zero share, far from 0 or 100%.
	if changed < n/50 || changed > n/2 {
		t.Fatalf("Registry changed %d of %d names; expected a small non-zero share", changed, n)
	}
}

func TestLoadLexiconExplicitWeight(t *testing.T) {
	lex, err := LoadLexicon("data")
	if err != nil {
		t.Fatal(err)
	}
	if len(lex.female.values) == 0 {
		t.Fatal("empty")
	}
}

// repeatedWord returns a word that appears twice in a name, ignoring particles.
func repeatedWord(n Name) string {
	seen := map[string]bool{}
	for _, w := range append(append([]string{}, n.Given...), n.Surnames...) {
		w = surnameWord(w)
		if seen[w] {
			return w
		}
		seen[w] = true
	}
	return ""
}

func TestNamesHaveNoRepeatedWords(t *testing.T) {
	lex, err := LoadLexicon(ibgeLexicon)
	if err != nil {
		t.Fatal(err)
	}
	g := NewGenerator(lex, rng(5))
	for i := 0; i < 20000; i++ {
		f := g.Family()
		for _, n := range []Name{f.Mother, f.Father, g.Child(f, Female, true), g.Child(f, Male, i%2 == 0)} {
			if w := repeatedWord(n); w != "" {
				t.Fatalf("%q repeats %q", n.Full(), w)
			}
		}
	}
}

func TestChildOfParentsSharingASurnameCarriesItOnce(t *testing.T) {
	f := Family{
		Mother: Name{Given: []string{"Ana"}, Surnames: []string{"Lima", "Morais"}},
		Father: Name{Given: []string{"Jose"}, Surnames: []string{"de Morais"}},
	}
	g := NewGenerator(SeedLexicon(), rng(1))
	for i := 0; i < 200; i++ {
		c := g.Child(f, Male, true)
		if len(c.Surnames) != 1 || surnameWord(c.Surnames[0]) != "Morais" {
			t.Fatalf("child surnames = %v, want Morais once", c.Surnames)
		}
	}
}
