package fpgen

import (
	"image"
	"math"
	"math/rand/v2"
)

// Rolled distortion, wider than sketch_one.py's defaults as in
// gen_impressions.py: impressions that differ too little would make the
// genuine scores flatter the generator. tools/fpscore's table was measured
// with these.
const (
	rolledRot   = 15 // degrees, either way
	rolledShift = 12 // px, standard deviation
	rolledWarp  = 8  // px, elastic amplitude
)

// warp returns sampling coordinates for an elastic skin distortion plus a
// rigid motion, so a print and its outline share one warp.
func warp(r *rand.Rand, amp, rotDeg, shiftX, shiftY, smooth float64) (sx, sy grid) {
	dx, dy := gaussian(normal(r), smooth), gaussian(normal(r), smooth)
	kx, ky := amp/(absMax(dx)+1e-9), amp/(absMax(dy)+1e-9)
	a := rotDeg * math.Pi / 180
	sx, sy = newGrid(), newGrid()
	each(func(x, y, i int) {
		xc, yc := float64(x)-W/2, float64(y)-H/2
		sx[i] = math.Cos(a)*xc - math.Sin(a)*yc + W/2 + dx[i]*kx - shiftX
		sy[i] = math.Sin(a)*xc + math.Cos(a)*yc + H/2 + dy[i]*ky - shiftY
	})
	return sx, sy
}

// resample reads the master's ridges (cubic, -1 outside) and outline
// (bilinear) through a warp.
func (m *Master) resample(sx, sy grid) (ridges, outline grid) {
	mk := newGrid()
	for i, v := range m.mask {
		if v {
			mk[i] = 1
		}
	}
	ridges, outline = newGrid(), newGrid()
	rows(func(y0, y1 int) {
		for i := y0 * W; i < y1*W; i++ {
			ridges[i] = sampleCubic(m.ridges, sx[i], sy[i], -1, false)
			outline[i] = sampleLinear(mk, sx[i], sy[i])
		}
	})
	return ridges, outline
}

// creases draws white lines where skin folds: gently curved, mostly across
// the finger.
func creases(r *rand.Rand, n int, wlo, whi float64) grid {
	out := newGrid()
	for k := 0; k < n; k++ {
		cx, cy := uniform(r, 0.2, 0.8)*W, uniform(r, 0.35, 0.95)*H
		t := r.NormFloat64() * 0.45
		length, bend, w := uniform(r, 40, 160), r.NormFloat64()*0.004, uniform(r, wlo, whi)
		each(func(x, y, i int) {
			u := (float64(x)-cx)*math.Cos(t) + (float64(y)-cy)*math.Sin(t)
			v := -(float64(x)-cx)*math.Sin(t) + (float64(y)-cy)*math.Cos(t) - bend*u*u
			along := clamp(1-math.Pow(2*u/length, 2), 0, 1)
			out[i] = math.Max(out[i], math.Exp(-0.5*(v/w)*(v/w))*along)
		})
	}
	return out
}

// Rolled takes an ink-on-card impression, dark ridges on white. Pressure sets
// ridge width; dry patches break ridges; over-inked patches merge them; ridge
// edges are rough; pores show as light dots on ridges; creases cross them.
func (m *Master) Rolled(r *rand.Rand) *image.Gray {
	rot := uniform(r, -rolledRot, rolledRot)
	shx, shy := r.NormFloat64()*rolledShift, r.NormFloat64()*rolledShift
	ridges, outline := m.resample(warp(r, rolledWarp, rot, shx, shy, 60))

	level0 := uniform(r, -0.2, 0.2)
	level := smoothNoise(r, 35)
	dry := smoothNoise(r, 6)
	blot := smoothNoise(r, 15)
	rough := smoothNoise(r, 1.2)
	ink := newGrid()
	pore := newGrid()
	for i := range ink {
		lv := level0 + 0.25*level[i]
		d := math.Max(-dry[i]-1, 0)
		b := math.Max(blot[i]-2, 0)
		ink[i] = 1 / (1 + math.Exp(-(ridges[i]+lv+0.12*rough[i]-0.5*d+1.2*b)*5))
		if r.Float64() < 0.008 && ridges[i] > 0.8 {
			pore[i] = 1
		}
	}
	// Pores: sparse light dots on ridge centres.
	pores := gaussian(pore, 1)
	cr := creases(r, 2+r.IntN(5), 1.5, 3.5)
	contact := gaussian(outline, 6)
	texture := smoothNoise(r, 3)
	gray := newGrid()
	for i := range ink {
		ink[i] *= 1 - clamp(pores[i]*6, 0, 0.7)
		ink[i] *= 1 - cr[i]
		c := contact[i] * (0.85 + 0.15*clamp(texture[i], -1, 1))
		gray[i] = 1 - 0.88*ink[i]*clamp(c, 0, 1)
	}
	gray = gaussian(gray, 0.6)
	for i := range gray {
		gray[i] += 0.025 * r.NormFloat64()
	}
	return toImage(gray)
}

// Latent takes a partial, smudged, low-contrast mark, powdered on a light
// textured surface, and returns it with the deposit's share of the finger's
// area. Crude: latent realism is a stated limitation, not a claim.
func (m *Master) Latent(r *rand.Rand) (*image.Gray, float64) {
	rot := uniform(r, -25, 25)
	shx, shy := r.NormFloat64()*10, r.NormFloat64()*10
	ridges, outline := m.resample(warp(r, 9, rot, shx, shy, 45))
	mk := make(mask, H*W)
	for i, v := range outline {
		mk[i] = v > 0.5
	}

	// Deposit: an irregular blob (an ellipse perturbed by noise) well inside
	// the finger. The finger can be warped far enough that nothing is 90 px
	// inside it; then the centre is drawn from its deepest pixels.
	depth := cityBlock(func(i int) bool { return mk[i] }, true)
	deepest := 0
	for _, d := range depth {
		deepest = max(deepest, d)
	}
	need := min(90, deepest-1)
	var centres []int
	for i, d := range depth {
		if d > need {
			centres = append(centres, i)
		}
	}
	c := centres[r.IntN(len(centres))]
	cx, cy := float64(c%W), float64(c/W)
	a, b, t := uniform(r, 95, 130), uniform(r, 70, 100), uniform(r, 0, math.Pi)
	edge := smoothNoise(r, 18)
	region := make(mask, H*W)
	each(func(x, y, i int) {
		u := ((float64(x)-cx)*math.Cos(t) + (float64(y)-cy)*math.Sin(t)) / a
		v := (-(float64(x)-cx)*math.Sin(t) + (float64(y)-cy)*math.Cos(t)) / b
		region[i] = 1-math.Hypot(u, v)+0.35*edge[i] > 0 && mk[i]
	})
	region = largest(dilate(erode(region, 3), 3))

	rf := newGrid()
	area, fingerArea := 0, 0
	for i, v := range region {
		if v {
			rf[i] = 1
			area++
		}
		if m.mask[i] {
			fingerArea++
		}
	}
	blob := gaussian(rf, 4)
	pressure := smoothNoise(r, 10) // uneven pressure: ridges drop out
	residue := newGrid()
	for i := range residue {
		residue[i] = clamp(ridges[i], 0, 1) * blob[i] * clamp(0.75+0.35*pressure[i], 0, 1)
	}
	smear := boxFilter1D(residue, int(uniform(r, 6, 14)), r.IntN(2))
	part := smoothNoise(r, 25)
	for i := range residue {
		if part[i] > 0.6 {
			residue[i] = smear[i]
		}
	}

	// Surface: fine grain dominates, faint larger mottling, a few scratches.
	g1, g2, g3 := smoothNoise(r, 1.5), smoothNoise(r, 6), smoothNoise(r, 40)
	scratches := creases(r, r.IntN(4), 0.6, 1.2)
	dep := gaussian(residue, 1.2)
	gray := newGrid()
	for i := range gray {
		surface := 0.80 + 0.035*g1[i] + 0.02*g2[i] + 0.02*g3[i] - 0.10*scratches[i]
		powder := 0.0
		if r.Float64() < 0.02 { // stray powder specks
			powder = 0.06 * r.Float64()
		}
		gray[i] = surface - powder - 0.36*dep[i] + 0.03*r.NormFloat64()
	}
	return toImage(gray), float64(area) / float64(fingerArea)
}

// toImage writes a 0..1 field (0 black) as an 8-bit grayscale image.
func toImage(g grid) *image.Gray {
	img := image.NewGray(image.Rect(0, 0, W, H))
	for i, v := range g {
		img.Pix[i] = uint8(math.Round(clamp(v, 0, 1) * 255))
	}
	return img
}
