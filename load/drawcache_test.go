package load

import (
	"bytes"
	"path/filepath"
	"testing"

	"github.com/rodfileto/trackid-sim/onomastic"
	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/cases"
)

// What goes into the draw cache comes back the same, file for file; a cache for another
// world is refused rather than read.
func TestDrawCacheRoundTrip(t *testing.T) {
	c := world.DefaultConfig()
	c.Humans = 60
	w, err := world.Generate(c, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	enrollments := make([]enrollmentMaterial, len(w.Enrollments))
	for i, e := range w.Enrollments {
		in := enrollmentMaterial{e: e, files: make([]rendered, len(e.Files))}
		for k := range e.Files {
			if k%2 == 0 { // leave some as placeholders (nil data)
				in.files[k] = rendered{[]byte{byte(i), byte(k), 1}, "f"}
			}
		}
		enrollments[i] = in
	}
	caseMats := make([]caseMaterial, len(w.Cases))
	for i, cs := range w.Cases {
		in := caseMaterial{c: cs, evidences: make([]renderedEvidence, len(cs.Evidences))}
		for k, ev := range cs.Evidences {
			boxes := make([]*cases.Box, len(ev.Traces))
			for j := range boxes {
				boxes[j] = &cases.Box{X1: float64(j), Y1: 1, X2: 2, Y2: 3}
			}
			in.evidences[k] = renderedEvidence{rendered{[]byte{byte(i), byte(k), 2}, "e"}, boxes}
		}
		caseMats[i] = in
	}

	path := filepath.Join(t.TempDir(), "w.gob")
	if err := writeDrawn(path, enrollments, caseMats); err != nil {
		t.Fatal(err)
	}
	gotE, gotC, err := readDrawn(path, w)
	if err != nil {
		t.Fatal(err)
	}
	for i := range enrollments {
		for k, f := range enrollments[i].files {
			g := gotE[i].files[k]
			if !bytes.Equal(g.data, f.data) || g.name != f.name || (f.data == nil) != (g.data == nil) {
				t.Fatalf("enrollment %d file %d: got %+v, want %+v", i, k, g, f)
			}
		}
	}
	for i := range caseMats {
		for k, ev := range caseMats[i].evidences {
			g := gotC[i].evidences[k]
			if !bytes.Equal(g.data, ev.data) || g.name != ev.name || len(g.boxes) != len(ev.boxes) {
				t.Fatalf("case %d evidence %d differs", i, k)
			}
			for j := range ev.boxes {
				if *g.boxes[j] != *ev.boxes[j] {
					t.Fatalf("case %d evidence %d box %d: got %+v, want %+v", i, k, j, *g.boxes[j], *ev.boxes[j])
				}
			}
		}
	}

	c.Humans = 80
	other, err := world.Generate(c, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := readDrawn(path, other); err == nil {
		t.Fatal("read another world's cache")
	}
	k1, _ := DrawCacheKey(w)
	k2, _ := DrawCacheKey(other)
	if k1 == k2 {
		t.Fatal("two worlds share a cache key")
	}
}
