package load

import (
	"context"
	"database/sql"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/rodfileto/trackid-sim/world"
)

// A burst sends a world's material to trackid all at once, or spread evenly over a short
// window, from several senders in parallel, as material arriving at an institute in a rush
// would (the stress test, docs/stress-test-plan.md in ../trackid). Ingest, by contrast, walks
// the world one enrollment and one case at a time.
//
// Everything is drawn before the clock starts, so the burst times trackid (upload, ingest,
// queueing), not the simulator's rendering. Occurrences go in first, also outside the burst:
// they are records cases link to, with no pipeline behind them.

// BurstOptions sets how a burst is sent.
type BurstOptions struct {
	Senders int           // operations in flight at once; at least 1
	Window  time.Duration // operations are released evenly over it; 0 releases them all at once
	Seed    uint64        // the order enrollments and cases are interleaved in
	// CacheDir, when set, keeps the drawn files on disk by world (DrawCache), so a burst
	// of the same world again skips the drawing.
	CacheDir string
}

// Op is one operation of a burst as it went: an enrollment (one person's records, sent
// together so two senders never race on the same person) or a case with its request.
type Op struct {
	Kind     string        `json:"kind"` // "enrollment" | "case"
	ID       string        `json:"id"`   // the person id, or the case id
	Files    int           `json:"files"`
	Bytes    int           `json:"bytes"`
	Released time.Duration `json:"released"` // since the burst started
	Started  time.Duration `json:"started"`  // when a sender took it; minus Released is the wait
	Took     time.Duration `json:"took"`
	Err      string        `json:"err,omitempty"`
}

// BurstResult is what a burst did.
type BurstResult struct {
	Prepare     time.Duration `json:"prepare"`     // drawing every file, before the clock starts
	Cached      bool          `json:"cached"`      // the files came from the draw cache
	Occurrences time.Duration `json:"occurrences"` // writing the occurrences, before the clock starts
	Start       time.Time     `json:"start"`       // when the first operation was released
	Send        time.Duration `json:"send"`        // until the last operation returned
	Ops         []Op          `json:"ops"`         // in release order
}

// Errors counts the operations that failed.
func (r BurstResult) Errors() int {
	n := 0
	for _, op := range r.Ops {
		if op.Err != "" {
			n++
		}
	}
	return n
}

type burstOp struct {
	Op
	run func(context.Context) error
}

// Burst draws every enrollment and case of w, writes its occurrences, then sends the rest as
// a burst. A failed operation is recorded in its Op and the burst goes on: failures under load
// are what the stress test is looking for. The error is for what stops the burst itself.
func Burst(ctx context.Context, sqlDB *sql.DB, w *world.World, m *Media, opt BurstOptions, logf func(string, ...any)) (BurstResult, error) {
	var res BurstResult
	if opt.Senders < 1 {
		return res, fmt.Errorf("load: a burst needs at least one sender")
	}
	humans := humansByID(w)

	t0 := time.Now()
	enrollments, cases, cached, err := drawWorld(m, w, humans, opt.CacheDir, logf)
	if err != nil {
		return res, err
	}
	res.Cached = cached
	var ops []burstOp
	byPerson := map[string]int{} // index into ops
	for k, e := range w.Enrollments {
		in := enrollments[k]
		i, ok := byPerson[e.PersonID]
		if !ok {
			i = len(ops)
			byPerson[e.PersonID] = i
			ops = append(ops, burstOp{Op: Op{Kind: "enrollment", ID: e.PersonID}})
		}
		op := &ops[i]
		prev := op.run
		op.run = func(ctx context.Context) error {
			if prev != nil {
				if err := prev(ctx); err != nil {
					return err
				}
			}
			return in.ingest(ctx, sqlDB, m)
		}
		for _, f := range in.files {
			op.Files++
			op.Bytes += len(f.data)
		}
	}
	occ := make(map[string]world.Occurrence, len(w.Occurrences))
	for _, o := range w.Occurrences {
		occ[o.ID] = o
	}
	for k, c := range w.Cases {
		in := cases[k]
		op := burstOp{Op: Op{Kind: "case", ID: c.CaseID}, run: func(ctx context.Context) error { return in.ingest(ctx, sqlDB, m, occ) }}
		for _, ev := range in.evidences {
			if ev.data != nil {
				op.Files++
				op.Bytes += len(ev.data)
			}
		}
		ops = append(ops, op)
	}
	r := rand.New(rand.NewPCG(opt.Seed, 0xb0057))
	r.Shuffle(len(ops), func(i, j int) { ops[i], ops[j] = ops[j], ops[i] })
	res.Prepare = time.Since(t0)
	if logf != nil {
		logf("  burst: drew %d operations in %s", len(ops), res.Prepare.Round(time.Millisecond))
	}

	t1 := time.Now()
	if _, err := ingestOccurrences(ctx, sqlDB, w, nil); err != nil {
		return res, err
	}
	res.Occurrences = time.Since(t1)

	for i := range ops {
		ops[i].Released = Release(i, len(ops), opt.Window)
	}
	res.Start = time.Now()
	if logf != nil {
		logf("  burst: sending %d operations from %d senders over %s ...", len(ops), opt.Senders, opt.Window)
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for range opt.Senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				op := &ops[i]
				op.Started = time.Since(res.Start)
				err := op.run(ctx)
				op.Took = time.Since(res.Start) - op.Started
				if err != nil {
					op.Err = err.Error()
				}
			}
		}()
	}
	var stopped error
release:
	for i := range ops {
		if d := time.Until(res.Start.Add(ops[i].Released)); d > 0 {
			select {
			case <-ctx.Done():
				stopped = ctx.Err()
				break release
			case <-time.After(d):
			}
		}
		select {
		case <-ctx.Done():
			stopped = ctx.Err()
			break release
		case work <- i:
		}
	}
	close(work)
	wg.Wait()
	res.Send = time.Since(res.Start)
	res.Ops = make([]Op, len(ops))
	for i, op := range ops {
		res.Ops[i] = op.Op
	}
	return res, stopped
}

// Release is when the i-th of n operations is released in a window: evenly spaced from the
// start, the last one at the window's end. A zero window releases everything at once.
func Release(i, n int, window time.Duration) time.Duration {
	if window <= 0 || n <= 1 {
		return 0
	}
	return time.Duration(int64(window) * int64(i) / int64(n-1))
}
