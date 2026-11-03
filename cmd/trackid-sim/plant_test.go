package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"math/rand/v2"
	"strings"
	"testing"
)

func pairs(genuine, impostor int) []*plantPair {
	var out []*plantPair
	for i := 0; i < genuine; i++ {
		out = append(out, &plantPair{Genuine: true})
	}
	for i := 0; i < impostor; i++ {
		out = append(out, &plantPair{})
	}
	return out
}

func count(ps []*plantPair) (ids, exclusions int) {
	for _, p := range ps {
		switch {
		case p.Wrong && p.Genuine:
			exclusions++
		case p.Wrong:
			ids++
		}
	}
	return ids, exclusions
}

func TestPickWrong(t *testing.T) {
	rng := rand.New(rand.NewPCG(1, 0))
	for _, c := range []struct {
		name                    string
		genuine, impostor, want int
		share                   float64
		ids, exclusions, short  int
	}{
		{"half and half", 10, 10, 6, 0.5, 3, 3, 0},
		{"few impostors: exclusions make up", 10, 1, 6, 0.5, 1, 5, 2},
		{"few genuine: identifications make up", 1, 10, 6, 0.5, 5, 1, 0},
		{"exclusions only", 10, 10, 4, 0, 0, 4, 0},
		{"more than there are", 2, 1, 9, 0.5, 1, 2, 1},
	} {
		ps := pairs(c.genuine, c.impostor)
		ids, short := pickWrong(ps, c.want, c.share, rng)
		gotIDs, gotEx := count(ps)
		if ids != c.ids || gotIDs != c.ids || gotEx != c.exclusions || short != c.short {
			t.Errorf("%s: ids=%d (marked %d) exclusions=%d short=%d; want %d, %d, %d", c.name, ids, gotIDs, gotEx, short, c.ids, c.exclusions, c.short)
		}
	}
}

func TestChooseAnalysesPrefersImpostors(t *testing.T) {
	one := plantAnalysis{AnalysisID: 1, Pairs: []plantPair{{Genuine: true}}}
	two := plantAnalysis{AnalysisID: 2, Pairs: []plantPair{{Genuine: true}, {Genuine: false}}}
	three := plantAnalysis{AnalysisID: 3, Pairs: []plantPair{{Genuine: true}}}
	got := chooseAnalyses([]plantAnalysis{one, two, three}, 2)
	if len(got) != 2 || got[0].AnalysisID != 2 || got[1].AnalysisID != 1 {
		t.Fatalf("chosen = %+v; want 2 (with a different-people pair), then 1", got)
	}
}

func TestThumbnailURI(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 800))
	for i := range src.Pix {
		src.Pix[i] = 200
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, src); err != nil {
		t.Fatal(err)
	}
	uri, err := thumbnailURI(buf.Bytes(), 160)
	if err != nil {
		t.Fatal(err)
	}
	data, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(string(uri), "data:image/jpeg;base64,"))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 80 || b.Dy() != 160 {
		t.Fatalf("thumbnail is %dx%d, want 80x160", b.Dx(), b.Dy())
	}
}
