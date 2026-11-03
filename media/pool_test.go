package media

import (
	"math/rand/v2"
	"testing"
	"time"
)

// A frame's DVR time lies within its occurrence: inside [from, to], or within five minutes of
// from when the occurrence has no end, in from's zone.
func TestDVRTime(t *testing.T) {
	brt := time.FixedZone("BRT", -3*3600)
	from := time.Date(2022, 5, 7, 21, 30, 0, 0, brt)
	to := from.Add(40 * time.Minute)
	r := rand.New(rand.NewPCG(1, 2))
	for i := 0; i < 500; i++ {
		if at := dvrTime(r, from, &to); at.Before(from) || at.After(to) {
			t.Fatalf("%v outside %v-%v", at, from, to)
		}
		if at := dvrTime(r, from, nil); at.Before(from) || at.After(from.Add(5*time.Minute)) || at.Location() != brt {
			t.Fatalf("%v not within five minutes of %v in BRT", at, from)
		}
	}
}
