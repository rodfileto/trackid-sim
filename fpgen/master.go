// Package fpgen generates synthetic fingerprints: a master print per finger,
// then any number of rolled impressions (the KNOWN side, an enrollment's
// ten-print card) and latents (the QUESTIONED side, a lift from a scene).
//
// It is the Go port of tools/fpgen/sketch_one.py, an SFinGe-style pipeline
// (Cappelli & Maltoni): singular points -> orientation field -> ridge
// frequency -> ridges grown from seeds by iterated space-variant Gabor
// filtering -> extra minutiae as phase-spiral dipoles. The Python file holds
// the reasoning behind each constant; this keeps the constants and the order
// of the stages, not numpy's random streams, so a print here is not pixel-equal
// to its Python counterpart. What carries over is checked the way the
// prototype was: SourceAFIS scores of genuine vs impostor pairs (tools/fpscore).
//
// Everything is drawn from the caller's rand, so a print is a pure function
// of its stream. The bar is not realism: prints that look and behave like
// fingerprints for manual comparison and for a real matcher.
package fpgen

import (
	"hash/fnv"
	"image"
	"math"
	"math/cmplx"
	"math/rand/v2"
)

// Pattern classes and their rough population shares; tented arch is folded
// into arch.
var (
	Classes = []string{"left_loop", "right_loop", "whorl", "arch"}
	shares  = []float64{0.33, 0.30, 0.30, 0.07}
)

const nOrient = 16 // orientation bins for the space-variant filter

var periods = []float64{7.5, 8.25, 9.0, 9.75, 10.5} // ridge period bins, px

// Master is one finger's ridge pattern before any impression is taken.
type Master struct {
	Class  string
	ridges grid // -1 valley .. +1 ridge, 0 outside the finger
	mask   mask // the fingertip
}

type point struct{ x, y float64 }

// NewMaster draws a finger: its class, outline and ridges.
func NewMaster(r *rand.Rand) *Master {
	cls := pick(r)
	m := fingertip(r)
	cores, deltas := singularPoints(r, cls)
	theta := orientation(cores, deltas, r)
	period := frequency(r)
	grown := growRidges(theta, period, m, r)
	ridges := addMinutiaePairs(grown, theta, period, m, append(append([]point{}, cores...), deltas...), r, 18+r.IntN(8))
	for i := range ridges {
		if !m[i] {
			ridges[i] = 0
		}
	}
	return &Master{Class: cls, ridges: ridges, mask: m}
}

func pick(r *rand.Rand) string {
	u, acc := r.Float64(), 0.0
	for i, s := range shares {
		if acc += s; u < acc {
			return Classes[i]
		}
	}
	return Classes[len(Classes)-1]
}

// fingertip is a superellipse: round at the tip (p=2), squarer towards the
// joint (p=4).
func fingertip(r *rand.Rand) mask {
	cx, cy := W/2.0, H*0.52
	a, b := W*uniform(r, 0.40, 0.45), H*uniform(r, 0.44, 0.48)
	m := make(mask, H*W)
	each(func(x, y, i int) {
		u, v := (float64(x)-cx)/a, (float64(y)-cy)/b
		p := 4.0
		if v < 0 {
			p = 2
		}
		m[i] = math.Pow(math.Abs(u), p)+math.Pow(math.Abs(v), p) <= 1
	})
	return m
}

// singularPoints places cores and deltas for a class. Placement varies widely
// on purpose: pinned points gave every loop one flow field, and same-class
// impostors matched (see sketch_one.py).
func singularPoints(r *rand.Rand, cls string) (cores, deltas []point) {
	cx := W/2.0 + r.NormFloat64()*22
	cy := H*0.42 + r.NormFloat64()*22
	rad := func(deg float64) float64 { return deg * math.Pi / 180 }
	switch cls {
	case "arch":
		return nil, nil
	case "left_loop", "right_loop":
		side := 1.0
		if cls == "right_loop" {
			side = -1
		}
		dist, ang := uniform(r, 80, 190), rad(uniform(r, 35, 70))
		return []point{{cx, cy}}, []point{{cx + side*dist*math.Cos(ang), cy + dist*math.Sin(ang)}}
	default: // whorl
		dy, tilt := uniform(r, 8, 30), r.NormFloat64()*8
		d1, d2 := uniform(r, 90, 170), uniform(r, 90, 170)
		a1, a2 := rad(uniform(r, 35, 65)), rad(uniform(r, 35, 65))
		return []point{{cx - tilt, cy - dy}, {cx + tilt, cy + dy}},
			[]point{{cx - d1*math.Cos(a1), cy + d1*math.Sin(a1)}, {cx + d2*math.Cos(a2), cy + d2*math.Sin(a2)}}
	}
}

// coreMap remaps a core's angle as a - k sin(a - open), so the loop's legs run
// nearly parallel along its opening instead of fanning out.
type coreMap struct{ open, k float64 }

func zeroPole(z complex128, cores, deltas []point, maps []coreMap) float64 {
	t := 0.0
	for i, c := range cores {
		a := cmplx.Phase(z - complex(c.x, c.y))
		t += 0.5 * (a - maps[i].k*math.Sin(a-maps[i].open))
	}
	for _, d := range deltas {
		t -= 0.5 * cmplx.Phase(z-complex(d.x, d.y))
	}
	return t
}

// orientation is the zero-pole model near the singular points, blended on
// doubled angles into a fingertip flow above them. Radians, mod pi.
func orientation(cores, deltas []point, r *rand.Rand) grid {
	maps := make([]coreMap, len(cores))
	for i, c := range cores {
		best, bestOff := 0.0, math.Inf(1)
		for k := 0; k < 720; k++ {
			alpha := -math.Pi + 2*math.Pi*float64(k)/720
			ring := complex(c.x, c.y) + 40*cmplx.Exp(complex(0, alpha))
			if off := math.Abs(math.Sin(zeroPole(ring, cores, deltas, maps) - alpha)); off < bestOff {
				best, bestOff = alpha, off
			}
		}
		maps[i] = coreMap{best, uniform(r, 0.55, 0.75)}
	}
	hump := 0.0
	if len(cores) == 0 {
		hump = uniform(r, 0.45, 0.7)
	}
	top := H * 0.36
	for i, c := range cores {
		if i == 0 || c.y < top {
			top = c.y
		}
	}
	pts := append(append([]point{}, cores...), deltas...)
	jitter := r.NormFloat64() * 0.03

	theta := newGrid()
	ex, ey, ea, eb := W/2.0, H*0.60, W*0.50, H*0.50
	each(func(xi, yi, i int) {
		x, y := float64(xi), float64(yi)
		var sp float64
		if len(cores) > 0 {
			sp = zeroPole(complex(x, y), cores, deltas, maps)
		} else {
			sp = -hump * math.Sin(math.Pi*(x-W/2)/W) * math.Exp(-math.Pow((y-H*0.5)/(H*0.25), 2))
		}
		bg := math.Atan2((y-ey)/(eb*eb), (x-ex)/(ea*ea)) + math.Pi/2
		dist := 1e9
		for _, p := range pts {
			dist = math.Min(dist, math.Hypot(x-p.x, y-p.y))
		}
		w := clamp((top-20-y)/110, 0, 1) * clamp((dist-30)/80, 0, 1)
		w = w * w * (3 - 2*w)
		c2 := (1-w)*math.Cos(2*sp) + w*math.Cos(2*bg)
		s2 := (1-w)*math.Sin(2*sp) + w*math.Sin(2*bg)
		theta[i] = math.Mod(0.5*math.Atan2(s2, c2)+jitter+2*math.Pi, math.Pi)
	})
	return theta
}

// frequency is the ridge period: a base per finger, a broad +/-8% drift and a
// finer +/-6% ripple. Where the period changes, minutiae have to appear.
func frequency(r *rand.Rand) grid {
	broad, fine := gaussian(normal(r), 40), gaussian(normal(r), 12)
	bm, fm := absMax(broad)+1e-9, absMax(fine)+1e-9
	base := uniform(r, 8.3, 9.7)
	out := newGrid()
	for i := range out {
		out[i] = base * (1 + 0.08*broad[i]/bm + 0.06*fine[i]/fm)
	}
	return out
}

// gaborSize is the side of each filter kernel.
const gaborSize = 25

// gabor is an even Gabor: cosine across the ridges, elongated along them.
func gabor(theta, period float64) []float64 {
	rad := gaborSize / 2
	g := make([]float64, gaborSize*gaborSize)
	sx, sy := period*0.55, period*0.9
	mean := 0.0
	for j := -rad; j <= rad; j++ {
		for i := -rad; i <= rad; i++ {
			x, y := float64(i), float64(j)
			along := x*math.Cos(theta) + y*math.Sin(theta)
			across := -x*math.Sin(theta) + y*math.Cos(theta)
			v := math.Exp(-0.5*(across*across/(sx*sx)+along*along/(sy*sy))) * math.Cos(2*math.Pi*across/period)
			g[(j+rad)*gaborSize+i+rad] = v
			mean += v
		}
	}
	mean /= float64(len(g))
	for k := range g {
		g[k] -= mean
	}
	return g
}

// growRidges iterates: filter each pixel with the Gabor for its own
// orientation and period, then clip. Ridges spread from random seeds, and
// minutiae form where growing fronts meet out of phase. It stops two passes
// after the ridges cover the finger: iterating on would anneal those defects
// away.
func growRidges(theta, period grid, m mask, r *rand.Rand) grid {
	const nSeeds, maxIter, minIter = 60, 80, 8
	bank := make([][]float64, nOrient*len(periods))
	for o := 0; o < nOrient; o++ {
		for k, p := range periods {
			bank[o*len(periods)+k] = gabor(float64(o)*math.Pi/nOrient, p)
		}
	}
	kernel := make([]int, H*W)
	for i := range kernel {
		o := int(math.Round(theta[i]/math.Pi*nOrient)) % nOrient
		k, best := 0, math.Inf(1)
		for j, p := range periods {
			if d := math.Abs(period[i] - p); d < best {
				k, best = j, d
			}
		}
		kernel[i] = o*len(periods) + k
	}

	img := newGrid()
	var in []int
	for i, v := range m {
		if v {
			in = append(in, i)
		}
	}
	for s := 0; s < nSeeds; s++ { // partial Fisher-Yates: distinct seed pixels
		j := s + r.IntN(len(in)-s)
		in[s], in[j] = in[j], in[s]
		img[in[s]] = float64(2*r.IntN(2) - 1)
	}

	inner := erode(m, 6)
	const rad = gaborSize / 2
	const pw = W + 2*rad
	padded := make([]float64, (H+2*rad)*pw)
	covered := 0
	for it := 1; it <= maxIter; it++ {
		for y := 0; y < H; y++ {
			copy(padded[(y+rad)*pw+rad:], img[y*W:(y+1)*W])
		}
		out := newGrid()
		// The kernels are point-symmetric, so convolution is correlation.
		rows(func(y0, y1 int) {
			for y := y0; y < y1; y++ {
				for x := 0; x < W; x++ {
					g := bank[kernel[y*W+x]]
					acc := 0.0
					for j := 0; j < gaborSize; j++ {
						row := padded[(y+j)*pw+x : (y+j)*pw+x+gaborSize]
						gk := g[j*gaborSize : (j+1)*gaborSize]
						for i, v := range row {
							acc += v * gk[i]
						}
					}
					out[y*W+x] = acc
				}
			}
		})
		scale := 4 / (absMax(out) + 1e-9)
		for i, v := range out {
			img[i] = clamp(v*scale, -1, 1)
		}
		if covered == 0 {
			sq := newGrid()
			for i, v := range img {
				sq[i] = v * v
			}
			energy := gaussian(sq, 5)
			n, ok := 0, 0
			for i, v := range inner {
				if v {
					n++
					if energy[i] > 0.15 {
						ok++
					}
				}
			}
			if float64(ok) > 0.995*float64(n) {
				covered = it
			}
		}
		if covered > 0 && it >= covered+2 && it >= minIter {
			break
		}
	}
	return img
}

// addMinutiaePairs adds n dipoles: two opposite phase spirals, applied as a
// shift across the ridges, so each reads as a broken ridge, a short ridge,
// an enclosure or two separate minutiae, and leaves the far ridges untouched.
// Gabor growth alone tops out around 50-60 minutiae, bunched where the flow
// forces them.
func addMinutiaePairs(master, theta, period grid, m mask, pts []point, r *rand.Rand, n int) grid {
	inner := erode(m, 25)
	out := append(grid{}, master...)
	var placed []point
	for tries := 0; len(placed) < n && tries < 5000; tries++ {
		cyI, cxI := r.IntN(H), r.IntN(W)
		if !inner[cyI*W+cxI] {
			continue
		}
		cx, cy := float64(cxI), float64(cyI)
		near := func(ps []point, d float64) bool {
			for _, p := range ps {
				if math.Hypot(cx-p.x, cy-p.y) < d {
					return true
				}
			}
			return false
		}
		if near(pts, 45) || near(placed, 40) {
			continue
		}
		placed = append(placed, point{cx, cy})
		t, T := theta[cyI*W+cxI], period[cyI*W+cxI]
		nx, ny := -math.Sin(t), math.Cos(t) // normal to the ridges
		half := uniform(r, 8, 30)
		a := uniform(r, 0, math.Pi)
		dx, dy := half*math.Cos(a), half*math.Sin(a)
		R := half*4 + 30

		y0, y1 := max(0, int(cy-R)), min(H-1, int(cy+R))
		x0, x1 := max(0, int(cx-R)), min(W-1, int(cx+R))
		bw := x1 - x0 + 1
		box := make([]float64, (y1-y0+1)*bw)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				fx, fy := float64(x), float64(y)
				ang1 := math.Atan2(fy-(cy+dy), fx-(cx+dx))
				ang2 := math.Atan2(fy-(cy-dy), fx-(cx-dx))
				dpsi := math.Remainder(ang1-ang2, 2*math.Pi)
				win := clamp(1-((fx-cx)*(fx-cx)+(fy-cy)*(fy-cy))/(R*R), 0, 1)
				shift := dpsi / (2 * math.Pi) * T * win * win
				box[(y-y0)*bw+x-x0] = sampleCubic(out, fx-shift*nx, fy-shift*ny, 0, true)
			}
		}
		for y := y0; y <= y1; y++ {
			copy(out[y*W+x0:y*W+x1+1], box[(y-y0)*bw:(y-y0+1)*bw])
		}
	}
	// Resampling softens where windows overlap; restore ridge contrast.
	norm := math.Tanh(2.5)
	for i, v := range out {
		out[i] = math.Tanh(2.5*v) / norm
	}
	return out
}

// Stream returns the rand a print or impression draws from, keyed by a
// world seed and a name, so each one is independent of which were made
// before it.
func Stream(seed uint64, key string) *rand.Rand {
	h := fnv.New64a()
	h.Write([]byte(key))
	return rand.New(rand.NewPCG(seed, h.Sum64()))
}

// Image renders the master itself, dark ridges on white, for review.
func (m *Master) Image() *image.Gray {
	g := newGrid()
	for i, v := range m.ridges {
		g[i] = 1
		if m.mask[i] {
			g[i] = (1 - v) / 2
		}
	}
	return toImage(g)
}
