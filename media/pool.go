package media

import (
	"fmt"
	"hash/fnv"
	"image"
	_ "image/png"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/rodfileto/trackid-sim/world"
)

// Pool reads the face pool a world was bound to. The images live next to the
// index (tools/facegen/spike_out/, gitignored), so rendering works only where
// they were generated -- see ROADMAP block E.
type Pool struct {
	dir      string
	faces    map[string]world.PoolFace
	variants map[string]world.PoolVariant

	mu      sync.Mutex
	decoded map[string]image.Image
}

// OpenPool opens the pool index w was generated against.
func OpenPool(w *world.World) (*Pool, error) {
	if w.FacePool == nil {
		return nil, fmt.Errorf("media: world has no face pool bound; generate it with -pool")
	}
	idx, ref, err := world.LoadFacePool(w.FacePool.Path)
	if err != nil {
		return nil, err
	}
	if ref.SHA256 != w.FacePool.SHA256 {
		return nil, fmt.Errorf("media: %s changed since the world was generated; regenerate the world", w.FacePool.Path)
	}
	p := &Pool{
		dir:      filepath.Dir(w.FacePool.Path),
		faces:    map[string]world.PoolFace{},
		variants: map[string]world.PoolVariant{},
		decoded:  map[string]image.Image{},
	}
	for _, f := range idx.Faces {
		p.faces[f.ID] = f
		if f.Variant != nil {
			p.variants[f.Variant.ID] = *f.Variant
		}
	}
	return p, nil
}

// Photo returns a base face's file as it is, for an enrollment photo, and the
// path it was read from.
func (p *Pool) Photo(faceID string) ([]byte, string, error) {
	f, ok := p.faces[faceID]
	if !ok {
		return nil, "", fmt.Errorf("media: no pool face %q", faceID)
	}
	path := filepath.Join(p.dir, f.File)
	data, err := os.ReadFile(path)
	return data, path, err
}

// Figure returns a kept variant ready to place in a frame. Decoded images are
// cached: one person can appear in several evidences.
func (p *Pool) Figure(variantID string) (Figure, error) {
	v, ok := p.variants[variantID]
	if !ok {
		return Figure{}, fmt.Errorf("media: no kept variant %q", variantID)
	}
	p.mu.Lock()
	img, ok := p.decoded[variantID]
	p.mu.Unlock()
	if !ok {
		f, err := os.Open(filepath.Join(p.dir, v.File))
		if err != nil {
			return Figure{}, err
		}
		img, _, err = image.Decode(f)
		f.Close()
		if err != nil {
			return Figure{}, fmt.Errorf("media: %s: %w", v.File, err)
		}
		p.mu.Lock()
		p.decoded[variantID] = img
		p.mu.Unlock()
	}
	return Figure{Img: img, Face: v.FaceBox}, nil
}

// Evidence renders one facial evidence frame: figs in trace order, at the
// evidence's recorded capture, with a DVR timestamp inside the occurrence's
// time window (from, to; to may be nil, for a moment: the frame is then within
// five minutes of from), in from's time zone, so a frame never postdates the
// request that sent it. The frame draws from its own stream, keyed by case and
// evidence, so it does not depend on which frames were rendered before it. It
// returns the JPEG and each figure's placement, whose Box is the trace's box.
func Evidence(seed uint64, c world.Case, ev world.Evidence, figs []Figure, from time.Time, to *time.Time) ([]byte, []Placement, error) {
	if ev.Capture == nil {
		return nil, nil, fmt.Errorf("media: case %s evidence %d has no capture", c.CaseID, ev.Sequence)
	}
	h := fnv.New64a()
	fmt.Fprintf(h, "frame:%s:%d", c.CaseID, ev.Sequence)
	r := rand.New(rand.NewPCG(seed, h.Sum64()))

	clean, placed := Scene(figs, *ev.Capture, r)
	cam := 1 + r.IntN(12)
	osd := fmt.Sprintf("CAM %02d  %s", cam, dvrTime(r, from, to).Format("2006-01-02 15:04:05"))
	data, err := Degrade(clean, *ev.Capture, osd, r)
	return data, placed, err
}

// dvrTime is the moment a frame shows: a whole second within [from, to], or within five minutes
// of from when the occurrence has no end.
func dvrTime(r *rand.Rand, from time.Time, to *time.Time) time.Time {
	window := 5 * time.Minute
	if to != nil && to.After(from) {
		window = to.Sub(from)
	}
	return from.Add(time.Duration(r.Int64N(int64(window/time.Second)+1)) * time.Second)
}
