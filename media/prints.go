package media

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"runtime"
	"sync"

	"github.com/rodfileto/trackid-sim/fpgen"
	"github.com/rodfileto/trackid-sim/world"
)

// Prints renders fingerprints for the world's humans. Each human has one
// finger, the same one on every record: a master drawn from (seed, human id),
// of which every enrollment's NIST file is a new rolled impression and every
// lift a new latent. One finger rather than ten keeps trackid's exhaustive
// template matching to about a minute per load.
//
// Masters take a couple of seconds each and a human can appear on several
// records, so they are cached; Warm builds a known set in parallel first.
type Prints struct {
	seed uint64

	mu      sync.Mutex
	masters map[string]*fpgen.Master
}

func NewPrints(seed uint64) *Prints {
	return &Prints{seed: seed, masters: map[string]*fpgen.Master{}}
}

func (p *Prints) master(humanID string) *fpgen.Master {
	p.mu.Lock()
	m, ok := p.masters[humanID]
	p.mu.Unlock()
	if ok {
		return m
	}
	m = fpgen.NewMaster(fpgen.Stream(p.seed, "finger:"+humanID))
	p.mu.Lock()
	p.masters[humanID] = m
	p.mu.Unlock()
	return m
}

// Warm builds the masters of humanIDs concurrently. Each master already
// filters in parallel, so half the cores is enough.
func (p *Prints) Warm(humanIDs []string) {
	sem := make(chan struct{}, max(1, runtime.NumCPU()/2))
	var wg sync.WaitGroup
	for _, id := range humanIDs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			p.master(id)
		}()
	}
	wg.Wait()
}

// Rolled returns a PNG rolled impression of the human's finger for one
// enrollment file, drawn from its own stream (register, sequence).
func (p *Prints) Rolled(humanID, register string, seq int16) ([]byte, error) {
	img := p.master(humanID).Rolled(fpgen.Stream(p.seed, fmt.Sprintf("rolled:%s:%d", register, seq)))
	return encodePNG(img)
}

// liftGap separates the latents on a lift card, in the card's surface grey.
const liftGap = 24

// LiftCard renders one fingerprint evidence as a lift card: a latent per
// trace, side by side in trace order, each from its own stream. It returns the
// PNG and each trace's box, which is its latent's tile: the analyst's crop,
// not a detection.
func (p *Prints) LiftCard(c world.Case, ev world.Evidence, humans map[string]world.Human) ([]byte, []Box, error) {
	if len(ev.Traces) == 0 {
		return nil, nil, fmt.Errorf("media: case %s evidence %d has no traces", c.CaseID, ev.Sequence)
	}
	card := image.NewGray(image.Rect(0, 0, len(ev.Traces)*(fpgen.W+liftGap)-liftGap, fpgen.H))
	for i := range card.Pix {
		card.Pix[i] = 204 // the latents' surface level, 0.80
	}
	boxes := make([]Box, len(ev.Traces))
	for i, t := range ev.Traces {
		if _, ok := humans[t.HumanID]; !ok {
			return nil, nil, fmt.Errorf("media: trace %d: unknown human %s", t.Sequence, t.HumanID)
		}
		lat, _ := p.master(t.HumanID).Latent(fpgen.Stream(p.seed, fmt.Sprintf("latent:%s:%d:%d", c.CaseID, ev.Sequence, t.Sequence)))
		x := i * (fpgen.W + liftGap)
		draw.Draw(card, image.Rect(x, 0, x+fpgen.W, fpgen.H), lat, image.Point{}, draw.Src)
		boxes[i] = Box{X1: x, Y1: 0, X2: x + fpgen.W, Y2: fpgen.H}
	}
	data, err := encodePNG(card)
	return data, boxes, err
}

func encodePNG(img image.Image) ([]byte, error) {
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
