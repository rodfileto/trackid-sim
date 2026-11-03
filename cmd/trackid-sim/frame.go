package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/rodfileto/trackid-sim/media"
	"github.com/rodfileto/trackid-sim/world"
)

// cmdFrame renders one evidence -- a facial evidence's CCTV frame, or a
// fingerprint evidence's lift card -- plus a copy with each trace's box drawn on
// it for checking by eye. load renders the same image through the same media
// call.
func cmdFrame(args []string) {
	fs := flag.NewFlagSet("frame", flag.ExitOnError)
	worldPath := fs.String("world", "out/world.json", "world file")
	caseID := fs.String("case", "", "case id, facial or fingerprint (default: the first facial case)")
	evSeq := fs.Int("evidence", 1, "evidence sequence within the case")
	level := fs.String("level", "", "override the evidence's recorded capture level: good or medium")
	pick := fs.String("variants", "", "comma-separated pool variant ids to place instead of the evidence's traces (for trials)")
	out := fs.String("out", "out/frames", "output directory")
	fs.Parse(args)

	w, err := world.Load(*worldPath)
	if err != nil {
		log.Fatal(err)
	}
	var c *world.Case
	for i := range w.Cases {
		if w.Cases[i].CaseID == *caseID || (*caseID == "" && w.Cases[i].Modality == "FACIAL") {
			c = &w.Cases[i]
			break
		}
	}
	if c == nil {
		log.Fatalf("no case %q", *caseID)
	}
	if *evSeq < 1 || *evSeq > len(c.Evidences) {
		log.Fatalf("case %s has %d evidences", c.CaseID, len(c.Evidences))
	}
	ev := c.Evidences[*evSeq-1]
	humans := map[string]world.Human{}
	for _, h := range w.Humans {
		humans[h.ID] = h
	}
	if c.Modality == "FINGERPRINT" {
		data, boxes, err := media.NewPrints(w.Config.Seed).LiftCard(*c, ev, humans)
		if err != nil {
			log.Fatal(err)
		}
		writeChecked(*out, fmt.Sprintf("%s_e%d_lift", c.CaseID, ev.Sequence), ".png", data, boxes)
		return
	}
	pool, err := media.OpenPool(w)
	if err != nil {
		log.Fatal(err)
	}
	if *level != "" {
		capture, ok := world.CaptureLevels[*level]
		if !ok {
			log.Fatalf("unknown level %q", *level)
		}
		ev.Capture = &capture
	}

	ids := make([]string, len(ev.Traces))
	for i, t := range ev.Traces {
		ids[i] = humans[t.HumanID].Variant
	}
	name := fmt.Sprintf("%s_e%d", c.CaseID, ev.Sequence)
	if *pick != "" {
		ids = strings.Split(*pick, ",")
		name = "pick_" + strings.Join(ids, "+")
	}
	name += "_" + ev.Capture.Level

	var figs []media.Figure
	for _, id := range ids {
		f, err := pool.Figure(id)
		if err != nil {
			log.Fatal(err)
		}
		figs = append(figs, f)
		log.Printf("figure: %s", id)
	}

	o, ok := w.OccurrenceOf(*c)
	if !ok {
		log.Fatalf("case %s: occurrence %s is not in the world", c.CaseID, c.OccurrenceID)
	}
	data, placed, err := media.Evidence(w.Config.Seed, *c, ev, figs, o.OccurredFrom, o.OccurredTo)
	if err != nil {
		log.Fatal(err)
	}
	boxes := make([]media.Box, len(placed))
	for i, p := range placed {
		boxes[i] = p.Box
	}
	writeChecked(*out, name, ".jpg", data, boxes)
}

// writeChecked writes an evidence image as rendered, then a JPEG copy with each
// trace's box drawn on it.
func writeChecked(dir, name, ext string, data []byte, boxes []media.Box) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Fatal(err)
	}
	path := filepath.Join(dir, name+ext)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		log.Fatal(err)
	}
	b, _ := json.Marshal(boxes)
	log.Printf("wrote %s (%d KB), boxes %s", path, len(data)/1024, b)

	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		log.Fatal(err)
	}
	preview := image.NewRGBA(img.Bounds())
	draw.Draw(preview, preview.Bounds(), img, image.Point{}, draw.Src)
	for _, b := range boxes {
		rect(preview, b, color.RGBA{0, 255, 0, 255}, 3)
	}
	pf, err := os.Create(filepath.Join(dir, name+"_boxes.jpg"))
	if err != nil {
		log.Fatal(err)
	}
	defer pf.Close()
	if err := jpeg.Encode(pf, preview, &jpeg.Options{Quality: 90}); err != nil {
		log.Fatal(err)
	}
}

func rect(img *image.RGBA, b media.Box, c color.RGBA, t int) {
	for i := 0; i < t; i++ {
		for x := b.X1; x <= b.X2; x++ {
			img.SetRGBA(x, b.Y1+i, c)
			img.SetRGBA(x, b.Y2-i, c)
		}
		for y := b.Y1; y <= b.Y2; y++ {
			img.SetRGBA(b.X1+i, y, c)
			img.SetRGBA(b.X2-i, y, c)
		}
	}
}
