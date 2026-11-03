package world

import "math/rand/v2"

// ReuseFaces binds a pool face to every human who needs one, sharing faces between
// humans when the pool is short, so nothing is dropped. It is for the stress test
// (docs/stress-test-plan.md in ../trackid): a world larger than the pool keeps all
// its photos and facial traces, and trackid's detector, embedder, matcher and
// clusterer do the work they would on that many people. Humans who share a face
// look like one person to the matcher, so a world bound this way measures time and
// resources only, never accuracy: eval on it scores the reuse, not trackid.
//
// Faces are dealt by sex in a seeded order, round robin; a human captured on
// camera gets a face with a kept variant. Age is not matched.
func ReuseFaces(w *World, pool *FacePool, ref FacePoolRef) FaceSummary {
	r := rand.New(rand.NewPCG(w.Config.Seed, 0xface2))
	idx, needFace, needVariant := faceNeeds(w)

	all, kept := map[string][]int{}, map[string][]int{}
	for i, f := range pool.Faces {
		sex := poolSex[f.Sex]
		all[sex] = append(all[sex], i)
		if f.Variant != nil {
			kept[sex] = append(kept[sex], i)
		}
	}
	for _, m := range []map[string][]int{all, kept} {
		for _, faces := range m {
			r.Shuffle(len(faces), func(i, j int) { faces[i], faces[j] = faces[j], faces[i] })
		}
	}
	next := map[string]int{}
	deal := func(faces []int, key string) (int, bool) {
		if len(faces) == 0 {
			return 0, false
		}
		f := faces[next[key]%len(faces)]
		next[key]++
		return f, true
	}

	var s FaceSummary
	for i := range w.Humans {
		if !needFace[i] {
			continue
		}
		h := &w.Humans[i]
		faces, key := all[h.Sex], "all/"+h.Sex
		if needVariant[i] {
			faces, key = kept[h.Sex], "kept/"+h.Sex
		}
		f, ok := deal(faces, key)
		if !ok {
			continue
		}
		h.Face = pool.Faces[f].ID
		if v := pool.Faces[f].Variant; v != nil {
			h.Variant = v.ID
		}
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
	w.FacePool = &ref
	return s
}
