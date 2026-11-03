package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/png"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/rodfileto/trackid-sim/fpgen"
)

// cmdFingerprints writes fingers x (rolled, latent) impressions as 500 dpi
// PNGs with the manifest tools/fpgen/gen_impressions.py writes, so
// tools/fpscore scores the Go generator exactly as it scored the prototype.
func cmdFingerprints(args []string) {
	fs := flag.NewFlagSet("fingerprints", flag.ExitOnError)
	fingers := fs.Int("fingers", 100, "number of fingers (master prints)")
	nRolled := fs.Int("rolled", 4, "rolled impressions per finger")
	nLatent := fs.Int("latents", 2, "latents per finger")
	seed := fs.Uint64("seed", 1, "random seed")
	out := fs.String("out", "tools/fpgen/out/go_impressions", "output directory")
	sheet := fs.Bool("sheet", false, "also write fNNN_sheet.png: master, first rolled, first latent side by side")
	fs.Parse(args)
	if err := os.MkdirAll(*out, 0o755); err != nil {
		log.Fatal(err)
	}

	type latent struct {
		File string  `json:"file"`
		Area float64 `json:"area"`
	}
	type row struct {
		Finger  int      `json:"finger"`
		Class   string   `json:"class"`
		Seconds float64  `json:"seconds"`
		Rolled  []string `json:"rolled"`
		Latent  []latent `json:"latent"`
	}
	rows := make([]row, *fingers)
	write := func(name string, img image.Image) {
		f, err := os.Create(filepath.Join(*out, name))
		if err != nil {
			log.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			log.Fatal(err)
		}
	}

	start := time.Now()
	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	for i := range *fingers {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			t := time.Now()
			m := fpgen.NewMaster(fpgen.Stream(*seed, fmt.Sprintf("finger:%d", i)))
			rw := row{Finger: i, Class: m.Class}
			var strip []image.Image
			if *sheet {
				strip = append(strip, m.Image())
			}
			for k := range *nRolled {
				img := m.Rolled(fpgen.Stream(*seed, fmt.Sprintf("rolled:%d:%d", i, k)))
				name := fmt.Sprintf("f%03d_r%d.png", i, k)
				write(name, img)
				rw.Rolled = append(rw.Rolled, name)
				if k == 0 && *sheet {
					strip = append(strip, img)
				}
			}
			for k := range *nLatent {
				img, area := m.Latent(fpgen.Stream(*seed, fmt.Sprintf("latent:%d:%d", i, k)))
				name := fmt.Sprintf("f%03d_l%d.png", i, k)
				write(name, img)
				rw.Latent = append(rw.Latent, latent{name, float64(int(area*1000+0.5)) / 1000})
				if k == 0 && *sheet {
					strip = append(strip, img)
				}
			}
			if *sheet {
				write(fmt.Sprintf("f%03d_sheet.png", i), hstack(strip))
			}
			rw.Seconds = time.Since(t).Seconds()
			rows[i] = rw
			log.Printf("finger %d: %s, %.1fs", i, m.Class, rw.Seconds)
		}()
	}
	wg.Wait()

	b, _ := json.MarshalIndent(map[string]any{"dpi": 500, "seed": *seed, "generator": "trackid-sim fpgen (Go)", "fingers": rows}, "", " ")
	if err := os.WriteFile(filepath.Join(*out, "manifest.json"), b, 0o644); err != nil {
		log.Fatal(err)
	}
	classes := map[string]int{}
	for _, r := range rows {
		classes[r.Class]++
	}
	log.Printf("%d fingers, %d rolled, %d latents -> %s in %s %v", *fingers, *fingers**nRolled, *fingers**nLatent, *out, time.Since(start).Round(time.Second), classes)
}

// hstack places grayscale images side by side with a white gap.
func hstack(imgs []image.Image) image.Image {
	const gap = 8
	w, h := 0, 0
	for _, im := range imgs {
		w += im.Bounds().Dx() + gap
		h = max(h, im.Bounds().Dy())
	}
	out := image.NewGray(image.Rect(0, 0, w-gap, h))
	for i := range out.Pix {
		out.Pix[i] = 255
	}
	x := 0
	for _, im := range imgs {
		b := im.Bounds()
		for y := 0; y < b.Dy(); y++ {
			for xx := 0; xx < b.Dx(); xx++ {
				out.Set(x+xx, y, im.At(b.Min.X+xx, b.Min.Y+y))
			}
		}
		x += b.Dx() + gap
	}
	return out
}
