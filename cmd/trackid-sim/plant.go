package main

// plant works the first examination of some reviews as the class's planting account (users), and
// decides a share of their pairs wrongly on purpose -- a NEGATIVE on two faces of one person, a
// POSITIVE on two people -- so the students who verify them blind, and the third examiner when
// they disagree, have errors to catch. Every other pair it decides right. It works through
// trackid's own examination package, as a student does in the UI; the sim still never simulates
// a student.
//
// What was planted goes only to out/plant.json, which the debrief reads; trackid holds nothing
// that tells a planted decision from any other (no note, no flag), and the planting account's code
// looks like every student's. It stays deactivated between runs, so distribution never hands it a
// student's work.
//
//	trackid-sim plant -world out/world.json -env ../trackid/.env -analyses 6 -wrong 0.3 -false-ids 0.5
//	trackid-sim plant -world out/world.json -env ../trackid/.env -reports   # once they reach REPORT

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"time"

	"github.com/hibiken/asynq"
	"github.com/rodfileto/trackid/reports"
	"github.com/rodfileto/trackid/storage"

	"github.com/rodfileto/trackid-sim/load"
	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/analyses"
	"github.com/rodfileto/trackid/auth"
	"github.com/rodfileto/trackid/distribution"
	"github.com/rodfileto/trackid/embedding"
	"github.com/rodfileto/trackid/examination"
)

// plantRecord is out/plant.json: every analysis the planting account examined and how it decided
// each pair, against the world's truth.
type plantRecord struct {
	Planter  string          `json:"planter"`
	Analyses []plantAnalysis `json:"analyses"`
}

type plantAnalysis struct {
	AnalysisID int64       `json:"analysisId"`
	CaseID     string      `json:"caseId"`
	PlantedAt  time.Time   `json:"plantedAt"`
	Pairs      []plantPair `json:"pairs"`
	Reported   string      `json:"reported,omitempty"` // the report number, once plant -reports issued it
}

type plantPair struct {
	FeatureA string `json:"featureA"`
	FeatureB string `json:"featureB"`
	Genuine  bool   `json:"genuine"` // both faces are the same person
	Decision string `json:"decision"`
	Wrong    bool   `json:"wrong"` // decided wrongly on purpose
}

func cmdPlant(args []string) {
	fs := flag.NewFlagSet("plant", flag.ExitOnError)
	worldPath := fs.String("world", "out/world.json", "world file the database was loaded from")
	envFile := fs.String("env", "", "dotenv file with DATABASE_URL and REDIS_URL")
	keyPath := fs.String("key", "out/class-key.json", "key file from users")
	outPath := fs.String("out", "out/plant.json", "record of what was planted (kept out of trackid and git)")
	n := fs.Int("analyses", 6, "how many pending reviews to examine")
	wrong := fs.Float64("wrong", 0.3, "share of their pairs to decide wrongly on purpose")
	falseIDs := fs.Float64("false-ids", 0.5, "share of those wrong decisions to make on different-people pairs (false identifications); the rest are false exclusions")
	seed := fs.Uint64("seed", uint64(time.Now().UnixNano()), "random seed: which reviews, and which pairs go wrong")
	reports := fs.Bool("reports", false, "instead, issue the reports of the planted analyses that reached REPORT")
	fs.Parse(args)

	key, err := readClassKey(*keyPath)
	if err != nil {
		log.Fatalf("%v (run users first)", err)
	}
	var record plantRecord
	if err := readJSON(*outPath, &record); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	record.Planter = key.Planter

	ctx := context.Background()
	sqlDB := connect(ctx, *envFile)
	defer sqlDB.Close()
	repo := auth.NewRepository(sqlDB)
	instructor := mustUser(ctx, repo, key.Instructor.Username)
	planter := activate(ctx, repo, key.Planter, true)
	defer activate(ctx, repo, key.Planter, false)

	if *reports {
		issueReports(ctx, sqlDB, planter, &record)
		writeJSON(*outPath, record)
		return
	}

	w, err := world.Load(*worldPath)
	if err != nil {
		log.Fatal(err)
	}
	features, err := load.Features(ctx, sqlDB, w)
	must(err, "features")
	human := map[string]string{}
	for _, f := range features {
		human[f.ID] = f.HumanID
	}

	// Pending reviews of simulated cases with at least one pair, every face of them in the world.
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT ca.id, bc.case_id FROM case_analyses ca
		JOIN biometric_cases bc ON bc.id = ca.biometric_case_id
		WHERE ca.analysis_type = 'FACIAL_REVIEW' AND ca.status = 'OPEN' AND ca.stage = 'EXAMINATION'
		  AND bc.case_id LIKE 'SIM-%'
		  AND NOT EXISTS (SELECT 1 FROM current_analysis_assignments caa
		                  WHERE caa.case_analysis_id = ca.id AND caa.user_id IS NOT NULL)
		ORDER BY ca.id`)
	must(err, "pending reviews")
	type candidate struct {
		id     int64
		caseID string
	}
	var pending []candidate
	for rows.Next() {
		var c candidate
		must(rows.Scan(&c.id, &c.caseID), "pending reviews")
		pending = append(pending, c)
	}
	must(rows.Err(), "pending reviews")
	rows.Close()

	rng := rand.New(rand.NewPCG(*seed, 0))
	rng.Shuffle(len(pending), func(i, j int) { pending[i], pending[j] = pending[j], pending[i] })
	var eligible []plantAnalysis
	for _, c := range pending {
		ws, err := examination.GetWorksheet(ctx, sqlDB, instructor, c.id)
		must(err, "worksheet")
		a := plantAnalysis{AnalysisID: c.id, CaseID: c.caseID}
		known := len(ws.Pairs) > 0
		for _, p := range ws.Pairs {
			ha, okA := human[p.FeatureA]
			hb, okB := human[p.FeatureB]
			known = known && okA && okB
			a.Pairs = append(a.Pairs, plantPair{FeatureA: p.FeatureA, FeatureB: p.FeatureB, Genuine: ha == hb})
		}
		if known {
			eligible = append(eligible, a)
		}
	}
	chosen := chooseAnalyses(eligible, *n)
	if len(chosen) < *n {
		log.Printf("only %d pending reviews with pairs to plant in (asked for %d)", len(chosen), *n)
	}

	var all []*plantPair
	for i := range chosen {
		for j := range chosen[i].Pairs {
			all = append(all, &chosen[i].Pairs[j])
		}
	}
	want := int(math.Round(*wrong * float64(len(all))))
	if ids, short := pickWrong(all, want, *falseIDs, rng); short > 0 {
		log.Printf("only %d different-people pairs to plant false identifications on; %d more false exclusions instead", ids, short)
	}
	for _, p := range all {
		p.Decision = examination.Negative
		if p.Genuine != p.Wrong {
			p.Decision = examination.Positive
		}
	}

	now := time.Now().UTC()
	for i := range chosen {
		a := &chosen[i]
		must(distribution.Assign(ctx, sqlDB, a.AnalysisID, &planter.ID, instructor.ID), "assign")
		for _, p := range a.Pairs {
			// Reviews only: no conclusion level, which a comparison would need.
			must(examination.SaveDraft(ctx, sqlDB, planter, a.AnalysisID, examination.Pair{FeatureA: p.FeatureA, FeatureB: p.FeatureB}, p.Decision, "", ""), "draft")
		}
		res, err := examination.Submit(ctx, sqlDB, planter, a.AnalysisID)
		must(err, "submit")
		a.PlantedAt = now
		record.Analyses = append(record.Analyses, *a)
		writeJSON(*outPath, record) // after each, so a failure later loses nothing done
		wrongHere := 0
		for _, p := range a.Pairs {
			if p.Wrong {
				wrongHere++
			}
		}
		fmt.Printf("analysis %-4d %-16s %d pairs, %d wrong on purpose -> %s\n", a.AnalysisID, a.CaseID, len(a.Pairs), wrongHere, res.Stage)
	}
	enqueueSync()
	fmt.Printf("\nrecorded in %s; distribute the verifications as usual\n", *outPath)
}

// chooseAnalyses takes n of the eligible reviews (already shuffled), those holding a
// different-people pair first: review candidates are mostly one person, and a false
// identification can only be planted on two.
func chooseAnalyses(eligible []plantAnalysis, n int) []plantAnalysis {
	var withImpostor, others []plantAnalysis
	for _, a := range eligible {
		has := false
		for _, p := range a.Pairs {
			has = has || !p.Genuine
		}
		if has {
			withImpostor = append(withImpostor, a)
		} else {
			others = append(others, a)
		}
	}
	chosen := append(withImpostor, others...)
	if len(chosen) > n {
		chosen = chosen[:n]
	}
	return chosen
}

// pickWrong marks want of the pairs to be decided wrongly: round(falseIDs*want) of them
// different-people pairs (planted false identifications) and the rest same-person pairs (false
// exclusions), each drawn at random, one kind making up for the other when it runs out. It returns
// how many false identifications it planted, and by how many it fell short of the share asked for.
func pickWrong(pairs []*plantPair, want int, falseIDs float64, rng *rand.Rand) (ids, short int) {
	var impostor, genuine []*plantPair
	for _, i := range rng.Perm(len(pairs)) {
		if pairs[i].Genuine {
			genuine = append(genuine, pairs[i])
		} else {
			impostor = append(impostor, pairs[i])
		}
	}
	want = min(want, len(pairs))
	wantIDs := int(math.Round(falseIDs * float64(want)))
	ids = min(wantIDs, len(impostor))
	exclusions := min(want-ids, len(genuine))
	ids = min(want-exclusions, len(impostor)) // more false identifications if same-person pairs ran out
	for _, p := range impostor[:ids] {
		p.Wrong = true
	}
	for _, p := range genuine[:exclusions] {
		p.Wrong = true
	}
	return ids, max(0, wantIDs-ids)
}

// issueReports closes the planted analyses that reached REPORT: only their first examiner, the
// planting account, may issue the report. trackid issues it as it would in the UI, from the
// generated lead report (nothing written in its optional observations): it needs trackid's object
// storage (S3_*) and its PDF renderer (WORDCRAFT_CLI) from -env.
func issueReports(ctx context.Context, sqlDB *sql.DB, planter auth.UserProfile, record *plantRecord) {
	store, err := storage.NewClient(ctx, os.Getenv("S3_ENDPOINT"), os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), os.Getenv("S3_BUCKET"))
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}
	if os.Getenv("WORDCRAFT_CLI") == "" {
		log.Fatal("WORDCRAFT_CLI is not set: trackid renders issued reports with it")
	}
	svc := reports.Service{DB: sqlDB, Store: store, Renderer: reports.CLIRenderer{Path: os.Getenv("WORDCRAFT_CLI")},
		Images: func(ctx context.Context, featureID string) (reports.Image, error) {
			img, err := examination.FeatureImage(ctx, sqlDB, store, featureID)
			return reports.Image{Data: img.Data, ContentType: img.ContentType}, err
		}}
	for i := range record.Analyses {
		a := &record.Analyses[i]
		if a.Reported != "" {
			continue
		}
		report, err := svc.Issue(ctx, planter, a.AnalysisID)
		switch {
		case errors.Is(err, analyses.ErrNotReportStage):
			fmt.Printf("analysis %-4d %-16s not at REPORT yet\n", a.AnalysisID, a.CaseID)
		case err != nil:
			log.Fatalf("analysis %d: %v", a.AnalysisID, err)
		default:
			a.Reported = report.Number
			fmt.Printf("analysis %-4d %-16s report %s\n", a.AnalysisID, a.CaseID, report.Number)
		}
	}
}

func mustUser(ctx context.Context, repo *auth.Repository, username string) auth.UserProfile {
	u, err := repo.FindByStaffNumber(ctx, username)
	if err != nil {
		log.Fatalf("%s: %v (run users first)", username, err)
	}
	return u.Profile
}

// activate turns the planting account on for a run and off after it.
func activate(ctx context.Context, repo *auth.Repository, username string, on bool) auth.UserProfile {
	u := mustUser(ctx, repo, username)
	p, err := repo.UpdateAccess(ctx, u.ID, auth.Access{AccountRole: u.AccountRole, ExaminesFacial: u.ExaminesFacial,
		ExaminesFingerprint: u.ExaminesFingerprint, IntelligenceAnalyst: u.IntelligenceAnalyst, Coordinator: u.Coordinator, IsActive: on})
	must(err, "activate "+username)
	return p
}

// enqueueSync asks trackid's worker to reconcile clusters with the new decisions, as the API does
// after a submit.
func enqueueSync() {
	opt, err := asynq.ParseRedisURI(os.Getenv("REDIS_URL"))
	if err != nil {
		log.Printf("REDIS_URL: %v; clusters update at the next sync", err)
		return
	}
	client := asynq.NewClient(opt)
	defer client.Close()
	embedding.EnqueueSyncFace(client)
}
