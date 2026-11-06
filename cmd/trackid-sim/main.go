// trackid-sim generates a synthetic, ground-truthed population (names, enrollments,
// offending, cases), loads it into a trackid database through trackid's own
// extension contracts, lets trackid's own pipeline embed, match and cluster the
// faces, and scores the result against the truth -- the clusters and, after the review
// exercise, each examiner's decisions.
//
//	trackid-sim generate -out out/world.json
//	trackid-sim load     -world out/world.json -env ../trackid/.env
//	trackid-sim eval     -world out/world.json -env ../trackid/.env
//	trackid-sim users    -env ../trackid/.env -examiners 12   # instructor + coded examiners, key in out/class-key.json
//	trackid-sim plant    -world out/world.json -env ../trackid/.env -analyses 6 -wrong 0.3   # errors on purpose, record in out/plant.json
//	trackid-sim snapshot -env ../trackid/.env -out out/snapshot.json   # before the exercise
//	trackid-sim eval     -world out/world.json -env ../trackid/.env -before out/snapshot.json
//	trackid-sim debrief  -world out/world.json -env ../trackid/.env -before out/snapshot.json   # out/debrief.html
//	trackid-sim stress   -humans 600 -senders 8 -window 5m   # on the stress stack only; see scripts/stress/
//	trackid-sim frame    -world out/world.json -case SIM-2015-00001 -level medium
//	trackid-sim fingerprints -fingers 100 -out tools/fpgen/out/go_impressions   # for tools/fpscore
//	trackid-sim fetch-lexicon -out out/lexicon   # Censo 2022 names; see onomastic/ibge.go
//	trackid-sim fetch-isp                        # ISP-RJ counts per CISP + boundaries -> world/data/rio
//	trackid-sim fetch-banks                      # bank branches, geocoded -> world/data/rio
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/hibiken/asynq"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"

	"github.com/rodfileto/trackid-sim/eval"
	"github.com/rodfileto/trackid-sim/load"
	"github.com/rodfileto/trackid-sim/media"
	"github.com/rodfileto/trackid-sim/onomastic"
	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/cluster"
	"github.com/rodfileto/trackid/storage"
)

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "generate":
		cmdGenerate(os.Args[2:])
	case "load":
		cmdLoad(os.Args[2:])
	case "eval":
		cmdEval(os.Args[2:])
	case "fetch-lexicon":
		cmdFetchLexicon(os.Args[2:])
	case "frame":
		cmdFrame(os.Args[2:])
	case "fingerprints":
		cmdFingerprints(os.Args[2:])
	case "fetch-isp":
		cmdFetchISP(os.Args[2:])
	case "fetch-banks":
		cmdFetchBanks(os.Args[2:])
	case "users":
		cmdUsers(os.Args[2:])
	case "snapshot":
		cmdSnapshot(os.Args[2:])
	case "plant":
		cmdPlant(os.Args[2:])
	case "debrief":
		cmdDebrief(os.Args[2:])
	case "stress":
		cmdStress(os.Args[2:])
	case "stress-report":
		cmdStressReport(os.Args[2:])
	default:
		usage()
	}
}

func usage() {
	log.Fatal("usage: trackid-sim <generate|load|eval|users|snapshot|plant|debrief|stress|stress-report|frame|fingerprints|fetch-lexicon|fetch-isp|fetch-banks> [flags]")
}

func cmdGenerate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	cfg := world.DefaultConfig()
	out := fs.String("out", "out/world.json", "where to write the world")
	lexicon := fs.String("lexicon", "", "directory with given_female.txt, given_male.txt, surnames.txt (default: bundled placeholder)")
	fs.Uint64Var(&cfg.Seed, "seed", cfg.Seed, "random seed")
	fs.IntVar(&cfg.Humans, "humans", cfg.Humans, "number of humans")
	fs.Float64Var(&cfg.OffenderFraction, "offenders", cfg.OffenderFraction, "share of humans who offend")
	fs.Float64Var(&cfg.EnrolledFraction, "enrolled", cfg.EnrolledFraction, "share of non-offenders enrolled")
	fs.Float64Var(&cfg.DuplicatePersonRate, "duplicates", cfg.DuplicatePersonRate, "share of enrolled humans enrolled again under another person")
	fs.Float64Var(&cfg.DoubleIdentityRate, "doubles", cfg.DoubleIdentityRate, "share of humans who also hold a record under another enrolled human's person and name")
	fs.Float64Var(&cfg.FraudLicenceRate, "fraud-licence", cfg.FraudLicenceRate, "share of those documents that are driver's licences; the rest are RGs from another state")
	fs.Float64Var(&cfg.FraudWithFiscalRate, "fraud-fiscal", cfg.FraudWithFiscalRate, "share of those documents carrying the copied person's fiscal number")
	fs.Float64Var(&cfg.FraudsterEnrolledRate, "fraudster-enrolled", cfg.FraudsterEnrolledRate, "share of those humans who also have a record of their own")
	poolPath := fs.String("pool", "tools/facegen/pool_index.json", "face pool index to bind faces to humans (empty: no faces)")
	fs.Parse(args)

	lex := onomastic.SeedLexicon()
	if *lexicon != "" {
		var err error
		if lex, err = onomastic.LoadLexicon(*lexicon); err != nil {
			log.Fatal(err)
		}
	}
	w, err := world.Generate(cfg, lex)
	if err != nil {
		log.Fatal(err)
	}
	var faces *world.FaceSummary
	if *poolPath != "" {
		pool, ref, err := world.LoadFacePool(*poolPath)
		if err != nil {
			log.Fatal(err)
		}
		s := world.AssignFaces(w, pool, ref)
		faces = &s
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		log.Fatal(err)
	}
	if err := w.Save(*out); err != nil {
		log.Fatal(err)
	}
	s := w.Summary()
	log.Printf("wrote %s", *out)
	log.Printf("humans=%d offenders=%d (%d never enrolled)", s.Humans, s.Offenders, s.UnenrolledOffenders)
	log.Printf("enrollments=%d (renewals=%d, cross-person duplicates=%d, double identities=%d)", s.Enrollments, s.Renewals, s.Duplicates, s.DoubleIdentities)
	log.Printf("occurrences=%d (civil=%d)", s.Occurrences, s.CivilOccurrences)
	log.Printf("cases=%d (facial=%d, fingerprint=%d, civil=%d) traces=%d (bystanders=%d)", s.Cases, s.FacialCases, s.FingerprintCases, s.CivilCases, s.Traces, s.BystanderTraces)
	log.Printf("full-name collisions: %d humans (%.2f%%) share their name with another human, across %d names",
		s.SharedNameHumans, 100*float64(s.SharedNameHumans)/float64(max(s.Humans, 1)), s.SharedNames)
	if faces != nil {
		f := *faces
		log.Printf("faces: %d/%d humans (%d/%d captured on camera, %d/%d double-identity pairs), age gap mean %.1f max %d",
			f.Assigned, f.Needed, f.CaptureAssigned, f.CaptureNeeded, f.DoublesWithFaces, f.Doubles, f.MeanAgeGap, f.MaxAgeGap)
		if f.DroppedPhotos+f.DroppedTraces > 0 {
			log.Printf("faces: pool too small -- dropped %d photos, %d facial traces, %d cases (lower -humans to keep them)",
				f.DroppedPhotos, f.DroppedTraces, f.DroppedCases)
		}
	}
}

// connect opens Postgres from the environment.
func connect(ctx context.Context, envFile string) *sql.DB {
	if envFile != "" {
		if err := godotenv.Load(envFile); err != nil {
			log.Fatalf("env: %v", err)
		}
	}
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("DATABASE_URL is not set (pass -env ../trackid/.env or export it)")
	}
	sqlDB, err := sql.Open("pgx", url)
	if err != nil {
		log.Fatal(err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		log.Fatalf("postgres: %v", err)
	}
	return sqlDB
}

// openMedia connects to trackid's object storage and task queue from the
// environment connect already loaded, opens the world's face pool and sets up
// its fingerprints.
func openMedia(ctx context.Context, w *world.World) (*load.Media, func() error) {
	endpoint, bucket := os.Getenv("S3_ENDPOINT"), os.Getenv("S3_BUCKET")
	if endpoint == "" || bucket == "" {
		log.Fatal("S3_ENDPOINT/S3_BUCKET are not set (pass -env ../trackid/.env, or -images=false for placeholders)")
	}
	store, err := storage.NewClient(ctx, endpoint, os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), bucket)
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}
	pool, err := media.OpenPool(w)
	if err != nil {
		log.Fatal(err)
	}
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		log.Fatal("REDIS_URL is not set (pass -env ../trackid/.env, or -images=false for placeholders)")
	}
	opt, err := asynq.ParseRedisURI(redisURL)
	if err != nil {
		log.Fatalf("redis: %v", err)
	}
	client := asynq.NewClient(opt)
	return &load.Media{Store: store, Pool: pool, Prints: media.NewPrints(w.Config.Seed), Queue: client}, client.Close
}

func cmdLoad(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	worldPath := fs.String("world", "out/world.json", "world file from generate")
	envFile := fs.String("env", "", "dotenv file with DATABASE_URL, S3_* and REDIS_URL")
	force := fs.Bool("force", false, "load even if the database holds non-simulated data (NEVER use on real data)")
	images := fs.Bool("images", true, "upload enrollment photos and prints and rendered evidence (CCTV frames, lift cards), and send them through trackid's pipeline; false writes sim:// placeholders and nothing is matched")
	wait := fs.Duration("wait", 15*time.Minute, "how long to wait for trackid's worker to embed or extract, match and cluster the faces and prints")
	fs.Parse(args)

	ctx := context.Background()
	w, err := world.Load(*worldPath)
	if err != nil {
		log.Fatal(err)
	}
	sqlDB := connect(ctx, *envFile)
	defer sqlDB.Close()

	if !*force {
		if err := load.CheckSafe(ctx, sqlDB); err != nil {
			log.Fatal(err)
		}
	}
	var m *load.Media
	if *images {
		var closeQueue func() error
		m, closeQueue = openMedia(ctx, w)
		defer closeQueue()
	}
	log.Printf("ingesting %d enrollments and %d cases via identity.Ingest / cases.Ingest ...", len(w.Enrollments), len(w.Cases))
	if err := load.Ingest(ctx, sqlDB, w, m, log.Printf); err != nil {
		log.Fatal(err)
	}

	if m != nil {
		log.Printf("waiting for trackid's worker to embed, extract, match and cluster the faces and prints ...")
		p, err := load.Wait(ctx, sqlDB, os.Getenv("REDIS_URL"), m.Queue, 5*time.Second, *wait, log.Printf)
		must(err, "pipeline")
		if p.Embedded < p.Faces {
			log.Printf("  %d of %d faces have no embedding (no face detected in the image)", p.Faces-p.Embedded, p.Faces)
		}
		if p.Extracted < p.Prints {
			log.Printf("  %d of %d prints have no template (the extractor could not use the image)", p.Prints-p.Extracted, p.Prints)
		}
	}
	features, err := load.Features(ctx, sqlDB, w)
	must(err, "features")
	printLayers(ctx, sqlDB, features)
}

func cmdEval(args []string) {
	fs := flag.NewFlagSet("eval", flag.ExitOnError)
	worldPath := fs.String("world", "out/world.json", "world file the database was loaded from")
	envFile := fs.String("env", "", "dotenv file with DATABASE_URL")
	before := fs.String("before", "", "cluster snapshot taken before the review exercise (snapshot), to report what the exercise changed")
	fs.Parse(args)

	ctx := context.Background()
	w, err := world.Load(*worldPath)
	if err != nil {
		log.Fatal(err)
	}
	sqlDB := connect(ctx, *envFile)
	defer sqlDB.Close()

	features, err := load.Features(ctx, sqlDB, w)
	must(err, "features")
	printLayers(ctx, sqlDB, features)

	ds, err := eval.Decisions(ctx, sqlDB)
	must(err, "decisions")
	fmt.Printf("\n== human decisions ==\n\n")
	for _, r := range eval.ScoreDecisions(features, ds) {
		fmt.Print(r)
	}

	basis, err := eval.ScoreBasis(ctx, sqlDB, features, load.IsQuestioned)
	must(err, "identifications by basis")
	fmt.Printf("\n== identifications by basis (lead, laudo) ==\n\n")
	if len(basis) == 0 {
		fmt.Println("none yet: no examiner has confirmed an identification")
	}
	for _, b := range basis {
		fmt.Print(b)
	}

	if *before != "" {
		prev := readSnapshot(*before)
		fmt.Printf("\n== cluster changes since %s ==\n\n", prev.TakenAt.Format(time.RFC3339))
		for _, basis := range cluster.Bases {
			cur, err := eval.Snapshot(ctx, sqlDB, string(basis))
			must(err, "snapshot")
			merges, err := eval.Merges(ctx, sqlDB, string(basis))
			must(err, "merges")
			fmt.Printf("%-9s\n%s", basis, eval.CompareSnapshots(prev.Layers[string(basis)], cur, merges))
		}
	}
}

// printLayers scores both of trackid's cluster layers: AUTOMATIC (every
// confirmed pair, SYSTEM matches included) and REVIEWED (the pairs people
// confirmed), which is what the case and person pages show.
func printLayers(ctx context.Context, sqlDB *sql.DB, features []eval.Feature) {
	for _, basis := range cluster.Bases {
		assign, err := eval.Snapshot(ctx, sqlDB, string(basis))
		must(err, "snapshot")
		fmt.Printf("\n== %s clusters ==\n", basis)
		printMetrics(features, assign)
	}
}

func printMetrics(features []eval.Feature, assign map[string]int64) {
	fmt.Println()
	for _, m := range eval.Evaluate(features, assign, load.IsQuestioned) {
		fmt.Print(m)
	}
}

func cmdFetchLexicon(args []string) {
	fs := flag.NewFlagSet("fetch-lexicon", flag.ExitOnError)
	outDir := fs.String("out", "out/lexicon", "directory to write the lexicon, the ranking snapshots and dropped.csv")
	loc := fs.String("localidade", "0", "0 (Brazil), a state (SP or 35) or a 7-digit municipality code")
	minPerMillion := fs.Float64("min-per-million", 5, "keep names carried by at least this many people per million inhabitants")
	minSexShare := fs.Float64("min-sex-share", 0.01, "leave a given name off a sex's list when that sex's share of it is below this (0 keeps all)")
	middleNameRatio := fs.Float64("middle-name-ratio", 2, "leave a surname out when it is at least this many times more common as a first name (0 keeps all)")
	pause := fs.Duration("pause", 150*time.Millisecond, "wait between IBGE API requests")
	refresh := fs.Bool("refresh", false, "fetch the rankings again even if snapshots for the same -localidade and -min-per-million exist")
	fs.Parse(args)

	localidade, err := onomastic.Localidade(*loc)
	must(err, "localidade")
	must(os.MkdirAll(*outDir, 0o755), "create output dir")

	// The rankings are saved as snapshots next to the lexicon. They are reused when
	// they were fetched with the same locality and floor, so a rerun makes no requests.
	type source struct {
		Localidade    string  `json:"localidade"`
		MinPerMillion float64 `json:"minPerMillion"`
		Fetched       string  `json:"fetched"`
	}
	sourcePath := filepath.Join(*outDir, "source.json")
	var src source
	if b, err := os.ReadFile(sourcePath); err == nil {
		json.Unmarshal(b, &src)
	}
	reuse := !*refresh && src.Localidade == localidade && src.MinPerMillion == *minPerMillion

	client := onomastic.Client{Pause: *pause}
	ranking := func(file, kind, sex string) []onomastic.RankEntry {
		path := filepath.Join(*outDir, file)
		if reuse {
			f, err := os.Open(path)
			must(err, "read "+path)
			defer f.Close()
			r, err := onomastic.ReadRanking(f)
			must(err, "read "+path)
			return r
		}
		log.Printf("fetching %s ranking%s for localidade %s ...", kind, map[string]string{"": "", "F": " (F)", "M": " (M)"}[sex], localidade)
		r, err := client.FetchRanking(context.Background(), localidade, kind, sex, *minPerMillion)
		must(err, "ibge names api")
		f, err := os.Create(path)
		must(err, "write "+path)
		must(onomastic.WriteRanking(f, r), "write "+path)
		must(f.Close(), "write "+path)
		return r
	}
	female := ranking("ranking_given_female.csv", onomastic.GivenRanking, "F")
	male := ranking("ranking_given_male.csv", onomastic.GivenRanking, "M")
	surnames := ranking("ranking_surnames.csv", onomastic.SurnameRanking, "")
	if !reuse {
		src = source{Localidade: localidade, MinPerMillion: *minPerMillion, Fetched: time.Now().Format("2006-01-02")}
		b, _ := json.MarshalIndent(src, "", "  ")
		must(os.WriteFile(sourcePath, append(b, '\n'), 0o644), "write "+sourcePath)
	}

	femaleList, droppedF := onomastic.GivenNames("given_female", female, male, *minSexShare)
	maleList, droppedM := onomastic.GivenNames("given_male", male, female, *minSexShare)
	surnameList, droppedS := onomastic.Surnames(surnames, onomastic.FirstNameCounts(female, male), *middleNameRatio)

	// IBGE's terms of use allow reuse citing IBGE as the source, and ask that a
	// transformation of its data name who made it in a note.
	header := func(what, rule string) []string {
		return []string{
			"Fonte / source: IBGE, Censo Demografico 2022, Nomes no Brasil (servicodados.ibge.gov.br/api/v3/nomes/2022).",
			fmt.Sprintf("Weights: people with this %s, localidade %s (0 = Brazil). Accents stripped by the source.", what, localidade),
			fmt.Sprintf("Transformation by trackid-sim fetch-lexicon (github.com/rodfileto/trackid-sim), rankings fetched %s:", src.Fetched),
			fmt.Sprintf("kept names carried by at least %g per million inhabitants; %s. Left-out names are in dropped.csv.", *minPerMillion, rule),
		}
	}
	writeList := func(name string, comments []string, list []onomastic.WeightedName) {
		f, err := os.Create(filepath.Join(*outDir, name))
		must(err, "write "+name)
		must(onomastic.WriteList(f, comments, list), "write "+name)
		must(f.Close(), "write "+name)
	}
	writeList("given_female.txt", header("female first given name",
		fmt.Sprintf("left out names under %g%% female", 100**minSexShare)), femaleList)
	writeList("given_male.txt", header("male first given name",
		fmt.Sprintf("left out names under %g%% male", 100**minSexShare)), maleList)
	writeList("surnames.txt", header("word after the first given name (IBGE's surname count)",
		fmt.Sprintf("left out suffixes (Junior, Filho, Neto, Sobrinho) and words at least %gx more common as a first name", *middleNameRatio)), surnameList)

	dropped := append(append(droppedF, droppedM...), droppedS...)
	f, err := os.Create(filepath.Join(*outDir, "dropped.csv"))
	must(err, "write dropped.csv")
	must(onomastic.WriteDropped(f, dropped), "write dropped.csv")
	must(f.Close(), "write dropped.csv")

	log.Printf("wrote %s: %d female, %d male given names, %d surnames (left out: %d female, %d male, %d surnames; see dropped.csv)",
		*outDir, len(femaleList), len(maleList), len(surnameList), len(droppedF), len(droppedM), len(droppedS))
}

func must(err error, what string) {
	if err != nil {
		log.Fatalf("%s: %v", what, err)
	}
}
