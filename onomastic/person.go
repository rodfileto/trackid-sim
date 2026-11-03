package onomastic

import (
	"math/rand/v2"
	"strings"
)

// Sex codes match the gender table in the trackid schema.
const (
	Female = "F"
	Male   = "M"
)

// Name is a person's name split into its parts. Surname entries may carry a
// glued particle ("da Silva"); they are treated as one unit.
type Name struct {
	Given    []string
	Surnames []string
}

// Full renders the name as one space-separated string.
func (n Name) Full() string {
	return strings.Join(append(append([]string{}, n.Given...), n.Surnames...), " ")
}

// surnameWord is a surname without its particle: "da Silva" -> "Silva".
func surnameWord(s string) string {
	if i := strings.LastIndexByte(s, ' '); i >= 0 {
		return s[i+1:]
	}
	return s
}

// hasSurname reports whether list holds s, ignoring particles.
func hasSurname(list []string, s string) bool {
	for _, x := range list {
		if surnameWord(x) == surnameWord(s) {
			return true
		}
	}
	return false
}

func (n Name) lastSurname() string {
	if len(n.Surnames) == 0 {
		return ""
	}
	return n.Surnames[len(n.Surnames)-1]
}

// Family is a couple whose children share both parents' names -- the source of
// sibling records, which are the hardest confusers for name-based linkage.
type Family struct {
	Mother, Father Name
}

// Generator draws names from a Lexicon.
type Generator struct {
	lex *Lexicon
	r   *rand.Rand
}

func NewGenerator(lex *Lexicon, r *rand.Rand) *Generator {
	return &Generator{lex: lex, r: r}
}

var particles = []string{"de", "da", "do", "dos", "das"}

func (g *Generator) given(sex string) []string {
	pool := g.lex.female
	if sex == Male {
		pool = g.lex.male
	}
	n := 1
	switch x := g.r.Float64(); {
	case x < 0.05:
		n = 3
	case x < 0.45:
		n = 2
	}
	out := make([]string, 0, n)
	for len(out) < n {
		name := pool.draw(g.r)
		if !contains(out, name) {
			out = append(out, name)
		}
	}
	return out
}

func (g *Generator) surname() string {
	s := g.lex.surnames.draw(g.r)
	if g.r.Float64() < 0.08 {
		s = particles[g.r.IntN(len(particles))] + " " + s
	}
	return s
}

// maxRedraws bounds the redraws that keep a name free of repeated words. The pools
// hold thousands of names, so a limit is only reached with a tiny lexicon, which then
// keeps its repeats.
const maxRedraws = 20

// adult builds a parent's name: one to three inherited surnames, none repeated.
func (g *Generator) adult(sex string) Name {
	n := 2
	switch x := g.r.Float64(); {
	case x < 0.10:
		n = 1
	case x > 0.90:
		n = 3
	}
	surnames := make([]string, 0, n)
	for tries := 0; len(surnames) < n; tries++ {
		s := g.surname()
		if hasSurname(surnames, s) && tries < maxRedraws {
			continue
		}
		surnames = append(surnames, s)
	}
	return Name{Given: g.givenNotIn(sex, surnames), Surnames: surnames}
}

// givenNotIn draws given names none of which is also one of the surnames ("Rosa ...
// Rosa"): the census lexicon has words that are both.
func (g *Generator) givenNotIn(sex string, surnames []string) []string {
	for tries := 0; ; tries++ {
		given := g.given(sex)
		clash := false
		for _, n := range given {
			if hasSurname(surnames, n) {
				clash = true
			}
		}
		if !clash || tries >= maxRedraws {
			return given
		}
	}
}

// Family draws a mother and a father.
func (g *Generator) Family() Family {
	return Family{Mother: g.adult(Female), Father: g.adult(Male)}
}

// Child names a child of f. By Brazilian convention the child usually carries
// the mother's last surname then the father's; some carry only one. A child of
// an undeclared father (fatherKnown false) carries only the mother's, and parents
// sharing a last surname pass it on once.
func (g *Generator) Child(f Family, sex string, fatherKnown bool) Name {
	mother, father := f.Mother.lastSurname(), f.Father.lastSurname()
	var surnames []string
	switch x := g.r.Float64(); {
	case !fatherKnown:
		surnames = []string{mother}
	case x < 0.85:
		surnames = []string{mother, father}
	case x < 0.95:
		surnames = []string{father}
	default:
		surnames = []string{mother}
	}
	if len(surnames) == 2 && surnameWord(surnames[0]) == surnameWord(surnames[1]) {
		surnames = surnames[1:]
	}
	return Name{Given: g.givenNotIn(sex, surnames), Surnames: surnames}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
