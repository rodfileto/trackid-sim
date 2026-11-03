// Command facescore answers the C0 question: do the generated faces behave like
// faces to the recognizer trackid actually uses?
//
// It embeds every base face and every degraded capture with trackid-vision and
// reports two distributions:
//
//	genuine  -- base_i vs degraded_i: same person, different capture quality
//	impostor -- base_i vs base_j: different people
//
// The spike passes if those separate. It also reports the detection rate per
// quality level, which is the other half of C0: a capture the detector misses
// never reaches the recognizer at all.
//
// This is a separate Go module on purpose. It needs cgo and onnxruntime, and
// the simulator's own build must not inherit that.
//
// Usage:
//
//	go run . -vision ../../../trackid-vision
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/rodfileto/trackid-vision/vision"
)

// embedCache keys embeddings by the sha256 of the image bytes, so a rerun that
// changes only a threshold does not re-embed 800 images for eight minutes.
// Hashing content rather than paths means a regenerated pool invalidates itself
// and a moved or renamed file still hits -- the one thing a cache here must
// never do is serve a stale embedding for changed pixels.
type cacheEntry struct {
	Embedding []float32
	Score     float32
	// Frac is the detected face's area as a fraction of the whole frame. It
	// is what tells a reframed photograph from a re-rendered one, which the
	// embedding cannot: a face embedding is deliberately invariant to
	// framing, clothing and background, so two images that differ in exactly
	// those ways still score ~0.95. Measured: 12 of 20 full-length register
	// shots -- every one a genuine new photograph -- were called
	// near-duplicates by an embedding threshold.
	Frac float64
	// Box is the detected face in original-image pixels (x1,y1,x2,y2). Kept
	// so review sheets can crop the face the detector actually found rather
	// than guessing at where a head sits in a full-length frame -- guessing
	// produced three wrong crops before this was added.
	Box [4]float32
	Err string // a detection miss is a result worth caching too
}

// cacheVersion is bumped whenever cacheEntry's shape changes. gob decodes a
// field that did not exist when an entry was written as its zero value, so
// without this an added field reads back as 0 everywhere and silently
// disables whatever depends on it -- which is exactly what happened when Frac
// was introduced: every cached base face reported a frame share of zero and
// the reframe check passed everything.
const cacheVersion = 3

type cacheFile struct {
	Version int
	Entries map[string]cacheEntry
}

type embedCache struct {
	path         string
	m            map[string]cacheEntry
	hits, misses int
}

func openCache(path string) *embedCache {
	c := &embedCache{path: path, m: map[string]cacheEntry{}}
	f, err := os.Open(path)
	if err != nil {
		return c
	}
	defer f.Close()
	var cf cacheFile
	if err := gob.NewDecoder(f).Decode(&cf); err != nil {
		// A corrupt or stale-format cache is not worth failing over: drop it.
		fmt.Fprintf(os.Stderr, "note: ignoring unreadable cache %s (%v)\n", path, err)
		return c
	}
	if cf.Version != cacheVersion {
		fmt.Fprintf(os.Stderr, "note: cache is v%d, want v%d -- re-embedding\n",
			cf.Version, cacheVersion)
		return c
	}
	c.m = cf.Entries
	return c
}

func (c *embedCache) save() {
	if c.path == "" {
		return
	}
	f, err := os.Create(c.path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "note: cannot write cache: %v\n", err)
		return
	}
	defer f.Close()
	if err := gob.NewEncoder(f).Encode(cacheFile{Version: cacheVersion, Entries: c.m}); err != nil {
		fmt.Fprintf(os.Stderr, "note: cannot encode cache: %v\n", err)
	}
}

func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

type poolManifest struct {
	Model string `json:"model"`
	Faces []struct {
		ID   string `json:"id"`
		File string `json:"file"`
		Sex  string `json:"sex"`
		Age  int    `json:"age"`
	} `json:"faces"`
}

type degradedManifest struct {
	Captures []struct {
		ID    string `json:"id"`
		Level string `json:"level"`
		File  string `json:"file"`
	} `json:"captures"`
}

// variantManifest is what tools/facegen/gen_variants.py writes: a new image of
// an existing pool face, produced by an edit model rather than by degrading the
// base file. Scoring these is the C0 gate -- see the -variants flag.
type variantManifest struct {
	Model    string `json:"model"`
	Weights  string `json:"weights"`
	Variants []struct {
		ID     string `json:"id"`
		BaseID string `json:"base_id"`
		Kind   string `json:"kind"`
		File   string `json:"file"`
	} `json:"variants"`
}

type pair struct {
	A, B  string  `json:"-"`
	Level string  `json:"level,omitempty"`
	Score float64 `json:"score"`
}

// pairList collects repeated -variant id=path flags.
type pairList []string

func (p *pairList) String() string     { return strings.Join(*p, ",") }
func (p *pairList) Set(v string) error { *p = append(*p, v); return nil }

func main() {
	visionDir := flag.String("vision", "../../../trackid-vision", "trackid-vision checkout (for models/ and onnxruntime-gpu/)")
	facegen := flag.String("facegen", "../facegen", "facegen dir holding the manifests")
	outPath := flag.String("out", "scores.json", "where to write the full score dump")
	cachePath := flag.String("cache", "embed_cache.gob",
		"sha256-keyed embedding cache; empty disables it")
	variantManifestPath := flag.String("variants", "",
		"variant manifest from gen_variants.py (default <facegen>/variant_manifest.json if present)")
	dropBelow := flag.Float64("drop-below", 0,
		"identity-check threshold; a variant scoring at or under it is dropped (0 = use the worst impostor pair)")
	dupAbove := flag.Float64("dup-above", 0,
		"near-duplicate threshold; a variant scoring at or over it is dropped as not a new capture (0 = use the same-file quantile)")
	reframeMax := flag.Float64("reframe-max", 0.5,
		"a variant whose face keeps at least this share of its base's frame share never reframed, so the edit did nothing")
	qtl := flag.Float64("quantile", 0.999,
		"quantile setting the default thresholds: impostor floor at q, same-file ceiling at 1-q")
	var variants pairList
	flag.Var(&variants, "variant", "id=path of an edited variant to score against its base face (repeatable)")
	flag.Parse()

	// DetectorYuNet is set explicitly: up to trackid-vision v0.3.0 vision.Config
	// defaulted to DetectorSCRFD, whose InsightFace weights are non-commercial
	// and must never touch the released corpus. See docs/LICENSES.md.
	svc, err := vision.NewService(vision.Config{
		Detector:          vision.DetectorYuNet,
		DetectorPath:      filepath.Join(*visionDir, "models/face_detection_yunet_2026may.onnx"),
		RecognizerPath:    filepath.Join(*visionDir, "models/glintr100.onnx"),
		SharedLibraryPath: filepath.Join(*visionDir, "onnxruntime-gpu/libonnxruntime.so"),
		UseGPU:            false, // CPU: no contention with image generation
	})
	if err != nil {
		fatal("new vision service: %v", err)
	}
	defer svc.Close()

	if *cachePath != "" {
		cache = openCache(*cachePath)
		defer func() {
			cache.save()
			fmt.Printf("embeddings: %d cached, %d computed -> %s\n",
				cache.hits, cache.misses, *cachePath)
		}()
	}

	var pool poolManifest
	readJSON(filepath.Join(*facegen, "pool_manifest.json"), &pool)
	var degraded degradedManifest
	if err := tryReadJSON(filepath.Join(*facegen, "degraded_manifest.json"), &degraded); err != nil {
		fmt.Fprintf(os.Stderr, "note: no degraded captures (%v); impostor scores only\n", err)
	}

	base := map[string][]float32{}
	baseFrac := map[string]float64{} // face area / frame area, for the reframe check
	for _, f := range pool.Faces {
		emb, _, frac, err := embed(svc, filepath.Join(*facegen, f.File))
		if err != nil {
			fmt.Fprintf(os.Stderr, "base %s: %v\n", f.ID, err)
			continue
		}
		base[f.ID] = emb
		baseFrac[f.ID] = frac
	}
	fmt.Printf("base faces embedded: %d/%d\n", len(base), len(pool.Faces))

	// Degraded captures: a miss here is a result, not an error.
	type capKey struct{ id, level string }
	caps := map[capKey][]float32{}
	detected := map[string][2]int{} // level -> {hits, total}
	for _, c := range degraded.Captures {
		st := detected[c.Level]
		st[1]++
		emb, _, _, err := embed(svc, filepath.Join(*facegen, c.File))
		if err == nil {
			caps[capKey{c.ID, c.Level}] = emb
			st[0]++
		}
		detected[c.Level] = st
	}

	var genuine, impostor []pair
	for k, emb := range caps {
		if b, ok := base[k.id]; ok {
			genuine = append(genuine, pair{A: k.id, B: k.id, Level: k.level, Score: cosine(b, emb)})
		}
	}
	ids := make([]string, 0, len(base))
	for id := range base {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	// Impostor pairs must be built the same way as the genuine pairs they are
	// compared against, or the comparison measures domain, not identity. Two
	// clean portraits share lighting, framing and background and so score high
	// for reasons unrelated to who they show; a clean-vs-degraded pair loses
	// similarity for the same irrelevant reasons. So each level gets its own
	// cross-domain impostor baseline, base_i vs degraded_j for i != j.
	for i := 0; i < len(ids); i++ {
		for j := i + 1; j < len(ids); j++ {
			impostor = append(impostor, pair{A: ids[i], B: ids[j], Level: "clean",
				Score: cosine(base[ids[i]], base[ids[j]])})
		}
	}
	for k, emb := range caps {
		for _, id := range ids {
			if id == k.id {
				continue
			}
			impostor = append(impostor, pair{A: id, B: k.id, Level: k.level,
				Score: cosine(base[id], emb)})
		}
	}

	if len(detected) > 0 {
		fmt.Println("\ndetection rate (face found in the 1920x1080 frame):")
		for _, lvl := range []string{"good", "medium", "poor"} {
			if st, ok := detected[lvl]; ok {
				fmt.Printf("  %-7s %3d/%-3d  %.0f%%\n", lvl, st[0], st[1], 100*float64(st[0])/float64(st[1]))
			}
		}
	}

	fmt.Println("\ncosine similarity (impostor baseline is matched to each level):")
	report("impostor (clean/clean)", byLevel(impostor, "clean"))
	for _, lvl := range []string{"good", "medium", "poor"} {
		g, im := byLevel(genuine, lvl), byLevel(impostor, lvl)
		if len(g) == 0 {
			continue
		}
		fmt.Println()
		report("genuine  ("+lvl+")", g)
		report("impostor ("+lvl+")", im)
		if len(im) > 0 {
			mg, sg := meanStd(g)
			mi, si := meanStd(im)
			d := (mg - mi) / math.Sqrt((sg*sg+si*si)/2)
			// A threshold exists separating the two sets only if the worst
			// genuine pair beats the best impostor pair.
			sort.Float64s(g)
			sort.Float64s(im)
			sep := "OVERLAP"
			if g[0] > im[len(im)-1] {
				sep = fmt.Sprintf("clean gap %.3f", g[0]-im[len(im)-1])
			}
			fmt.Printf("  %-26s d' = %.2f   %s\n", "->", d, sep)
		}
	}

	// Variants are the real identity question: a base face against a *new*
	// image of that person, not a degraded copy of the same file. Scored
	// against the clean/clean impostor distribution, which is the matching
	// baseline (both sides are undegraded generator output).
	//
	// The drop rate this reports is C0's last open item. An edit model does
	// not fail loudly: it returns a plausible portrait of *someone*, and only
	// the embedding says whether it is still the same person. So every variant
	// is checked and the ones that changed the person are counted, not
	// silently kept -- an unchecked variant pool would seed the corpus with
	// pairs labelled genuine that are not.
	type variantJob struct{ id, baseID, kind, file string }
	var jobs []variantJob
	vmPath := *variantManifestPath
	if vmPath == "" {
		if d := filepath.Join(*facegen, "variant_manifest.json"); fileExists(d) {
			vmPath = d
		}
	}
	if vmPath != "" {
		var vm variantManifest
		readJSON(vmPath, &vm)
		for _, v := range vm.Variants {
			jobs = append(jobs, variantJob{v.ID, v.BaseID, v.Kind,
				filepath.Join(*facegen, v.File)})
		}
		fmt.Printf("\nvariant manifest: %s (%d variants, %s)\n", vmPath, len(vm.Variants), vm.Weights)
	}
	for _, v := range variants {
		id, path, ok := strings.Cut(v, "=")
		if !ok {
			fmt.Fprintf(os.Stderr, "  bad -variant %q, want id=path\n", v)
			continue
		}
		jobs = append(jobs, variantJob{id, id, "adhoc", path})
	}

	var variantRows []map[string]any
	var variantImpostor []float64
	if len(jobs) > 0 && len(byLevel(impostor, "clean")) == 0 && *dropBelow == 0 {
		// Fewer than two base faces: there is no impostor distribution to set
		// the threshold from, and a drop rate against no baseline is a number
		// with no meaning.
		fatal("%d variants to score but no clean impostor pairs; pass -drop-below", len(jobs))
	}
	if len(jobs) > 0 {
		im := byLevel(impostor, "clean")
		sort.Float64s(im)
		// The default threshold is the worst thing the recognizer already does
		// on a pair it should reject. A variant that scores no better than the
		// best impostor is not distinguishable from a different person, so it
		// cannot be used as a genuine pair whatever it looks like.
		// The floor is a quantile, not the maximum. The maximum of ~20k
		// impostor pairs is one pair, and it climbs every time the pool grows
		// -- going from 30 to 200 faces moved it from +0.659 to +0.763 purely
		// by sampling deeper into the tail, with no change to the generator.
		// A threshold that moves with n is not a threshold. p99.9 still
		// rejects 999 of every 1000 impostor pairs and is stable as the pool
		// grows, which is what an operating point has to be.
		// Impostor pairs must be built the same way as the genuine pairs they
		// are compared against -- the same rule the degraded levels follow a
		// few lines up, and the one the first version of this block broke. A
		// variant is a face inside a full-length frame at different framing
		// and lighting, so scoring it against the clean/clean baseline (two
		// 512 px studio portraits) measures the domain gap, not identity.
		// Measured: a register variant that is plainly the same man scored
		// +0.645, under a clean/clean floor of +0.702, while its own domain's
		// impostor pairs sit far lower.
		//
		// So: base_i vs variant_j for i != j, which is the variant-domain
		// equivalent of the degraded baselines. It needs at least two variants
		// of different people to exist at all.
		vEmb := map[string][]float32{}
		for _, j := range jobs {
			if emb, _, _, err := embed(svc, j.file); err == nil {
				vEmb[j.id] = emb
			}
		}
		for _, j := range jobs {
			emb, ok := vEmb[j.id]
			if !ok {
				continue
			}
			for _, id := range ids {
				if id == j.baseID {
					continue
				}
				variantImpostor = append(variantImpostor, cosine(base[id], emb))
			}
		}
		sort.Float64s(variantImpostor)

		var threshold float64
		var rule string
		if *dropBelow != 0 {
			threshold, rule = *dropBelow, fmt.Sprintf("-drop-below %+.3f", *dropBelow)
		} else if len(variantImpostor) >= 20 {
			threshold = quantile(variantImpostor, *qtl)
			rule = fmt.Sprintf("variant-matched impostor p%.4g %+.3f (n=%d, max %+.3f)",
				*qtl*100, threshold, len(variantImpostor),
				variantImpostor[len(variantImpostor)-1])
		} else {
			// Too few variants to form their own baseline. Fall back, and say
			// so: this floor is measured in the wrong domain and will reject
			// good variants.
			threshold = quantile(im, *qtl)
			rule = fmt.Sprintf("clean/clean impostor p%.4g %+.3f -- WRONG DOMAIN, "+
				"need >=20 variant pairs for a matched baseline (have %d)",
				*qtl*100, threshold, len(variantImpostor))
		}

		// A variant can fail in two opposite directions, and only one of them
		// looks like failure. Too low and the edit changed the person. Too
		// high and the edit did nothing -- the model re-rendered its input,
		// which scores beautifully and is worthless as a second capture.
		//
		// But the second test cannot be done on the embedding. A face
		// embedding is built to ignore framing, clothing and background, so a
		// genuine new photograph of the same person under similar lighting
		// scores ~0.95 exactly as a re-render would. Measured on 20 register
		// variants: 12 were called near-duplicates at +0.889..+0.947 and every
		// one of them was a real full-length shot in different clothes.
		//
		// So the reframe test is structural: compare the detected face's share
		// of the frame against its base face's share. A passport crop gives the
		// face a large share; a full-length register shot gives it a small one.
		// If the share barely moved, the model returned the same framing and
		// the edit did nothing -- whatever the cosine says.
		reframe := *reframeMax
		ceilRule := fmt.Sprintf("face keeps >= %.0f%% of its base's frame share "+
			"(i.e. never reframed)", reframe*100)
		if *dupAbove != 0 {
			ceilRule = fmt.Sprintf("cosine >= %+.3f (-dup-above)", *dupAbove)
		}

		fmt.Printf("\nvariants (is this a new photograph of the same person?)\n")
		if len(variantImpostor) > 0 {
			m, sd := meanStd(variantImpostor)
			fmt.Printf("  variant-domain impostor: n=%d mean %+.3f sd %.3f max %+.3f\n",
				len(variantImpostor), m, sd, variantImpostor[len(variantImpostor)-1])
		}
		fmt.Printf("  identity lost:  score <= %s\n", rule)
		fmt.Printf("  near-duplicate: score >= %s\n", ceilRule)

		type tally struct {
			kept, dropped, undetected, dup int
			scores                         []float64
		}
		byKind := map[string]*tally{}
		order := []string{}
		for _, j := range jobs {
			t, ok := byKind[j.kind]
			if !ok {
				t = &tally{}
				byKind[j.kind] = t
				order = append(order, j.kind)
			}
			b, known := base[j.baseID]
			if !known {
				fmt.Fprintf(os.Stderr, "  %s: no base face %q in the pool\n", j.id, j.baseID)
				continue
			}
			emb, det, frac, err := embed(svc, j.file)
			if err != nil {
				// A variant the detector misses is a drop too: it never
				// reaches the recognizer, so it cannot enter the corpus.
				t.undetected++
				t.dropped++
				fmt.Printf("  DROP %-18s %v\n", j.id, err)
				variantRows = append(variantRows, map[string]any{
					"id": j.id, "base_id": j.baseID, "kind": j.kind, "file": j.file,
					"error": err.Error(), "kept": false})
				continue
			}
			sc := cosine(b, emb)
			t.scores = append(t.scores, sc)
			// How much of its base face's frame share this face kept. Near 1
			// means the framing did not change.
			share := math.NaN()
			if bf := baseFrac[j.baseID]; bf > 0 {
				share = frac / bf
			}
			isDup := false
			if *dupAbove != 0 {
				isDup = sc >= *dupAbove
			} else if !math.IsNaN(share) {
				isDup = share >= reframe
			}
			kept := sc > threshold && !isDup
			switch {
			case kept:
				t.kept++
			case sc <= threshold:
				t.dropped++
				fmt.Printf("  DROP %-18s cosine %+.3f  det %.2f  (identity not preserved)\n",
					j.id, sc, det)
			default:
				t.dropped++
				t.dup++
				fmt.Printf("  DROP %-18s cosine %+.3f  share %.2fx  (no reframe: the edit did nothing)\n",
					j.id, sc, share)
			}
			pct := math.NaN()
			if len(im) > 0 {
				pct = float64(sort.SearchFloat64s(im, sc)) / float64(len(im)) * 100
			}
			variantRows = append(variantRows, map[string]any{
				"id": j.id, "base_id": j.baseID, "kind": j.kind, "file": j.file,
				"score": sc, "percentile": nullable(pct), "detector_score": det, "kept": kept,
				"face_frac": frac, "face_box": lastBox, "frame_share": nullable(share),
				"near_duplicate": isDup})
		}

		var kept, dropped, undetected, dup int
		fmt.Printf("\n  %-10s %5s %5s %8s %8s %8s\n", "kind", "kept", "drop", "mean", "min", "max")
		for _, k := range order {
			t := byKind[k]
			n := t.kept + t.dropped
			kept, dropped, undetected, dup = kept+t.kept, dropped+t.dropped, undetected+t.undetected, dup+t.dup
			m, _ := meanStd(t.scores)
			sort.Float64s(t.scores)
			lo, hi := math.NaN(), math.NaN()
			if len(t.scores) > 0 {
				lo, hi = t.scores[0], t.scores[len(t.scores)-1]
			}
			fmt.Printf("  %-10s %5d %5d %+8.3f %+8.3f %+8.3f   %.0f%% dropped\n",
				k, t.kept, t.dropped, m, lo, hi, 100*float64(t.dropped)/float64(n))
		}
		total := kept + dropped
		fmt.Printf("\n  drop rate: %d/%d = %.1f%% (%d undetected, %d identity, %d near-duplicate)\n",
			dropped, total, 100*float64(dropped)/float64(total), undetected,
			dropped-undetected-dup, dup)
	}

	writeJSON(*outPath, map[string]any{
		"model": pool.Model, "genuine": genuine, "impostor": impostor, "detected": detected,
		"variants": variantRows, "variant_manifest": vmPath, "drop_below": *dropBelow,
		"dup_above": *dupAbove, "quantile": *qtl,
		"variant_impostor": variantImpostor,
	})
	fmt.Printf("\nfull scores -> %s\n", *outPath)
}

// nullable renders NaN as JSON null rather than failing the whole dump.
func nullable(v float64) any {
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return nil
	}
	return v
}

// quantile returns the p-th quantile of an already-sorted slice, nearest-rank.
func quantile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return math.NaN()
	}
	i := int(math.Round(p * float64(len(sorted)-1)))
	if i < 0 {
		i = 0
	}
	if i >= len(sorted) {
		i = len(sorted) - 1
	}
	return sorted[i]
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// embed returns the highest-scoring face in the image, via the cache.
func embed(svc *vision.Service, path string) ([]float32, float32, float64, error) {
	if cache != nil {
		if h, err := fileHash(path); err == nil {
			if e, ok := cache.m[h]; ok {
				cache.hits++
				if e.Err != "" {
					return nil, 0, 0, fmt.Errorf("%s", e.Err)
				}
				lastBox = e.Box
				return e.Embedding, e.Score, e.Frac, nil
			}
			cache.misses++
			emb, sc, frac, err := embedUncached(svc, path)
			msg := ""
			if err != nil {
				msg = err.Error()
			}
			cache.m[h] = cacheEntry{Embedding: emb, Score: sc, Frac: frac, Box: lastBox, Err: msg}
			return emb, sc, frac, err
		}
	}
	return embedUncached(svc, path)
}

// cache is process-global: embed is called from several places and threading a
// parameter through each of them would be noise for a single-run tool. lastBox
// carries the most recent detection's box out of embed for the same reason.
var cache *embedCache
var lastBox [4]float32

func embedUncached(svc *vision.Service, path string) ([]float32, float32, float64, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, 0, err
	}
	// Read once, use twice: the frame's dimensions are needed alongside the
	// detection to turn a box into a fraction of the image.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, err
	}
	faces, err := svc.DetectAndEmbed(bytes.NewReader(raw))
	if err != nil {
		return nil, 0, 0, err
	}
	if len(faces) == 0 {
		return nil, 0, 0, fmt.Errorf("no face detected")
	}
	best := faces[0]
	for _, c := range faces[1:] {
		if c.Score > best.Score {
			best = c
		}
	}
	w := float64(best.Box[2] - best.Box[0])
	h := float64(best.Box[3] - best.Box[1])
	frac := (w * h) / float64(cfg.Width*cfg.Height)
	lastBox = best.Box
	return best.Embedding, best.Score, frac, nil
}

// cosine is a plain dot product: trackid-vision returns L2-normalized embeddings.
func cosine(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// byLevel pulls out the scores of one quality level.
func byLevel(ps []pair, level string) []float64 {
	var out []float64
	for _, p := range ps {
		if p.Level == level {
			out = append(out, p.Score)
		}
	}
	return out
}

func scores(ps []pair) []float64 {
	out := make([]float64, len(ps))
	for i, p := range ps {
		out[i] = p.Score
	}
	return out
}

func meanStd(v []float64) (float64, float64) {
	var sum float64
	for _, x := range v {
		sum += x
	}
	m := sum / float64(len(v))
	var ss float64
	for _, x := range v {
		ss += (x - m) * (x - m)
	}
	return m, math.Sqrt(ss / float64(len(v)))
}

func report(label string, v []float64) {
	if len(v) == 0 {
		return
	}
	sort.Float64s(v)
	m, sd := meanStd(v)
	fmt.Printf("  %-26s n=%-4d mean %+.3f sd %.3f  min %+.3f  max %+.3f\n",
		label, len(v), m, sd, v[0], v[len(v)-1])
}

func readJSON(path string, v any) {
	if err := tryReadJSON(path, v); err != nil {
		fatal("%v", err)
	}
}

func tryReadJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) {
	b, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		fatal("marshal: %v", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		fatal("write %s: %v", path, err)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
