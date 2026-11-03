package world

import (
	"testing"

	"github.com/rodfileto/trackid-sim/onomastic"
)

// A world far larger than the pool keeps every photo and facial trace, and every
// human who needs a face (or a variant) has one.
func TestReuseFacesKeepsEverything(t *testing.T) {
	c := DefaultConfig()
	c.Humans = 3000
	w, err := Generate(c, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	cases := len(w.Cases)
	pool, ref, err := LoadFacePool(poolIndex)
	if err != nil {
		t.Fatal(err)
	}
	s := ReuseFaces(w, pool, ref)
	t.Logf("%+v", s)
	if s.Assigned != s.Needed || s.CaptureAssigned != s.CaptureNeeded {
		t.Fatalf("not every human got a face: %+v", s)
	}
	if s.Needed <= len(pool.Faces) {
		t.Fatalf("world too small to need reuse: %d humans need a face, pool has %d", s.Needed, len(pool.Faces))
	}
	if len(w.Cases) != cases || s.DroppedPhotos+s.DroppedTraces+s.DroppedCases != 0 {
		t.Fatalf("reuse dropped something: %+v, cases %d -> %d", s, cases, len(w.Cases))
	}
	for _, h := range w.Humans {
		if h.Variant != "" && h.Face == "" {
			t.Fatalf("%s has a variant but no face", h.ID)
		}
	}
}
