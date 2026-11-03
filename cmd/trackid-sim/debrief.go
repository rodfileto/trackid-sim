package main

// debrief writes the review exercise's results as one self-contained HTML page, to open on the
// projector after the class: each examiner's decisions against the world's truth, what became of
// every error planted on purpose (caught by the blind verifier? settled right by the third
// examiner?) and, given the snapshot from before the class, what the class did to the clusters.
// Examiners appear by code only; names stay in the key file. The planting account is reported
// apart from the students. The page goes to out/, which git ignores, and never into trackid.
//
//	trackid-sim debrief -world out/world.json -env ../trackid/.env -before out/snapshot.json

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"image"
	"image/jpeg"
	_ "image/png" // trackid serves faces as PNG
	"log"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rodfileto/trackid-sim/eval"
	"github.com/rodfileto/trackid-sim/load"
	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/auth"
	"github.com/rodfileto/trackid/cluster"
	"github.com/rodfileto/trackid/examination"
	"github.com/rodfileto/trackid/storage"
)

type debriefRow struct {
	Code string
	eval.Tally
}

// plantedRow is one planted pair on the page, with its two faces when -images is on: the case's
// own face first.
type plantedRow struct {
	eval.PlantedOutcome
	Questioned, Reference template.URL
}

type debriefData struct {
	Generated  string
	World      string
	Students   []debriefRow
	Planter    *debriefRow
	Agreement  string
	Planted    []plantedRow
	Wrong      int // planted wrong on purpose
	Verified   int // of those, verified
	Caught     int // of those, disputed by the verifier
	SettledOK  int // of those, ended on the truth
	FalseAlarm int // right planted decisions the verifier disputed
	Stability  []string
	Code       func(string) string
}

func cmdDebrief(args []string) {
	fs := flag.NewFlagSet("debrief", flag.ExitOnError)
	worldPath := fs.String("world", "out/world.json", "world file the database was loaded from")
	envFile := fs.String("env", "", "dotenv file with DATABASE_URL")
	keyPath := fs.String("key", "out/class-key.json", "key file from users: limits the page to the class's codes")
	plantPath := fs.String("plant", "out/plant.json", "record from plant")
	before := fs.String("before", "", "cluster snapshot taken before the class (snapshot)")
	outPath := fs.String("out", "out/debrief.html", "page to write")
	images := fs.Bool("images", true, "show the two faces of each planted error (needs trackid's object storage: S3_* in -env)")
	fs.Parse(args)

	key, err := readClassKey(*keyPath)
	if err != nil {
		log.Fatalf("%v (run users first)", err)
	}
	var record plantRecord
	if err := readJSON(*plantPath, &record); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Fatal(err)
	}
	w, err := world.Load(*worldPath)
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	sqlDB := connect(ctx, *envFile)
	defer sqlDB.Close()
	features, err := load.Features(ctx, sqlDB, w)
	must(err, "features")
	ds, err := eval.Decisions(ctx, sqlDB)
	must(err, "decisions")

	// trackid stores usernames lowercased; the class knows its codes as E01, E02, ...
	codes := map[string]string{}
	for _, e := range key.Examiners {
		codes[strings.ToLower(e.Username)] = e.Username
	}
	planter := strings.ToLower(key.Planter)
	var class []eval.Decision
	for _, d := range ds {
		if _, ok := codes[d.Username]; ok && d.Modality == eval.Face {
			class = append(class, d)
		}
	}
	report := eval.ScoreDecisions(features, class)[0]

	data := debriefData{Generated: time.Now().Format("2006-01-02 15:04"), World: *worldPath,
		Code: func(username string) string {
			if c, ok := codes[username]; ok {
				return c
			}
			return username
		}}
	for username, t := range report.ByExaminer {
		row := debriefRow{Code: codes[username], Tally: *t}
		if username == planter {
			data.Planter = &row
			continue
		}
		data.Students = append(data.Students, row)
	}
	sort.Slice(data.Students, func(i, j int) bool { return data.Students[i].Code < data.Students[j].Code })
	data.Agreement = fmt.Sprintf("%d of %d pairs verified agreed with their first examiner", report.Agreed, report.Verified)

	var planted []eval.Planted
	for _, a := range record.Analyses {
		for _, p := range a.Pairs {
			planted = append(planted, eval.Planted{AnalysisID: a.AnalysisID, CaseID: a.CaseID, A: p.FeatureA, B: p.FeatureB,
				Genuine: p.Genuine, Decision: p.Decision, Wrong: p.Wrong})
		}
	}
	var faces func(analysisID int64, featureID string) template.URL
	if *images {
		faces = faceLoader(ctx, sqlDB, mustUser(ctx, auth.NewRepository(sqlDB), key.Instructor.Username))
	}
	for _, o := range eval.PlantedOutcomes(planted, ds) {
		row := plantedRow{PlantedOutcome: o}
		if faces != nil && o.Wrong {
			q, r := o.A, o.B
			if !load.IsQuestioned(q) {
				q, r = r, q
			}
			row.Questioned, row.Reference = faces(o.AnalysisID, q), faces(o.AnalysisID, r)
		}
		data.Planted = append(data.Planted, row)
	}
	for _, o := range data.Planted {
		switch {
		case o.Wrong:
			data.Wrong++
			if o.VerifierDecision != "" {
				data.Verified++
			}
			if o.Disputed() {
				data.Caught++
			}
			if o.SettledRight() {
				data.SettledOK++
			}
		case o.Disputed():
			data.FalseAlarm++
		}
	}

	if *before != "" {
		prev := readSnapshot(*before)
		for _, basis := range cluster.Bases {
			cur, err := eval.Snapshot(ctx, sqlDB, string(basis))
			must(err, "snapshot")
			merges, err := eval.Merges(ctx, sqlDB, string(basis))
			must(err, "merges")
			s := eval.CompareSnapshots(prev.Layers[string(basis)], cur, merges)
			data.Stability = append(data.Stability, fmt.Sprintf("%s: %d features compared, %d unchanged, %d by merges, %d split off (from %d clusters), %d unexplained",
				basis, s.Compared, s.Unchanged, s.ViaMerge, s.SplitOff, s.Splits, s.Unexplained))
		}
	}

	f, err := os.Create(*outPath)
	must(err, "create "+*outPath)
	defer f.Close()
	must(debriefPage.Execute(f, data), "render")
	fmt.Printf("wrote %s: %d students, %d planted errors (%d caught, %d settled right)\n",
		*outPath, len(data.Students), data.Wrong, data.Caught, data.SettledOK)
}

// faceLoader returns a function giving one face of an analysis's pairs as a small inline JPEG,
// through trackid's examination.PairImage as the instructor sees it (a case's face cut to its box);
// "" when it has none.
func faceLoader(ctx context.Context, sqlDB *sql.DB, instructor auth.UserProfile) func(int64, string) template.URL {
	endpoint, bucket := os.Getenv("S3_ENDPOINT"), os.Getenv("S3_BUCKET")
	if endpoint == "" || bucket == "" {
		log.Fatal("S3_ENDPOINT/S3_BUCKET are not set (pass -env ../trackid/.env, or -images=false)")
	}
	store, err := storage.NewClient(ctx, endpoint, os.Getenv("S3_ACCESS_KEY"), os.Getenv("S3_SECRET_KEY"), bucket)
	if err != nil {
		log.Fatalf("object storage: %v", err)
	}
	return func(analysisID int64, featureID string) template.URL {
		img, err := examination.PairImage(ctx, sqlDB, store, instructor, analysisID, featureID)
		if err != nil {
			log.Printf("face %s of analysis %d: %v", featureID, analysisID, err)
			return ""
		}
		uri, err := thumbnailURI(img.Data, 160)
		if err != nil {
			log.Printf("face %s of analysis %d: %v", featureID, analysisID, err)
			return ""
		}
		return uri
	}
}

// thumbnailURI scales an image down to height (never up) and returns it as a JPEG data URI.
func thumbnailURI(data []byte, height int) (template.URL, error) {
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	b := src.Bounds()
	if b.Dy() > height {
		// Box filter: each output pixel averages the source pixels it covers.
		width := max(1, b.Dx()*height/b.Dy())
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		for y := 0; y < height; y++ {
			y0, y1 := b.Min.Y+y*b.Dy()/height, b.Min.Y+(y+1)*b.Dy()/height
			for x := 0; x < width; x++ {
				x0, x1 := b.Min.X+x*b.Dx()/width, b.Min.X+(x+1)*b.Dx()/width
				var r, g, bl, n uint32
				for sy := y0; sy < max(y1, y0+1); sy++ {
					for sx := x0; sx < max(x1, x0+1); sx++ {
						cr, cg, cb, _ := src.At(sx, sy).RGBA()
						r, g, bl, n = r+cr, g+cg, bl+cb, n+1
					}
				}
				i := dst.PixOffset(x, y)
				dst.Pix[i], dst.Pix[i+1], dst.Pix[i+2], dst.Pix[i+3] = uint8(r/n>>8), uint8(g/n>>8), uint8(bl/n>>8), 255
			}
		}
		src = dst
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 82}); err != nil {
		return "", err
	}
	return template.URL("data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())), nil
}

func pct(a, b int) string {
	if b == 0 {
		return "–"
	}
	return fmt.Sprintf("%.0f%%", 100*float64(a)/float64(b))
}

func label(decision string) string {
	switch decision {
	case eval.Positive:
		return "same person"
	case eval.Negative:
		return "different people"
	case eval.Inconclusive:
		return "inconclusive"
	}
	return "–"
}

var debriefPage = template.Must(template.New("debrief").Funcs(template.FuncMap{
	"pct":   pct,
	"label": label,
	"rate":  func(f float64) string { return fmt.Sprintf("%.0f%%", 100*f) },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Review exercise debrief</title>
<style>
:root { --bg:#fbfbfa; --fg:#1d1d1f; --muted:#6b6b70; --line:#e2e2e0; --ok:#1a7f4b; --bad:#b3261e; --warn:#8a5a00; --card:#fff; }
@media (prefers-color-scheme: dark) { :root { --bg:#151517; --fg:#ececee; --muted:#a0a0a8; --line:#2c2c30; --ok:#5fd39a; --bad:#ff8a80; --warn:#f2c46d; --card:#1d1d20; } }
* { box-sizing: border-box; }
body { margin:0; background:var(--bg); color:var(--fg); font:16px/1.5 system-ui, sans-serif; }
main { max-width:1100px; margin:0 auto; padding:32px 16px 64px; }
h1 { font-size:28px; margin:0 0 4px; }
h2 { font-size:20px; margin:40px 0 12px; }
p.meta { color:var(--muted); margin:0; }
.cards { display:grid; grid-template-columns:repeat(auto-fit, minmax(170px, 1fr)); gap:12px; margin-top:24px; }
.card { background:var(--card); border:1px solid var(--line); border-radius:12px; padding:16px; }
.card b { display:block; font-size:30px; font-variant-numeric:tabular-nums; }
.card span { color:var(--muted); font-size:14px; }
.scroll { overflow-x:auto; }
table { border-collapse:collapse; width:100%; background:var(--card); border:1px solid var(--line); border-radius:12px; overflow:hidden; }
th, td { padding:8px 12px; text-align:left; border-bottom:1px solid var(--line); font-variant-numeric:tabular-nums; white-space:nowrap; }
th { font-size:13px; color:var(--muted); font-weight:600; }
tr:last-child td { border-bottom:0; }
.ok { color:var(--ok); font-weight:600; } .bad { color:var(--bad); font-weight:600; } .warn { color:var(--warn); }
.note { color:var(--muted); font-size:14px; margin-top:8px; }
td.faces { display:flex; gap:6px; }
td.faces img { height:80px; border-radius:6px; border:1px solid var(--line); background:var(--line); }
</style>
</head>
<body><main>
<h1>Review exercise debrief</h1>
<p class="meta">{{.World}} · {{.Generated}}</p>

<div class="cards">
  <div class="card"><b>{{.Wrong}}</b><span>errors planted on purpose</span></div>
  <div class="card"><b>{{pct .Caught .Verified}}</b><span>caught by the blind verifier ({{.Caught}} of {{.Verified}} verified)</span></div>
  <div class="card"><b>{{pct .SettledOK .Wrong}}</b><span>ended on the truth ({{.SettledOK}} of {{.Wrong}})</span></div>
  <div class="card"><b>{{.FalseAlarm}}</b><span>right decisions the verifier disputed</span></div>
</div>

<h2>Each examiner</h2>
<div class="scroll"><table>
<tr><th>Code</th><th>Same person: confirmed / inconclusive / rejected</th><th>Different people: confirmed / inconclusive / rejected</th><th>False identifications</th><th>False exclusions</th></tr>
{{range .Students}}<tr><td>{{.Code}}</td>
<td>{{.Genuine.Positive}} / {{.Genuine.Inconclusive}} / {{.Genuine.Negative}}</td>
<td>{{.Impostor.Positive}} / {{.Impostor.Inconclusive}} / {{.Impostor.Negative}}</td>
<td class="{{if gt .Impostor.Positive 0}}bad{{end}}">{{rate .FalseIdentification}}</td>
<td class="{{if gt .Genuine.Negative 0}}warn{{end}}">{{rate .FalseExclusion}}</td></tr>
{{end}}</table></div>
<p class="note">{{.Agreement}}. A false identification calls two people one; a false exclusion splits one person in two.</p>

<h2>The planted errors</h2>
<div class="scroll"><table>
<tr><th>Faces</th><th>Analysis</th><th>Truth</th><th>Planted</th><th>Blind verifier</th><th>Third examiner</th><th>Ended</th></tr>
{{range .Planted}}{{if .Wrong}}<tr>
<td class="faces">{{if .Questioned}}<img src="{{.Questioned}}" alt="questioned face">{{end}}{{if .Reference}}<img src="{{.Reference}}" alt="reference face">{{end}}</td>
<td>{{.CaseID}} #{{.AnalysisID}}</td>
<td>{{label .Right}}</td>
<td class="bad">{{label .Decision}}</td>
<td>{{if .VerifierDecision}}{{call $.Code .Verifier}}: {{label .VerifierDecision}} {{if .Disputed}}<span class="ok">caught</span>{{else}}<span class="bad">missed</span>{{end}}{{else}}not yet{{end}}</td>
<td>{{if .ThirdDecision}}{{call $.Code .Third}}: {{label .ThirdDecision}}{{else}}–{{end}}</td>
<td>{{if .Final}}{{if .SettledRight}}<span class="ok">{{label .Final}}</span>{{else}}<span class="bad">{{label .Final}}</span>{{end}}{{else}}open{{end}}</td>
</tr>{{end}}{{end}}</table></div>

{{if .Planter}}<p class="note">The planting account ({{.Planter.Code}}) also decided {{.Planter.Genuine.Positive}} same-person and {{.Planter.Impostor.Negative}} different-people pairs right, so the verifiers could not tell its work from anyone's.</p>{{end}}

{{if .Stability}}<h2>What the class did to the clusters</h2>
{{range .Stability}}<p>{{.}}</p>{{end}}
<p class="note">A split comes from a rejected link; an unexplained change would break a cluster number someone cited.</p>{{end}}
</main></body></html>
`))
