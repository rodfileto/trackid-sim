package media

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/rodfileto/trackid-sim/fpgen"
	"github.com/rodfileto/trackid-sim/world"
)

// A lift card holds one latent per trace, in trace order, each boxed by its
// tile, and renders the same twice.
func TestLiftCard(t *testing.T) {
	humans := map[string]world.Human{"H1": {ID: "H1"}, "H2": {ID: "H2"}}
	c := world.Case{CaseID: "SIM-2015-00001", Modality: "FINGERPRINT"}
	ev := world.Evidence{Sequence: 1, Traces: []world.Trace{{Sequence: 1, HumanID: "H2"}, {Sequence: 2, HumanID: "H1"}}}
	data, boxes, err := NewPrints(7).LiftCard(c, ev, humans)
	if err != nil {
		t.Fatal(err)
	}
	img, err := png.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if len(boxes) != 2 || boxes[1].X1 != fpgen.W+liftGap || boxes[1].X2 != img.Bounds().Dx() || boxes[0].Y2 != fpgen.H {
		t.Fatalf("boxes %v on a %v card", boxes, img.Bounds())
	}
	again, _, _ := NewPrints(7).LiftCard(c, ev, humans)
	if !bytes.Equal(data, again) {
		t.Fatal("lift card is not deterministic")
	}
	if _, _, err := NewPrints(7).LiftCard(c, world.Evidence{Traces: []world.Trace{{HumanID: "nobody"}}}, humans); err == nil {
		t.Fatal("a trace of an unknown human rendered")
	}
}
