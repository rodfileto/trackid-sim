package load

import (
	"bufio"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/cases"
)

// The draw cache keeps a world's drawn files (photos, prints, CCTV frames, lift cards) on
// disk, so a burst of the same world again -- the same seed, size and face binding -- skips
// the drawing, which takes longer than the burst itself at the larger stress levels (28 min at
// 10x). It is keyed by a hash of the whole world, so any change to the world draws afresh.
// A change to how media draws (media, fpgen) does not change the key: bump drawCacheVersion,
// or delete the cache directory.
const drawCacheVersion = 1

// DrawCacheKey names a world's entry in the draw cache.
func DrawCacheKey(w *world.World) (string, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	fmt.Fprintf(h, "draw-cache-v%d\n", drawCacheVersion)
	h.Write(b)
	return hex.EncodeToString(h.Sum(nil))[:24], nil
}

// The cache file is a gob stream: a header, then each enrollment's files and each case's
// evidence in world order.
type drawHeader struct {
	Version, Enrollments, Cases int
}

type drawnFile struct {
	Data []byte
	Name string
}

type drawnEvidence struct {
	Data  []byte
	Name  string
	Boxes []*cases.Box
}

// drawWorld draws every enrollment and case of w, or reads them from the cache in dir.
func drawWorld(m *Media, w *world.World, humans map[string]world.Human, dir string, logf func(string, ...any)) ([]enrollmentMaterial, []caseMaterial, bool, error) {
	var path string
	if m != nil && dir != "" {
		key, err := DrawCacheKey(w)
		if err != nil {
			return nil, nil, false, err
		}
		path = filepath.Join(dir, key+".gob")
		t := time.Now()
		if e, c, err := readDrawn(path, w); err == nil {
			if logf != nil {
				logf("  burst: read the drawn files from %s in %s", path, time.Since(t).Round(time.Millisecond))
			}
			return e, c, true, nil
		} else if !os.IsNotExist(err) && logf != nil {
			logf("  burst: draw cache %s unusable, drawing again: %v", path, err)
		}
	}

	if m != nil {
		m.Prints.Warm(PrintedHumans(w))
	}
	enrollments := make([]enrollmentMaterial, len(w.Enrollments))
	for i, e := range w.Enrollments {
		in, err := prepareEnrollment(m, humans, e)
		if err != nil {
			return nil, nil, false, err
		}
		enrollments[i] = in
	}
	caseMats := make([]caseMaterial, len(w.Cases))
	for i, c := range w.Cases {
		in, err := prepareCase(m, w, humans, c)
		if err != nil {
			return nil, nil, false, err
		}
		caseMats[i] = in
	}
	if path != "" {
		if err := writeDrawn(path, enrollments, caseMats); err != nil && logf != nil {
			logf("  burst: could not keep the drawn files in %s: %v", path, err)
		}
	}
	return enrollments, caseMats, false, nil
}

func writeDrawn(path string, enrollments []enrollmentMaterial, caseMats []caseMaterial) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer os.Remove(tmp) // a no-op once renamed
	bw := bufio.NewWriterSize(f, 1<<20)
	enc := gob.NewEncoder(bw)
	if err := enc.Encode(drawHeader{drawCacheVersion, len(enrollments), len(caseMats)}); err != nil {
		f.Close()
		return err
	}
	for _, in := range enrollments {
		files := make([]drawnFile, len(in.files))
		for i, r := range in.files {
			files[i] = drawnFile{r.data, r.name}
		}
		if err := enc.Encode(files); err != nil {
			f.Close()
			return err
		}
	}
	for _, in := range caseMats {
		evs := make([]drawnEvidence, len(in.evidences))
		for i, r := range in.evidences {
			evs[i] = drawnEvidence{r.data, r.name, r.boxes}
		}
		if err := enc.Encode(evs); err != nil {
			f.Close()
			return err
		}
	}
	if err := bw.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func readDrawn(path string, w *world.World) ([]enrollmentMaterial, []caseMaterial, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	dec := gob.NewDecoder(bufio.NewReaderSize(f, 1<<20))
	var h drawHeader
	if err := dec.Decode(&h); err != nil {
		return nil, nil, err
	}
	if h.Version != drawCacheVersion || h.Enrollments != len(w.Enrollments) || h.Cases != len(w.Cases) {
		return nil, nil, fmt.Errorf("cache holds version %d, %d enrollments, %d cases; want version %d, %d, %d",
			h.Version, h.Enrollments, h.Cases, drawCacheVersion, len(w.Enrollments), len(w.Cases))
	}
	enrollments := make([]enrollmentMaterial, len(w.Enrollments))
	for i, e := range w.Enrollments {
		var files []drawnFile
		if err := dec.Decode(&files); err != nil {
			return nil, nil, err
		}
		if len(files) != len(e.Files) {
			return nil, nil, fmt.Errorf("enrollment %s: %d files cached, %d in the world", e.RegisterNumber, len(files), len(e.Files))
		}
		in := enrollmentMaterial{e: e, files: make([]rendered, len(files))}
		for k, d := range files {
			in.files[k] = rendered{d.Data, d.Name}
		}
		enrollments[i] = in
	}
	caseMats := make([]caseMaterial, len(w.Cases))
	for i, c := range w.Cases {
		var evs []drawnEvidence
		if err := dec.Decode(&evs); err != nil {
			return nil, nil, err
		}
		if len(evs) != len(c.Evidences) {
			return nil, nil, fmt.Errorf("case %s: %d evidences cached, %d in the world", c.CaseID, len(evs), len(c.Evidences))
		}
		in := caseMaterial{c: c, evidences: make([]renderedEvidence, len(evs))}
		for k, d := range evs {
			in.evidences[k] = renderedEvidence{rendered{d.Data, d.Name}, d.Boxes}
		}
		caseMats[i] = in
	}
	return enrollments, caseMats, nil
}
