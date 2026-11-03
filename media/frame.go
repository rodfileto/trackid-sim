// Package media turns the world's face and finger bindings into images trackid
// can show: enrollment photos (the pool's base faces, as they are), CCTV
// evidence frames built from the kept variants, and fingerprints -- rolled
// impressions for NIST files and lift cards for fingerprint evidence (see
// Prints).
//
// An evidence frame is a plain scene -- a wall, a counter across the bottom --
// with one figure per trace standing behind the counter, then degraded the way
// a capture chain would: optics blur, sensor noise, JPEG last. Everything is
// drawn from the caller's rand, so a frame is a pure function of the seed.
//
// The variants are waist-up studio photographs on flat grey, so a figure is not
// cut out: its backdrop is shifted, row by row, onto the wall colour and its
// edges feathered into the wall. The counter hides the photograph's bottom edge.
// This is for display and scoring, not realism (see ROADMAP C3).
package media

import (
	"image"
	"math"
	"math/rand/v2"

	"golang.org/x/image/draw"

	"github.com/rodfileto/trackid-sim/world"
)

// Frame is the evidence image size, as degrade.py used.
const (
	FrameW = 1920
	FrameH = 1080
)

// Figure is one person to place: a variant image and its detected face box in
// that image's pixels (x1, y1, x2, y2).
type Figure struct {
	Img  image.Image
	Face [4]float64
}

// Box is a face box in frame pixels.
type Box struct {
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
	X2 int `json:"x2"`
	Y2 int `json:"y2"`
}

// Placement records where a figure went, for world.json.
type Placement struct {
	X     int     `json:"x"`
	Y     int     `json:"y"`
	Scale float64 `json:"scale"`
	Box   Box     `json:"box"`
}

// Scene draws figs side by side behind a counter and returns the clean frame
// and each figure's placement, in the order given. More figures than fit at
// lvl.FacePx are all scaled down together: a wider shot, smaller faces.
func Scene(figs []Figure, lvl world.Capture, r *rand.Rand) (*image.RGBA, []Placement) {
	dst := image.NewRGBA(image.Rect(0, 0, FrameW, FrameH))

	// Wall: warm light grey, darker towards the ceiling, one hue per scene.
	base := [3]float64{168 + 20*r.Float64(), 166 + 18*r.Float64(), 160 + 16*r.Float64()}
	wall := func(y int) [3]float64 {
		k := 0.82 + 0.18*float64(y)/FrameH
		return [3]float64{base[0] * k, base[1] * k, base[2] * k}
	}
	for y := 0; y < FrameH; y++ {
		c := wall(y)
		for x := 0; x < FrameW; x++ {
			set(dst, x, y, c)
		}
	}

	// Scale: the level's face size, unless the figures would not fit side by side.
	scales := make([]float64, len(figs))
	width := 0.0
	for i, f := range figs {
		scales[i] = math.Min(1, lvl.FacePx/(f.Face[3]-f.Face[1]))
		width += float64(f.Img.Bounds().Dx()) * scales[i]
	}
	if fit := 0.96 * FrameW / width; fit < 1 {
		for i := range scales {
			scales[i] *= fit
		}
		width *= fit
	}

	// Counter top: low enough to leave every face in view, high enough to hide
	// the figures' cut-off bottoms. Its height follows the figures' scale: a
	// camera far enough away to make faces small makes the counter small too.
	mean := 0.0
	for _, s := range scales {
		mean += s
	}
	mean /= float64(len(scales))
	depth := math.Min(1, math.Max(0.3, mean/0.6))
	counterY := FrameH - int(FrameH*(0.18+0.08*r.Float64())*depth)

	gap := (FrameW - width) / float64(len(figs)+1)
	x := gap * (0.4 + 0.6*r.Float64())
	out := make([]Placement, len(figs))
	for i, f := range figs {
		s := scales[i]
		w := int(math.Round(float64(f.Img.Bounds().Dx()) * s))
		h := int(math.Round(float64(f.Img.Bounds().Dy()) * s))
		small := image.NewRGBA(image.Rect(0, 0, w, h))
		draw.CatmullRom.Scale(small, small.Bounds(), f.Img, f.Img.Bounds(), draw.Src, nil)

		// Stand behind the counter: the bottom edge well below it, the face
		// clear of it, the head inside the frame.
		faceBottom := f.Face[3] * s
		faceTop := f.Face[1] * s
		lo := float64(counterY-h) + 12                                  // bottom edge hidden
		hi := float64(counterY) - faceBottom - 0.6*(faceBottom-faceTop) // chin above the counter
		lo = math.Max(lo, 8-faceTop*0.55)                               // hair inside the frame
		y := lo
		if hi > lo {
			y = lo + (hi-lo)*r.Float64()
		}
		px, py := int(math.Round(x)), int(math.Round(y))
		headTop := faceTop - 0.45*(faceBottom-faceTop)
		blend(dst, small, px, py, math.Max(0, headTop), wall)
		out[i] = Placement{X: px, Y: py, Scale: s, Box: Box{
			X1: px + int(math.Round(f.Face[0]*s)), Y1: py + int(math.Round(f.Face[1]*s)),
			X2: px + int(math.Round(f.Face[2]*s)), Y2: py + int(math.Round(f.Face[3]*s)),
		}}
		x += float64(w) + gap*(0.7+0.6*r.Float64())
	}

	counter(dst, counterY, r)
	return dst, out
}

// blend composites src at (ox, oy). The studio backdrop is moved onto the wall
// colour first. Its colour is modelled from the borders: per row, the left and
// right strips interpolated across, plus, per column, how far the top strip
// departs from that -- the studio light's bright centre, which runs down behind
// the person. The difference to the wall is added to every pixel, the person
// included, which reads as a lighting change. A feather at the left and right
// edges, and a longer one from the top down to just above the head (headTop,
// in src pixels), takes out what is left.
func blend(dst *image.RGBA, src *image.RGBA, ox, oy int, headTop float64, wall func(int) [3]float64) {
	w, h := src.Bounds().Dx(), src.Bounds().Dy()
	strip := max(2, w/40)
	left := make([][3]float64, h)
	right := make([][3]float64, h)
	for y := 0; y < h; y++ {
		for x := 0; x < strip; x++ {
			a, b := get(src, x, y), get(src, w-1-x, y)
			for c := 0; c < 3; c++ {
				left[y][c] += a[c] / float64(strip)
				right[y][c] += b[c] / float64(strip)
			}
		}
	}
	left, right = smooth(left, h/30), smooth(right, h/30)

	// Top strip, per column, as a departure from the side model at those rows.
	top := make([][3]float64, w)
	for x := 0; x < w; x++ {
		t := float64(x) / float64(w-1)
		for y := 0; y < strip; y++ {
			p := get(src, x, y)
			for c := 0; c < 3; c++ {
				top[x][c] += (p[c] - (left[y][c]*(1-t) + right[y][c]*t)) / float64(strip)
			}
		}
	}
	top = smooth(top, w/30)

	feather := float64(max(3, w/28))
	featherTop := math.Max(feather, 0.8*headTop)
	for y := 0; y < h; y++ {
		dy := oy + y
		if dy < 0 || dy >= FrameH {
			continue
		}
		wc := wall(dy)
		for x := 0; x < w; x++ {
			dx := ox + x
			if dx < 0 || dx >= FrameW {
				continue
			}
			t := float64(x) / float64(w-1)
			p := get(src, x, y)
			var q [3]float64
			for c := 0; c < 3; c++ {
				bg := left[y][c]*(1-t) + right[y][c]*t + top[x][c]
				q[c] = p[c] + (wc[c] - bg)
			}
			a := math.Min(1, math.Min(float64(x), float64(w-1-x))/feather)
			a = math.Min(a, float64(y)/featherTop)
			a = a * a * (3 - 2*a) // smoothstep
			d := get(dst, dx, dy)
			for c := 0; c < 3; c++ {
				q[c] = d[c]*(1-a) + q[c]*a
			}
			set(dst, dx, dy, q)
		}
	}
}

// counter draws a service counter from y to the bottom: a lit top edge, then a
// darker front panel with a faint vertical grain.
func counter(dst *image.RGBA, y int, r *rand.Rand) {
	finishes := [][3]float64{{150, 112, 78}, {128, 96, 70}, {118, 120, 122}, {92, 94, 98}} // oak, walnut, grey laminate, charcoal
	top := finishes[r.IntN(len(finishes))]
	j := 0.92 + 0.16*r.Float64()
	top = [3]float64{top[0] * j, top[1] * j, top[2] * j}
	edge := FrameH / 90
	for yy := y; yy < FrameH; yy++ {
		k := 0.55 - 0.15*float64(yy-y)/float64(FrameH-y)
		if yy < y+edge {
			k = 1.15
		}
		for x := 0; x < FrameW; x++ {
			g := 1 + 0.04*math.Sin(float64(x)*0.07+math.Sin(float64(x)*0.013)*3)
			set(dst, x, yy, [3]float64{top[0] * k * g, top[1] * k * g, top[2] * k * g})
		}
	}
}

// smooth box-filters a column of colours over ±k rows.
func smooth(v [][3]float64, k int) [][3]float64 {
	out := make([][3]float64, len(v))
	for i := range v {
		lo, hi := max(0, i-k), min(len(v)-1, i+k)
		for j := lo; j <= hi; j++ {
			for c := 0; c < 3; c++ {
				out[i][c] += v[j][c]
			}
		}
		for c := 0; c < 3; c++ {
			out[i][c] /= float64(hi - lo + 1)
		}
	}
	return out
}

func get(img *image.RGBA, x, y int) [3]float64 {
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+3 : i+3]
	return [3]float64{float64(p[0]), float64(p[1]), float64(p[2])}
}

func set(img *image.RGBA, x, y int, c [3]float64) {
	i := img.PixOffset(x, y)
	p := img.Pix[i : i+4 : i+4]
	p[0], p[1], p[2], p[3] = clamp8(c[0]), clamp8(c[1]), clamp8(c[2]), 255
}

func clamp8(v float64) uint8 {
	switch {
	case v <= 0:
		return 0
	case v >= 255:
		return 255
	}
	return uint8(v + 0.5)
}
