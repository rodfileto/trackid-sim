package main

// stress sends a generated world to a trackid stack as a burst and measures how the stack
// copes (docs/stress-test-plan.md in ../trackid): every enrollment and case drawn beforehand,
// then sent by parallel senders within a short window; then the wait for trackid's worker to
// embed, extract, match and cluster it all, with the GPU, the stack's containers and Postgres
// sampled throughout. One JSON record per run goes to out/stress/.
//
// It runs only against the stress stack (scripts/stress/up.sh), never the development one: it
// needs TRACKID_STRESS=1, which scripts/stress/env.sh sets, and an empty database.

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rodfileto/trackid-sim/load"
	"github.com/rodfileto/trackid-sim/onomastic"
	"github.com/rodfileto/trackid-sim/stress"
	"github.com/rodfileto/trackid-sim/world"
)

func cmdStress(args []string) {
	fs := flag.NewFlagSet("stress", flag.ExitOnError)
	cfg := world.DefaultConfig()
	cfg.Humans = 300 // the demo world's size (scripts/reload-world.sh)
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "random seed of the world")
	fs.IntVar(&cfg.Humans, "humans", cfg.Humans, "number of humans; the burst's mix is the demo world's, scaled by this")
	lexicon := fs.String("lexicon", "onomastic/data/ibge2022", "name lexicon directory (empty: bundled placeholder)")
	poolPath := fs.String("pool", "tools/facegen/pool_index.json", "face pool index")
	faceReuse := fs.Bool("face-reuse", true, "let humans share pool faces so nothing is dropped beyond the pool's 200 identities (accuracy is then meaningless; times are not)")
	senders := fs.Int("senders", 8, "operations in flight at once")
	window := fs.Duration("window", 5*time.Minute, "release the operations evenly over this window; 0 sends them all at once")
	timeCap := fs.Duration("timecap", 2*time.Hour, "give up waiting for the pipeline this long after the burst started")
	poll := fs.Duration("poll", 5*time.Second, "how often to look at the pipeline")
	sampleEvery := fs.Duration("sample", 5*time.Second, "how often to sample the GPU, containers and Postgres")
	project := fs.String("project", "trackid-stress", "compose project whose containers are sampled")
	trackidDir := fs.String("trackid", "../trackid", "trackid checkout, for its commit")
	label := fs.String("label", "", "name of the run (default: humans, senders and window)")
	outDir := fs.String("out", "out/stress", "where to write the record")
	cacheDir := fs.String("cache", "out/stress/cache", "keep the drawn files here by world, so the same world again skips the drawing (empty: always draw)")
	fs.Parse(args)

	if os.Getenv("TRACKID_STRESS") != "1" {
		log.Fatal("stress runs only against the stress stack: source scripts/stress/env.sh first (it sets TRACKID_STRESS=1)")
	}
	if *label == "" {
		*label = fmt.Sprintf("h%d-s%d-w%s", cfg.Humans, *senders, *window)
	}
	stamp := time.Now().Format("20060102-150405")
	runDir := filepath.Join(*outDir, stamp+"-"+*label)
	must(os.MkdirAll(runDir, 0o755), "create "+runDir)

	// The world, bound to faces.
	lex := onomastic.SeedLexicon()
	if *lexicon != "" {
		var err error
		lex, err = onomastic.LoadLexicon(*lexicon)
		must(err, "lexicon")
	}
	w, err := world.Generate(cfg, lex)
	must(err, "generate")
	pool, ref, err := world.LoadFacePool(*poolPath)
	must(err, "face pool")
	var faces world.FaceSummary
	if *faceReuse {
		faces = world.ReuseFaces(w, pool, ref)
	} else {
		faces = world.AssignFaces(w, pool, ref)
	}
	must(w.Save(filepath.Join(runDir, "world.json")), "save world")
	s := w.Summary()
	log.Printf("world: %d humans, %d enrollments, %d occurrences, %d cases (facial %d, fingerprint %d), %d traces; faces %d/%d",
		s.Humans, s.Enrollments, s.Occurrences, s.Cases, s.FacialCases, s.FingerprintCases, s.Traces, faces.Assigned, faces.Needed)

	ctx := context.Background()
	sqlDB := connect(ctx, "")
	defer sqlDB.Close()
	must(load.CheckSafe(ctx, sqlDB), "database")
	var n int
	must(sqlDB.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM biometric_cases) + (SELECT count(*) FROM identity_registers)`).Scan(&n), "database")
	if n > 0 {
		log.Fatalf("the database already holds %d cases and registers: a burst needs a fresh stack (scripts/stress/down.sh, then up.sh)", n)
	}
	m, closeQueue := openMedia(ctx, w)
	defer closeQueue()

	rec := stress.Record{
		Label: *label, TakenAt: time.Now(),
		Config: stress.Config{
			Seed: cfg.Seed, Humans: cfg.Humans, FaceReuse: *faceReuse, Senders: *senders, Window: window.String(),
			Workers: os.Getenv("WORKER_CONCURRENCY"), TimeCap: timeCap.String(), Poll: poll.String(),
		},
		Machine: stress.ReadMachine(ctx, ".", *trackidDir),
		World: stress.World{Humans: s.Humans, Enrollments: s.Enrollments, Occurrences: s.Occurrences, Cases: s.Cases,
			FacialCases: s.FacialCases, FingerprintCases: s.FingerprintCases, Traces: s.Traces},
		Notes: map[string]string{},
	}
	if rec.Config.Workers == "" {
		rec.Config.Workers = "10 (default)"
	}
	var bands []string
	rows, err := sqlDB.QueryContext(ctx, `SELECT DISTINCT ON (embedding_type) embedding_type, review_threshold, confirm_threshold
		FROM match_thresholds ORDER BY embedding_type, id DESC`)
	must(err, "thresholds")
	for rows.Next() {
		var t string
		var review, confirm float64
		must(rows.Scan(&t, &review, &confirm), "thresholds")
		bands = append(bands, fmt.Sprintf("%s %g/%g", t, review, confirm))
	}
	rows.Close()
	rec.Config.Thresholds = strings.Join(bands, "; ")
	if len(bands) < 2 {
		log.Printf("warning: match thresholds set for %d modalities only (%s); scripts/stress/up.sh sets both", len(bands), rec.Config.Thresholds)
	}
	if !stress.ResetStatements(ctx, sqlDB) {
		rec.Notes["statements"] = "pg_stat_statements is not loaded; no statement costs"
	}

	sampleStart := time.Now()
	sampler := stress.StartSampler(ctx, sqlDB, *project, sampleStart, *sampleEvery)
	burst, err := load.Burst(ctx, sqlDB, w, m, load.BurstOptions{Senders: *senders, Window: *window, Seed: cfg.Seed, CacheDir: *cacheDir}, log.Printf)
	must(err, "burst")
	rec.Burst = stress.Summarize(burst)
	rec.Ops = burst.Ops
	log.Printf("burst: %d operations sent in %s, %d failed", len(burst.Ops), burst.Send.Round(time.Millisecond), rec.Burst.Errors)

	log.Printf("waiting for trackid's worker (cap %s from the burst's start) ...", *timeCap)
	remaining := *timeCap - time.Since(burst.Start)
	final, waitErr := load.WaitObserved(ctx, sqlDB, os.Getenv("REDIS_URL"), m.Queue, *poll, remaining, log.Printf, func(at time.Time, p load.Progress) {
		rec.Progress = append(rec.Progress, stress.ProgressSample{At: at.Sub(burst.Start).Seconds(), Progress: p})
	})
	rec.Final = final
	rec.Drain.Cleared = time.Since(burst.Start).Seconds()
	if waitErr != nil {
		rec.Drain.TimedOut = true
		rec.Notes["wait"] = waitErr.Error()
		log.Printf("pipeline: %v", waitErr)
	}
	shift := burst.Start.Sub(sampleStart).Seconds()
	for _, smp := range sampler.Stop() {
		smp.At -= shift // seconds since the burst started, like everything else
		rec.Samples = append(rec.Samples, smp)
	}

	drain, err := stress.ReadDrain(ctx, sqlDB, burst.Start)
	must(err, "stage times")
	drain.Cleared, drain.TimedOut = rec.Drain.Cleared, rec.Drain.TimedOut
	rec.Drain = drain
	rec.Counts, err = stress.Counts(ctx, sqlDB)
	must(err, "counts")
	rec.Tables, err = stress.Tables(ctx, sqlDB)
	must(err, "table sizes")
	if rec.Notes["statements"] == "" {
		rec.Statements, err = stress.Statements(ctx, sqlDB)
		must(err, "statements")
	}
	if len(rec.Notes) == 0 {
		rec.Notes = nil
	}

	path := filepath.Join(runDir, "record.json")
	b, err := json.MarshalIndent(rec, "", "  ")
	must(err, "record")
	must(os.WriteFile(path, append(b, '\n'), 0o644), "write "+path)
	printStress(rec)
	log.Printf("wrote %s", path)
	if waitErr != nil || rec.Burst.Errors > 0 {
		os.Exit(1)
	}
}

func printStress(r stress.Record) {
	d := r.Drain
	fmt.Printf("\n== stress %s ==\n\n", r.Label)
	fmt.Printf("world      %d humans, %d enrollments, %d cases, %d traces\n", r.World.Humans, r.World.Enrollments, r.World.Cases, r.World.Traces)
	fmt.Printf("burst      %d ops (%d files, %.1f MiB) from %d senders over %s: sent in %.1fs, %d failed\n",
		r.Burst.Ops, r.Burst.Files, r.Burst.MiB, r.Config.Senders, r.Config.Window, r.Burst.Send, r.Burst.Errors)
	for _, k := range []string{"enrollment", "case"} {
		if q, ok := r.Burst.Took[k]; ok {
			fmt.Printf("  %-10s p50 %.3fs  p95 %.3fs  p99 %.3fs  max %.3fs  (n=%d)\n", k, q.P50, q.P95, q.P99, q.Max, q.N)
		}
	}
	fmt.Printf("drain      cleared at %.0fs%s\n", d.Cleared, map[bool]string{true: " (TIMED OUT)", false: ""}[d.TimedOut])
	fmt.Printf("  last embedding %.0fs, last template %.0fs, last face match %.0fs, last print match %.0fs\n",
		d.LastEmbedding, d.LastTemplate, d.LastFaceMatch, d.LastPrintMatch)
	fmt.Printf("  SYSTEM decisions %.0fs..%.0fs, last cluster %.0fs\n", d.FirstDecision, d.LastDecision, d.LastCluster)
	fmt.Printf("pipeline   %s\n", r.Final)
	peak := stress.Sample{}
	maxQueue := 0
	for _, p := range r.Progress {
		maxQueue = max(maxQueue, p.Queued)
	}
	for _, s := range r.Samples {
		peak.GPUUtil, peak.GPUMemMiB = max(peak.GPUUtil, s.GPUUtil), max(peak.GPUMemMiB, s.GPUMemMiB)
		peak.Load1, peak.PGConnections = max(peak.Load1, s.Load1), max(peak.PGConnections, s.PGConnections)
	}
	fmt.Printf("peaks      queue %d, GPU %d%% / %d MiB, load %.1f, Postgres connections %d\n",
		maxQueue, peak.GPUUtil, peak.GPUMemMiB, peak.Load1, peak.PGConnections)
	for k, v := range r.Counts {
		fmt.Printf("  %-20s %d\n", k, v)
	}
}

// cmdStressReport prints one line per stress record, for comparing levels:
//
//	trackid-sim stress-report out/stress/*/record.json
func cmdStressReport(args []string) {
	if len(args) == 0 {
		log.Fatal("usage: trackid-sim stress-report <record.json>...")
	}
	fmt.Printf("%-28s %6s %6s %5s %6s %6s %7s %8s %8s %8s %8s %6s %5s %6s\n",
		"label", "humans", "ops", "errs", "send", "p95op", "maxQ", "embed", "extract", "fpMatch", "cleared", "GPU%", "load", "cases")
	for _, path := range args {
		b, err := os.ReadFile(path)
		must(err, "read "+path)
		var r stress.Record
		must(json.Unmarshal(b, &r), path)
		maxQ, gpu, load1 := 0, 0, 0.0
		for _, p := range r.Progress {
			maxQ = max(maxQ, p.Queued)
		}
		for _, s := range r.Samples {
			gpu, load1 = max(gpu, s.GPUUtil), max(load1, s.Load1)
		}
		cleared := fmt.Sprintf("%.0fs", r.Drain.Cleared)
		if r.Drain.TimedOut {
			cleared = "TIMEOUT"
		}
		fmt.Printf("%-28s %6d %6d %5d %5.0fs %5.2fs %7d %7.0fs %7.0fs %7.0fs %8s %6d %5.1f %6d\n",
			r.Label, r.World.Humans, r.Burst.Ops, r.Burst.Errors, r.Burst.Send, r.Burst.Took["all"].P95, maxQ,
			r.Drain.LastEmbedding, r.Drain.LastTemplate, r.Drain.LastPrintMatch, cleared, gpu, load1, r.World.Cases)
	}
}
