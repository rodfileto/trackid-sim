package world

import (
	_ "embed"
	"encoding/json"
	"math/rand/v2"
	"sort"
	"strconv"
)

// censo is the adult sex and single-year age distribution from IBGE Censo 2022,
// copied from tools/facegen/censo2022_shares.json so the world and the face
// pool draw from the same table (world_test.go checks the copy has not drifted).
//
//go:embed data/censo2022_adults.json
var censoJSON []byte

type ageTable struct {
	ages       []int
	cum        []float64 // cumulative weight, same order as ages
	femaleRate float64
}

var censo = mustAgeTable(censoJSON)

func mustAgeTable(data []byte) ageTable {
	var raw struct {
		Sex map[string]float64 `json:"sex"`
		Age map[string]float64 `json:"age"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		panic(err) // embedded and fixed; a failure is a programming error
	}
	var t ageTable
	for k := range raw.Age {
		a, err := strconv.Atoi(k)
		if err != nil {
			panic(err)
		}
		t.ages = append(t.ages, a)
	}
	sort.Ints(t.ages)
	total := 0.0
	for _, a := range t.ages {
		total += raw.Age[strconv.Itoa(a)]
		t.cum = append(t.cum, total)
	}
	for i := range t.cum {
		t.cum[i] /= total
	}
	t.femaleRate = raw.Sex["female"] / (raw.Sex["female"] + raw.Sex["male"])
	return t
}

func (t ageTable) min() int { return t.ages[0] }
func (t ageTable) max() int { return t.ages[len(t.ages)-1] }

func (t ageTable) draw(r *rand.Rand) int {
	i := sort.SearchFloat64s(t.cum, r.Float64())
	return t.ages[min(i, len(t.ages)-1)]
}

// siblingAges returns the ages (eldest first) of a family of up to size
// children, all within the table's adult range. One child, chosen uniformly,
// takes its age from the table; the others are spaced 1-3 years apart around
// it, and any that fall outside the range are dropped. Anchoring a random
// position rather than the eldest keeps the truncation roughly symmetric, so
// the realised ages stay close to the table while siblings stay close in age
// -- which is what makes them hard to tell apart on name and parents.
func (t ageTable) siblingAges(r *rand.Rand, size int) []int {
	ages := make([]int, size)
	k := r.IntN(size)
	ages[k] = t.draw(r)
	for i := k + 1; i < size; i++ {
		ages[i] = ages[i-1] - (1 + r.IntN(3))
	}
	for i := k - 1; i >= 0; i-- {
		ages[i] = ages[i+1] + (1 + r.IntN(3))
	}
	out := ages[:0]
	for _, a := range ages {
		if a >= t.min() && a <= t.max() {
			out = append(out, a)
		}
	}
	return out
}
