package onomastic

import (
	"math/rand/v2"
	"strings"
	"unicode"
)

// Profile describes how one source system distorts a name when recording it.
// Two enrollments of the same human under different profiles are the raw
// material for record-linkage evaluation.
type Profile struct {
	TypoRate       float64 // per token: one edit (substitute/delete/insert/transpose)
	AbbreviateRate float64 // per middle token: reduced to an initial
	DropTokenRate  float64 // per middle token: omitted entirely
	StripAccents   bool
	Uppercase      bool
}

// Registry mimics a civil registry: careful data entry, accents kept.
var Registry = Profile{TypoRate: 0.02, AbbreviateRate: 0.02, DropTokenRate: 0.02}

// Police mimics an ABIS enrollment terminal: uppercase, no accents, noisier.
var Police = Profile{TypoRate: 0.10, AbbreviateRate: 0.10, DropTokenRate: 0.08, StripAccents: true, Uppercase: true}

// Apply returns full name as this source would have recorded it. The first
// token (first given name) is never dropped or abbreviated.
func (p Profile) Apply(r *rand.Rand, full string) string {
	tokens := strings.Fields(full)
	out := make([]string, 0, len(tokens))
	for i, tok := range tokens {
		middle := i > 0 && i < len(tokens)-1
		if middle && r.Float64() < p.DropTokenRate {
			continue
		}
		if middle && len([]rune(tok)) > 3 && r.Float64() < p.AbbreviateRate {
			tok = string([]rune(tok)[0]) + "."
		} else if len([]rune(tok)) > 2 && r.Float64() < p.TypoRate {
			tok = typo(r, tok)
		}
		out = append(out, tok)
	}
	s := strings.Join(out, " ")
	if p.StripAccents {
		s = StripAccents(s)
	}
	if p.Uppercase {
		s = strings.ToUpper(s)
	}
	return s
}

func typo(r *rand.Rand, tok string) string {
	rs := []rune(tok)
	i := 1 + r.IntN(len(rs)-1) // never touch the first letter
	switch r.IntN(4) {
	case 0: // substitute
		rs[i] = sameCase(rs[i], rune('a'+r.IntN(26)))
	case 1: // delete
		rs = append(rs[:i], rs[i+1:]...)
	case 2: // insert a doubled letter
		rs = append(rs[:i+1], rs[i:]...)
	default: // transpose two adjacent letters, neither of them the first
		// Apply only passes tokens of 3+ runes, so j in [1, len-2] is always valid.
		j := 1 + r.IntN(len(rs)-2)
		rs[j], rs[j+1] = rs[j+1], rs[j]
	}
	return string(rs)
}

func sameCase(like, r rune) rune {
	if unicode.IsUpper(like) {
		return unicode.ToUpper(r)
	}
	return r
}

var accentMap = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ã", "a", "ä", "a",
	"é", "e", "ê", "e", "è", "e", "ë", "e",
	"í", "i", "î", "i", "ï", "i",
	"ó", "o", "ô", "o", "õ", "o", "ö", "o",
	"ú", "u", "û", "u", "ü", "u",
	"ç", "c", "ñ", "n",
	"Á", "A", "À", "A", "Â", "A", "Ã", "A", "Ä", "A",
	"É", "E", "Ê", "E", "È", "E", "Ë", "E",
	"Í", "I", "Î", "I", "Ï", "I",
	"Ó", "O", "Ô", "O", "Õ", "O", "Ö", "O",
	"Ú", "U", "Û", "U", "Ü", "U",
	"Ç", "C", "Ñ", "N",
)

// StripAccents removes Portuguese diacritics.
func StripAccents(s string) string { return accentMap.Replace(s) }
