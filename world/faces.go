package world

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"os"
	"slices"
	"sort"

	"github.com/rodfileto/trackid-sim/onomastic"
)

// The face pool is fixed: 200 base faces, of which 146 have a variant that
// kept its identity (tools/facegen/pool_index.json). Faces are therefore a
// scarce resource the world is fitted to, not something generated per human.

type PoolVariant struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	// FaceBox is the detected face in the variant's own pixels (x1, y1, x2, y2).
	FaceBox [4]float64 `json:"face_box"`
}

type PoolFace struct {
	ID      string       `json:"id"`
	Sex     string       `json:"sex"` // "female" | "male", as the prompt asked
	Age     int          `json:"age"` // as the prompt asked
	File    string       `json:"file"`
	SHA256  string       `json:"sha256"`
	Variant *PoolVariant `json:"variant"` // nil when the variant failed the identity check
}

type FacePool struct {
	Faces []PoolFace `json:"faces"`
}

// FacePoolRef records which pool index a world was bound to.
type FacePoolRef struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func LoadFacePool(path string) (*FacePool, FacePoolRef, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, FacePoolRef{}, err
	}
	var p FacePool
	if err := json.Unmarshal(data, &p); err != nil {
		return nil, FacePoolRef{}, fmt.Errorf("world: parse %s: %w", path, err)
	}
	sum := sha256.Sum256(data)
	return &p, FacePoolRef{Path: path, SHA256: hex.EncodeToString(sum[:])}, nil
}

var poolSex = map[string]string{"female": onomastic.Female, "male": onomastic.Male}

type FaceSummary struct {
	Needed, Assigned               int // humans with a photo enrollment or a camera capture
	CaptureNeeded, CaptureAssigned int // humans captured on camera (need a kept variant)
	Doubles, DoublesWithFaces      int // double-identity pairs; those where both humans have a face
	DroppedPhotos, DroppedTraces   int // photo files / facial traces removed for lack of a face
	DroppedCases                   int
	MeanAgeGap                     float64 // |human age - face age|, years, over assigned humans
	MaxAgeGap                      int
}

// AssignFaces binds pool faces to the humans that need one, then removes what
// cannot be shown: photo files of humans left without a face, and facial
// traces of humans without a kept variant (cases left empty go too).
//
// Priority, when the pool is short: double-identity pairs first (both A and
// the B whose identity A also holds, so the conflict shows in faces and not
// only in fingerprints), then humans captured on camera, then humans only enrolled.
// Pairs and humans are taken whole or not at all, in a seeded order within
// each tier. Sex must match; ages are matched by minimum total gap, with
// human age taken on 1 January of YearFrom, as Generate draws it.
func AssignFaces(w *World, pool *FacePool, ref FacePoolRef) FaceSummary {
	r := rand.New(rand.NewPCG(w.Config.Seed, 0xface))
	idx, needFace, needVariant := faceNeeds(w)

	// Tiers of units; a unit is taken whole.
	var pairs, captured, enrolled [][]int
	inPair := map[int]bool{}
	for i, h := range w.Humans {
		if h.Impersonates == "" {
			continue
		}
		var unit []int
		for _, m := range []int{idx[h.Impersonates], i} {
			inPair[m] = true
			if needFace[m] {
				unit = append(unit, m)
			}
		}
		pairs = append(pairs, unit)
	}
	for i := range w.Humans {
		switch {
		case inPair[i] || !needFace[i]:
		case needVariant[i]:
			captured = append(captured, []int{i})
		default:
			enrolled = append(enrolled, []int{i})
		}
	}

	bySex := map[string][]int{} // pool indexes
	for i, f := range pool.Faces {
		bySex[poolSex[f.Sex]] = append(bySex[poolSex[f.Sex]], i)
	}
	type budget struct{ variant, all int }
	free := map[string]budget{}
	for sex, faces := range bySex {
		b := budget{all: len(faces)}
		for _, i := range faces {
			if pool.Faces[i].Variant != nil {
				b.variant++
			}
		}
		free[sex] = b
	}

	selected := map[int]bool{}
	for _, tier := range [][][]int{pairs, captured, enrolled} {
		r.Shuffle(len(tier), func(i, j int) { tier[i], tier[j] = tier[j], tier[i] })
		for _, unit := range tier {
			want := map[string]budget{}
			for _, m := range unit {
				b := want[w.Humans[m].Sex]
				b.all++
				if needVariant[m] {
					b.variant++
				}
				want[w.Humans[m].Sex] = b
			}
			fits := true
			for sex, b := range want {
				fits = fits && b.variant <= free[sex].variant && b.all <= free[sex].all
			}
			if !fits {
				continue
			}
			for sex, b := range want {
				f := free[sex]
				free[sex] = budget{variant: f.variant - b.variant, all: f.all - b.all}
			}
			for _, m := range unit {
				selected[m] = true
			}
		}
	}

	age := func(h Human) int { return w.Config.YearFrom - 1 - birthYear(h.BirthDate) }
	var s FaceSummary
	gaps := 0
	for sex, faces := range bySex {
		var vHumans, oHumans, vFaces []int
		for m := range selected {
			if w.Humans[m].Sex != sex {
				continue
			}
			if needVariant[m] {
				vHumans = append(vHumans, m)
			} else {
				oHumans = append(oHumans, m)
			}
		}
		for _, f := range faces {
			if pool.Faces[f].Variant != nil {
				vFaces = append(vFaces, f)
			}
		}
		used := map[int]bool{}
		assign := func(humans, candidates []int) {
			var left []int
			for _, f := range candidates {
				if !used[f] {
					left = append(left, f)
				}
			}
			for k, f := range matchByAge(w.Humans, humans, pool.Faces, left, age) {
				m := humans[k]
				used[f] = true
				w.Humans[m].Face = pool.Faces[f].ID
				if v := pool.Faces[f].Variant; v != nil {
					w.Humans[m].Variant = v.ID
				}
				gap := abs(age(w.Humans[m]) - pool.Faces[f].Age)
				gaps += gap
				s.MaxAgeGap = max(s.MaxAgeGap, gap)
			}
		}
		assign(vHumans, vFaces)
		assign(oHumans, faces)
	}

	for i, h := range w.Humans {
		if needFace[i] {
			s.Needed++
			if h.Face != "" {
				s.Assigned++
			}
		}
		if needVariant[i] {
			s.CaptureNeeded++
			if h.Variant != "" {
				s.CaptureAssigned++
			}
		}
		if h.Impersonates != "" {
			s.Doubles++
			if h.Face != "" && w.Humans[idx[h.Impersonates]].Face != "" {
				s.DoublesWithFaces++
			}
		}
	}
	if s.Assigned > 0 {
		s.MeanAgeGap = float64(gaps) / float64(s.Assigned)
	}

	// Remove what the pool cannot show.
	for k := range w.Enrollments {
		e := &w.Enrollments[k]
		if w.Humans[idx[e.HumanID]].Face != "" {
			continue
		}
		n := len(e.Files)
		e.Files = slices.DeleteFunc(e.Files, func(f File) bool { return f.FileType == "photo" })
		s.DroppedPhotos += n - len(e.Files)
		if len(e.Files) == 0 { // every enrollment carries something to match on
			e.Files = []File{{FileType: "nist", Sequence: 1}}
		}
	}
	var cases []Case
	for _, c := range w.Cases {
		if c.Modality == "FACIAL" {
			var evs []Evidence
			for _, ev := range c.Evidences {
				var ts []Trace
				for _, t := range ev.Traces {
					if w.Humans[idx[t.HumanID]].Variant == "" {
						s.DroppedTraces++
						continue
					}
					t.Sequence = int16(len(ts) + 1)
					ts = append(ts, t)
				}
				if len(ts) > 0 {
					ev.Sequence, ev.Traces = int16(len(evs)+1), ts
					evs = append(evs, ev)
				}
			}
			if len(evs) == 0 {
				s.DroppedCases++
				continue
			}
			c.Evidences = evs
		}
		cases = append(cases, c)
	}
	w.Cases = numberCases(cases, w.Occurrences)
	w.FacePool = &ref
	return s
}

// faceNeeds indexes w's humans by id and marks, by index, those who need a face (a photo
// enrollment or a camera capture) and those who need a kept variant (a camera capture).
func faceNeeds(w *World) (idx map[string]int, needFace, needVariant map[int]bool) {
	idx = map[string]int{}
	for i, h := range w.Humans {
		idx[h.ID] = i
	}
	needFace, needVariant = map[int]bool{}, map[int]bool{}
	for _, e := range w.Enrollments {
		for _, f := range e.Files {
			if f.FileType == "photo" {
				needFace[idx[e.HumanID]] = true
			}
		}
	}
	for _, c := range w.Cases {
		if c.Modality != "FACIAL" {
			continue
		}
		for _, ev := range c.Evidences {
			for _, t := range ev.Traces {
				needVariant[idx[t.HumanID]] = true
				needFace[idx[t.HumanID]] = true
			}
		}
	}
	return idx, needFace, needVariant
}

// matchByAge pairs every human in humans with a distinct face from faces,
// minimising the total age gap (both sorted by age, then an O(n*m) dynamic
// programme; for absolute differences an order-preserving matching is
// optimal). It returns, per human index into humans, the chosen pool index.
// Callers guarantee len(faces) >= len(humans).
func matchByAge(hs []Human, humans []int, pf []PoolFace, faces []int, age func(Human) int) map[int]int {
	hk := make([]int, len(humans)) // positions into humans, sorted by age
	for i := range hk {
		hk[i] = i
	}
	sort.Slice(hk, func(a, b int) bool {
		x, y := hs[humans[hk[a]]], hs[humans[hk[b]]]
		if age(x) != age(y) {
			return age(x) < age(y)
		}
		return x.ID < y.ID
	})
	fs := slices.Clone(faces)
	sort.Slice(fs, func(a, b int) bool {
		if pf[fs[a]].Age != pf[fs[b]].Age {
			return pf[fs[a]].Age < pf[fs[b]].Age
		}
		return pf[fs[a]].ID < pf[fs[b]].ID
	})
	n, m := len(hk), len(fs)
	const inf = 1 << 30
	dp := make([][]int, n+1)
	for i := range dp {
		dp[i] = make([]int, m+1)
		for j := range dp[i] {
			if i > 0 && j < i {
				dp[i][j] = inf
			}
		}
	}
	for i := 1; i <= n; i++ {
		for j := i; j <= m; j++ {
			take := dp[i-1][j-1] + abs(age(hs[humans[hk[i-1]]])-pf[fs[j-1]].Age)
			dp[i][j] = min(dp[i][j-1], take)
			if j == i {
				dp[i][j] = take
			}
		}
	}
	out := map[int]int{}
	for i, j := n, m; i > 0; j-- {
		if j > i && dp[i][j] == dp[i][j-1] {
			continue
		}
		out[hk[i-1]] = fs[j-1]
		i--
	}
	return out
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
