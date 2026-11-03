package fpgen

import (
	"math"
	"math/rand/v2"
	"runtime"
	"sync"
)

// H and W are the print's size in pixels at 500 dpi: ~24 x 20 mm.
const (
	H = 480
	W = 400
)

// grid is an H x W field of values, row-major. mask is its boolean twin.
type (
	grid []float64
	mask []bool
)

func newGrid() grid { return make(grid, H*W) }

// each calls f for every pixel with its coordinates and index.
func each(f func(x, y, i int)) {
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			f(x, y, y*W+x)
		}
	}
}

// rows runs f over row bands in parallel. f must write only its own rows, so
// the result does not depend on scheduling.
func rows(f func(y0, y1 int)) {
	n := runtime.GOMAXPROCS(0)
	band := (H + n - 1) / n
	var wg sync.WaitGroup
	for y0 := 0; y0 < H; y0 += band {
		wg.Add(1)
		go func(y0 int) {
			defer wg.Done()
			f(y0, min(y0+band, H))
		}(y0)
	}
	wg.Wait()
}

func normal(r *rand.Rand) grid {
	g := newGrid()
	for i := range g {
		g[i] = r.NormFloat64()
	}
	return g
}

func uniform(r *rand.Rand, lo, hi float64) float64 { return lo + (hi-lo)*r.Float64() }

func clamp(v, lo, hi float64) float64 { return math.Min(math.Max(v, lo), hi) }

func absMax(g grid) float64 {
	m := 0.0
	for _, v := range g {
		m = math.Max(m, math.Abs(v))
	}
	return m
}

// reflect maps an index outside [0, n) back in by half-sample symmetry
// (d c b a | a b c d | d c b a), scipy.ndimage's "reflect" mode.
func reflect(i, n int) int {
	p := 2 * n
	i %= p
	if i < 0 {
		i += p
	}
	if i >= n {
		i = p - 1 - i
	}
	return i
}

// gaussian blurs separably out to 4 sigma with reflected borders, as
// scipy.ndimage.gaussian_filter does by default.
func gaussian(src grid, sigma float64) grid {
	rad := int(4*sigma + 0.5)
	k := make([]float64, 2*rad+1)
	sum := 0.0
	for i := range k {
		d := float64(i - rad)
		k[i] = math.Exp(-d * d / (2 * sigma * sigma))
		sum += k[i]
	}
	for i := range k {
		k[i] /= sum
	}
	tmp, out := newGrid(), newGrid()
	rows(func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < W; x++ {
				acc := 0.0
				for j, kv := range k {
					acc += kv * src[y*W+reflect(x+j-rad, W)]
				}
				tmp[y*W+x] = acc
			}
		}
	})
	rows(func(y0, y1 int) {
		for y := y0; y < y1; y++ {
			for x := 0; x < W; x++ {
				acc := 0.0
				for j, kv := range k {
					acc += kv * tmp[reflect(y+j-rad, H)*W+x]
				}
				out[y*W+x] = acc
			}
		}
	})
	return out
}

// smoothNoise is blurred white noise rescaled to zero mean, unit deviation.
func smoothNoise(r *rand.Rand, sigma float64) grid {
	n := gaussian(normal(r), sigma)
	mean, sq := 0.0, 0.0
	for _, v := range n {
		mean += v
	}
	mean /= float64(len(n))
	for _, v := range n {
		sq += (v - mean) * (v - mean)
	}
	sd := math.Sqrt(sq/float64(len(n))) + 1e-9
	for i := range n {
		n[i] = (n[i] - mean) / sd
	}
	return n
}

// cubic is the Keys cubic convolution weight (a = -0.5). It interpolates,
// unlike scipy's prefiltered B-spline, which is close enough here: nothing
// downstream depends on matching scipy pixel for pixel.
func cubic(t float64) float64 {
	t = math.Abs(t)
	switch {
	case t < 1:
		return (1.5*t-2.5)*t*t + 1
	case t < 2:
		return ((-0.5*t+2.5)*t-4)*t + 2
	}
	return 0
}

// sampleCubic reads src at (x, y). Taps outside the grid read cval, or the
// nearest edge pixel when nearest is set; a point outside the grid is cval.
func sampleCubic(src grid, x, y, cval float64, nearest bool) float64 {
	if !nearest && (x < 0 || x > W-1 || y < 0 || y > H-1) {
		return cval
	}
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	acc := 0.0
	for j := -1; j <= 2; j++ {
		wy := cubic(y - float64(y0+j))
		yy := y0 + j
		for i := -1; i <= 2; i++ {
			wx := cubic(x - float64(x0+i))
			xx := x0 + i
			var v float64
			switch {
			case nearest:
				v = src[min(max(yy, 0), H-1)*W+min(max(xx, 0), W-1)]
			case xx < 0 || xx >= W || yy < 0 || yy >= H:
				v = cval
			default:
				v = src[yy*W+xx]
			}
			acc += wx * wy * v
		}
	}
	return acc
}

// sampleLinear reads src at (x, y) bilinearly; outside the grid is 0.
func sampleLinear(src grid, x, y float64) float64 {
	if x < 0 || x > W-1 || y < 0 || y > H-1 {
		return 0
	}
	x0, y0 := int(x), int(y)
	x1, y1 := min(x0+1, W-1), min(y0+1, H-1)
	fx, fy := x-float64(x0), y-float64(y0)
	top := src[y0*W+x0]*(1-fx) + src[y0*W+x1]*fx
	bot := src[y1*W+x0]*(1-fx) + src[y1*W+x1]*fx
	return top*(1-fy) + bot*fy
}

// cityBlock returns, per pixel, the city-block distance to the nearest pixel
// where far(i) is false. With border, everything outside the grid counts as
// such a pixel too.
func cityBlock(far func(i int) bool, border bool) []int {
	const inf = 1 << 30
	d := make([]int, H*W)
	each(func(x, y, i int) {
		switch {
		case !far(i):
		case border:
			d[i] = min(x+1, y+1, W-x, H-y)
		default:
			d[i] = inf
		}
	})
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			i := y*W + x
			if x > 0 {
				d[i] = min(d[i], d[i-1]+1)
			}
			if y > 0 {
				d[i] = min(d[i], d[i-W]+1)
			}
		}
	}
	for y := H - 1; y >= 0; y-- {
		for x := W - 1; x >= 0; x-- {
			i := y*W + x
			if x < W-1 {
				d[i] = min(d[i], d[i+1]+1)
			}
			if y < H-1 {
				d[i] = min(d[i], d[i+W]+1)
			}
		}
	}
	return d
}

// erode is n iterations of binary erosion with the 4-neighbour cross and a
// false border, scipy's binary_erosion default: a pixel survives when every
// false pixel is more than n steps away.
func erode(m mask, n int) mask {
	d := cityBlock(func(i int) bool { return m[i] }, true)
	out := make(mask, len(m))
	for i, v := range d {
		out[i] = v > n
	}
	return out
}

// dilate is n iterations of binary dilation with the 4-neighbour cross: a
// pixel is set when a set pixel is at most n steps away.
func dilate(m mask, n int) mask {
	d := cityBlock(func(i int) bool { return !m[i] }, false)
	out := make(mask, len(m))
	for i, v := range d {
		out[i] = v <= n
	}
	return out
}

// largest keeps the largest 4-connected component of m.
func largest(m mask) mask {
	label := make([]int, len(m))
	best, bestSize, next := 0, 0, 0
	var stack []int
	for i, v := range m {
		if !v || label[i] != 0 {
			continue
		}
		next++
		size := 0
		stack = append(stack[:0], i)
		label[i] = next
		for len(stack) > 0 {
			p := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			size++
			x, y := p%W, p/W
			for _, q := range [4][2]int{{x - 1, y}, {x + 1, y}, {x, y - 1}, {x, y + 1}} {
				if q[0] < 0 || q[0] >= W || q[1] < 0 || q[1] >= H {
					continue
				}
				j := q[1]*W + q[0]
				if m[j] && label[j] == 0 {
					label[j] = next
					stack = append(stack, j)
				}
			}
		}
		if size > bestSize {
			best, bestSize = next, size
		}
	}
	out := make(mask, len(m))
	for i, l := range label {
		out[i] = l == best && l != 0
	}
	return out
}

// boxFilter1D is a moving average of the given size along x (axis 1) or y
// (axis 0), with reflected borders, as scipy's uniform_filter1d.
func boxFilter1D(src grid, size, axis int) grid {
	out := newGrid()
	lo := size / 2
	each(func(x, y, i int) {
		acc := 0.0
		for k := 0; k < size; k++ {
			o := k - lo
			if axis == 1 {
				acc += src[y*W+reflect(x+o, W)]
			} else {
				acc += src[reflect(y+o, H)*W+x]
			}
		}
		out[i] = acc / float64(size)
	})
	return out
}
