package load

import (
	"testing"
	"time"
)

func TestRelease(t *testing.T) {
	w := 4 * time.Minute
	for _, c := range []struct {
		i, n   int
		window time.Duration
		want   time.Duration
	}{
		{0, 5, w, 0},
		{4, 5, w, w},
		{2, 5, w, 2 * time.Minute},
		{3, 5, 0, 0},
		{0, 1, w, 0},
	} {
		if got := Release(c.i, c.n, c.window); got != c.want {
			t.Errorf("Release(%d, %d, %s) = %s, want %s", c.i, c.n, c.window, got, c.want)
		}
	}
}
