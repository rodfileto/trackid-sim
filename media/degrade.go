package media

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"math/rand/v2"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/math/fixed"

	"github.com/rodfileto/trackid-sim/world"
)

// Degrade runs the capture chain on a clean frame, in the order degrade.py
// uses: blur (optics), noise (sensor gain), then JPEG (encoder), last. The
// on-screen timestamp is burned in before the blur, as a DVR does. It returns
// the encoded JPEG.
func Degrade(img *image.RGBA, lvl world.Capture, osd string, r *rand.Rand) ([]byte, error) {
	if osd != "" {
		overlay(img, osd)
	}
	if lvl.Blur > 0 {
		img = gaussian(img, lvl.Blur)
	}
	if lvl.Noise > 0 {
		pix := img.Pix
		for i := 0; i < len(pix); i += 4 {
			for c := 0; c < 3; c++ {
				pix[i+c] = clamp8(float64(pix[i+c]) + r.NormFloat64()*lvl.Noise)
			}
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: lvl.JPEG}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// overlay burns text into the top-left corner in a DVR's blocky white-on-shadow
// font: basicfont's 7x13 drawn at 1x, then scaled up 3x without smoothing.
func overlay(img *image.RGBA, text string) {
	const scale = 3
	w := font.MeasureString(basicfont.Face7x13, text).Ceil() + 2
	small := image.NewRGBA(image.Rect(0, 0, w, 15))
	for _, d := range []struct {
		off int
		c   color.Color
	}{{1, color.RGBA{0, 0, 0, 255}}, {0, color.RGBA{235, 235, 235, 255}}} {
		(&font.Drawer{Dst: small, Src: image.NewUniform(d.c), Face: basicfont.Face7x13,
			Dot: fixed.P(d.off, 11+d.off)}).DrawString(text)
	}
	at := image.Rect(24, 20, 24+w*scale, 20+15*scale)
	draw.NearestNeighbor.Scale(img, at, small, small.Bounds(), draw.Over, nil)
}

// gaussian blurs separably with a kernel out to 3 sigma.
func gaussian(src *image.RGBA, sigma float64) *image.RGBA {
	rad := int(math.Ceil(3 * sigma))
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
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	pass := func(in *image.RGBA, dx, dy int) *image.RGBA {
		out := image.NewRGBA(b)
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				var acc [3]float64
				for i, kv := range k {
					xx := min(max(x+(i-rad)*dx, 0), w-1)
					yy := min(max(y+(i-rad)*dy, 0), h-1)
					o := in.PixOffset(xx, yy)
					acc[0] += kv * float64(in.Pix[o])
					acc[1] += kv * float64(in.Pix[o+1])
					acc[2] += kv * float64(in.Pix[o+2])
				}
				set(out, x, y, acc)
			}
		}
		return out
	}
	return pass(pass(src, 1, 0), 0, 1)
}
