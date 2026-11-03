package load_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/rodfileto/trackid-sim/load"
	"github.com/rodfileto/trackid-sim/onomastic"
	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/db/migrations"
)

// TestIngestOccurrences loads a generated world without images into a throwaway
// trackid database (TRACKID_TEST_DATABASE_URL, a role allowed to CREATE
// DATABASE; skipped when unset): every occurrence arrives with its location and
// type, bank occurrences share their branch's location, every case is linked
// to its occurrence, and no ground truth is written.
func TestIngestOccurrences(t *testing.T) {
	adminURL := os.Getenv("TRACKID_TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TRACKID_TEST_DATABASE_URL not set")
	}
	admin, err := sql.Open("pgx", adminURL)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	name := fmt.Sprintf("trackid_sim_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE DATABASE ` + name); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(`DROP DATABASE ` + name + ` WITH (FORCE)`)
	u, _ := url.Parse(adminURL)
	u.Path = "/" + name
	sqlDB, err := sql.Open("pgx", u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	goose.SetBaseFS(migrations.FS)
	goose.SetLogger(goose.NopLogger())
	if err := goose.SetDialect("postgres"); err != nil {
		t.Fatal(err)
	}
	if err := goose.Up(sqlDB, "."); err != nil {
		t.Fatal(err)
	}

	w, err := world.Generate(world.DefaultConfig(), onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := load.Ingest(ctx, sqlDB, w, nil, nil); err != nil {
		t.Fatal(err)
	}

	locations := map[string]bool{}
	for _, o := range w.Occurrences {
		locations[o.LocationID] = true
	}
	var nOcc, nLoc, nTyped, nCases, nLinked, nCivilLinked int
	q := func(sql string, dst *int) {
		t.Helper()
		if err := sqlDB.QueryRowContext(ctx, sql).Scan(dst); err != nil {
			t.Fatal(err)
		}
	}
	q(`SELECT COUNT(*) FROM occurrences`, &nOcc)
	q(`SELECT COUNT(*) FROM locations`, &nLoc)
	q(`SELECT COUNT(DISTINCT occurrence_id) FROM occurrence_classifications`, &nTyped)
	q(`SELECT COUNT(*) FROM biometric_cases`, &nCases)
	q(`SELECT COUNT(*) FROM biometric_cases WHERE occurrence_id IS NOT NULL`, &nLinked)
	q(`SELECT COUNT(*) FROM biometric_cases b JOIN occurrence_classifications c ON c.occurrence_id = b.occurrence_id
	   JOIN occurrence_types t ON t.code = c.type_code WHERE b.case_type = 'CIVIL' AND t.case_type = 'CIVIL'`, &nCivilLinked)
	if nOcc != len(w.Occurrences) || nTyped != nOcc || nLoc != len(locations) {
		t.Errorf("occurrences %d (typed %d), locations %d; want %d occurrences, %d locations", nOcc, nTyped, nLoc, len(w.Occurrences), len(locations))
	}
	civil := 0
	for _, c := range w.Cases {
		if c.CaseType == "CIVIL" {
			civil++
		}
	}
	if nCases != len(w.Cases) || nLinked != nCases || nCivilLinked != civil {
		t.Errorf("cases %d, linked %d, civil linked to civil types %d; want %d, all, %d", nCases, nLinked, nCivilLinked, len(w.Cases), civil)
	}
	// Each case answers one request, which cites its occurrence; loading again adds none, and the
	// automatic review answers it.
	if err := load.Ingest(ctx, sqlDB, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	var nRequests, nCiting, nUnanswered int
	q(`SELECT COUNT(*) FROM case_requests`, &nRequests)
	q(`SELECT COUNT(*) FROM case_requests WHERE occurrence_id IS NOT NULL`, &nCiting)
	q(`SELECT COUNT(*) FROM case_analyses WHERE case_request_id IS NULL`, &nUnanswered)
	if nRequests != len(w.Cases) || nCiting != nRequests || nUnanswered != 0 {
		t.Errorf("requests %d (citing an occurrence %d), analyses answering none %d; want %d, all, 0", nRequests, nCiting, nUnanswered, len(w.Cases))
	}
	// Inquiries: an occurrence with no BO is registered by its own; every request cites the
	// inquiry its occurrence's requests come from.
	var wantByInquiry, wantCitingInquiry, nByInquiry, nCitingInquiry int
	cited := map[string]bool{}
	for _, o := range w.Occurrences {
		if o.Number == "" {
			wantByInquiry++
		}
		if o.Inquiry != "" || o.RequestInquiry != "" {
			cited[o.ID] = true
		}
	}
	for _, c := range w.Cases {
		if cited[c.OccurrenceID] {
			wantCitingInquiry++
		}
	}
	q(`SELECT COUNT(*) FROM occurrences WHERE occurrence_number IS NULL AND inquiry_number IS NOT NULL`, &nByInquiry)
	q(`SELECT COUNT(*) FROM case_requests WHERE cited_inquiry_number IS NOT NULL`, &nCitingInquiry)
	if wantByInquiry == 0 || nByInquiry != wantByInquiry || nCitingInquiry != wantCitingInquiry {
		t.Errorf("occurrences registered by an inquiry %d, requests citing one %d; want %d (some), %d",
			nByInquiry, nCitingInquiry, wantByInquiry, wantCitingInquiry)
	}
	// Spatial: every point is in Rio's bounding box (the PostGIS round trip).
	var outside int
	q(`SELECT COUNT(*) FROM locations WHERE NOT ST_Intersects(geom, ST_MakeEnvelope(-43.8, -23.1, -43.0, -22.7, 4326)::geography)`, &outside)
	if outside != 0 {
		t.Errorf("%d locations outside Rio", outside)
	}
}
