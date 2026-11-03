package fpgen

import (
	"bytes"
	"testing"
)

// A print is a pure function of its stream: the same key gives the same
// pixels, and a different key a different print.
func TestDeterministic(t *testing.T) {
	a := NewMaster(Stream(1, "finger:a"))
	b := NewMaster(Stream(1, "finger:a"))
	if a.Class != b.Class || !bytes.Equal(a.Image().Pix, b.Image().Pix) {
		t.Fatal("same stream, different master")
	}
	r1 := a.Rolled(Stream(1, "rolled:x")).Pix
	if !bytes.Equal(r1, b.Rolled(Stream(1, "rolled:x")).Pix) {
		t.Fatal("same stream, different rolled impression")
	}
	if bytes.Equal(r1, a.Rolled(Stream(1, "rolled:y")).Pix) {
		t.Fatal("two impressions of one finger are identical")
	}
	l1, area := a.Latent(Stream(1, "latent:x"))
	l2, _ := b.Latent(Stream(1, "latent:x"))
	if !bytes.Equal(l1.Pix, l2.Pix) {
		t.Fatal("same stream, different latent")
	}
	if area <= 0.05 || area >= 0.6 {
		t.Fatalf("latent covers %.2f of the finger, want a partial deposit", area)
	}
	c := NewMaster(Stream(1, "finger:c"))
	if bytes.Equal(a.Image().Pix, c.Image().Pix) {
		t.Fatal("two fingers are identical")
	}
}
