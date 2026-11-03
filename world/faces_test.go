package world

import (
	"testing"

	"github.com/rodfileto/trackid-sim/onomastic"
)

const poolIndex = "../tools/facegen/pool_index.json"

func boundWorld(t *testing.T, humans int) (*World, *FacePool, FaceSummary) {
	t.Helper()
	c := DefaultConfig()
	c.Humans = humans
	w, err := Generate(c, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	pool, ref, err := LoadFacePool(poolIndex)
	if err != nil {
		t.Fatal(err)
	}
	s := AssignFaces(w, pool, ref)
	t.Logf("%d humans: %+v", humans, s)
	return w, pool, s
}

// What survives assignment is consistent: every photo and every facial trace
// belongs to a human who has the face (or kept variant) to show it, faces
// are not shared, and sex matches.
func checkBound(t *testing.T, w *World, pool *FacePool) {
	t.Helper()
	faces := map[string]PoolFace{}
	for _, f := range pool.Faces {
		faces[f.ID] = f
	}
	humans := map[string]Human{}
	owner := map[string]string{}
	for _, h := range w.Humans {
		humans[h.ID] = h
		if h.Face == "" {
			if h.Variant != "" {
				t.Fatalf("%s has a variant but no face", h.ID)
			}
			continue
		}
		f, ok := faces[h.Face]
		if !ok {
			t.Fatalf("%s bound to unknown face %s", h.ID, h.Face)
		}
		if o, dup := owner[h.Face]; dup {
			t.Fatalf("face %s shared by %s and %s", h.Face, o, h.ID)
		}
		owner[h.Face] = h.ID
		if poolSex[f.Sex] != h.Sex {
			t.Fatalf("%s (%s) bound to a %s face", h.ID, h.Sex, f.Sex)
		}
		if (f.Variant != nil) != (h.Variant != "") || (f.Variant != nil && f.Variant.ID != h.Variant) {
			t.Fatalf("%s: variant %q does not match face %s", h.ID, h.Variant, h.Face)
		}
	}
	for _, e := range w.Enrollments {
		if len(e.Files) == 0 {
			t.Fatalf("enrollment %s has no files", e.RegisterNumber)
		}
		for _, f := range e.Files {
			if f.FileType == "photo" && humans[e.HumanID].Face == "" {
				t.Fatalf("enrollment %s has a photo but %s has no face", e.RegisterNumber, e.HumanID)
			}
		}
	}
	for _, c := range w.Cases {
		if len(c.Evidences) == 0 {
			t.Fatalf("case %s left empty", c.CaseID)
		}
		for _, ev := range c.Evidences {
			if len(ev.Traces) == 0 {
				t.Fatalf("case %s evidence %d left empty", c.CaseID, ev.Sequence)
			}
			for _, tr := range ev.Traces {
				if c.Modality == "FACIAL" && humans[tr.HumanID].Variant == "" {
					t.Fatalf("case %s: trace of %s, who has no kept variant", c.CaseID, tr.HumanID)
				}
			}
		}
	}
}

// At the default size the pool covers everyone: nothing is dropped.
func TestDefaultWorldFitsPool(t *testing.T) {
	w, pool, s := boundWorld(t, DefaultConfig().Humans)
	checkBound(t, w, pool)
	if s.Assigned != s.Needed || s.CaptureAssigned != s.CaptureNeeded || s.DoublesWithFaces != s.Doubles {
		t.Fatalf("default world should fit the pool: %+v", s)
	}
	if s.DroppedPhotos+s.DroppedTraces+s.DroppedCases != 0 {
		t.Fatalf("nothing should be dropped at the default size: %+v", s)
	}
	if s.MeanAgeGap > 5 {
		t.Fatalf("mean age gap %.1f years", s.MeanAgeGap)
	}
}

// A larger world overflows the pool: what is kept stays consistent, and
// double-identity pairs are served before anyone else.
func TestLargeWorldPrioritisesDoubles(t *testing.T) {
	w, pool, s := boundWorld(t, 1000)
	checkBound(t, w, pool)
	if s.Assigned >= s.Needed || s.DroppedPhotos == 0 {
		t.Fatalf("expected a shortfall at 1000 humans: %+v", s)
	}
	// 50 pairs need up to 100 faces of 200, split over two sexes: all fit.
	if s.DoublesWithFaces != s.Doubles {
		t.Fatalf("pairs should be served first: %d of %d have faces", s.DoublesWithFaces, s.Doubles)
	}
}
