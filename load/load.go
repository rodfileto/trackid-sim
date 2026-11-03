// Package load writes a generated world into a trackid database through trackid's
// own extension contracts (identity.Ingest, cases.Ingest), exactly as an
// organization's import command would. It writes no decisions: faces go through
// trackid's own pipeline (embedding, matching, clustering) via its queue, and
// human review is left to the people using the demo. Ground truth is never
// written; it stays in the world file.
package load

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/rodfileto/trackid-sim/eval"
	"github.com/rodfileto/trackid-sim/media"
	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/cases"
	"github.com/rodfileto/trackid/db"
	"github.com/rodfileto/trackid/embedding"
	"github.com/rodfileto/trackid/graph"
	"github.com/rodfileto/trackid/identity"
	"github.com/rodfileto/trackid/occurrences"
	"github.com/rodfileto/trackid/requests"
	"github.com/rodfileto/trackid/storage"
)

// ErrNotEmpty is returned when the target database holds data this simulator did
// not write. Loading is refused so synthetic rows are never mixed into real ones.
var ErrNotEmpty = errors.New("load: database contains non-simulated data")

// CheckSafe fails with ErrNotEmpty if person or biometric_cases holds any row whose
// id does not carry the simulator's "SIM-" prefix.
func CheckSafe(ctx context.Context, sqlDB *sql.DB) error {
	var persons, cases int
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM persons WHERE person_id NOT LIKE 'SIM-%'`).Scan(&persons); err != nil {
		return err
	}
	if err := sqlDB.QueryRowContext(ctx, `SELECT count(*) FROM biometric_cases WHERE case_id NOT LIKE 'SIM-%'`).Scan(&cases); err != nil {
		return err
	}
	if persons+cases > 0 {
		return fmt.Errorf("%w (%d persons, %d cases); refusing to continue", ErrNotEmpty, persons, cases)
	}
	return nil
}

// Media is where load gets images and puts them. With it, enrollment photos
// are the humans' base faces, NIST files rolled impressions of their finger,
// facial evidences rendered CCTV frames and fingerprint evidences lift cards,
// each with a box per trace, uploaded through trackid's own helpers (which own
// the object layout). Without it, every file is a sim:// placeholder that
// resolves to nothing.
type Media struct {
	Store  *storage.Client
	Pool   *media.Pool
	Prints *media.Prints
	// Queue, when set, is trackid's task queue: loaded faces are enqueued for
	// embedding and prints for template extraction, which drives matching and
	// clustering in trackid's worker.
	Queue embedding.Enqueuer
}

// Ingest writes every enrollment and case of w. Both Ingest calls are upserts and
// uploads are content-addressed, so re-running against the same world is safe.
func Ingest(ctx context.Context, sqlDB *sql.DB, w *world.World, m *Media, logf func(string, ...any)) error {
	humans := humansByID(w)
	if m != nil {
		ids := PrintedHumans(w)
		if logf != nil {
			logf("  prints: drawing %d fingers ...", len(ids))
		}
		m.Prints.Warm(ids)
	}
	for i, e := range w.Enrollments {
		in, err := prepareEnrollment(m, humans, e)
		if err != nil {
			return err
		}
		if err := in.ingest(ctx, sqlDB, m); err != nil {
			return err
		}
		if logf != nil && (i+1)%1000 == 0 {
			logf("  identity: %d/%d enrollments", i+1, len(w.Enrollments))
		}
	}
	occ, err := ingestOccurrences(ctx, sqlDB, w, logf)
	if err != nil {
		return err
	}
	for i, c := range w.Cases {
		in, err := prepareCase(m, w, humans, c)
		if err != nil {
			return err
		}
		if err := in.ingest(ctx, sqlDB, m, occ); err != nil {
			return err
		}
		if logf != nil && (i+1)%1000 == 0 {
			logf("  cases: %d/%d", i+1, len(w.Cases))
		}
	}
	return nil
}

func humansByID(w *world.World) map[string]world.Human {
	humans := make(map[string]world.Human, len(w.Humans))
	for _, h := range w.Humans {
		humans[h.ID] = h
	}
	return humans
}

// rendered is one file drawn for upload: its bytes and the name it is recorded under. A nil
// data is a sim:// placeholder (load without media).
type rendered struct {
	data []byte
	name string
}

// enrollmentMaterial is one enrollment with its files drawn, ready to upload and ingest.
type enrollmentMaterial struct {
	e     world.Enrollment
	files []rendered // parallel to e.Files
}

// prepareEnrollment draws an enrollment's files: the photo is the base face of the human
// actually enrolled -- for an impostor record, that is the impostor, not the person it is
// filed under -- and the NIST file a rolled impression of that human's finger.
func prepareEnrollment(m *Media, humans map[string]world.Human, e world.Enrollment) (enrollmentMaterial, error) {
	out := enrollmentMaterial{e: e, files: make([]rendered, len(e.Files))}
	if m == nil {
		return out, nil
	}
	for i, f := range e.Files {
		switch {
		case f.FileType == "photo" && humans[e.HumanID].Face != "":
			data, path, err := m.Pool.Photo(humans[e.HumanID].Face)
			if err != nil {
				return out, fmt.Errorf("enrollment %s: %w", e.RegisterNumber, err)
			}
			out.files[i] = rendered{data, "sim:" + filepath.Base(path)}
		case f.FileType == "nist":
			data, err := m.Prints.Rolled(e.HumanID, e.RegisterNumber, f.Sequence)
			if err != nil {
				return out, fmt.Errorf("enrollment %s: %w", e.RegisterNumber, err)
			}
			out.files[i] = rendered{data, fmt.Sprintf("sim:%s_nist_%d.png", e.RegisterNumber, f.Sequence)}
		}
	}
	return out, nil
}

// ingest uploads the enrollment's files and writes it through identity.Ingest.
func (in enrollmentMaterial) ingest(ctx context.Context, sqlDB *sql.DB, m *Media) error {
	e := in.e
	en := identity.Enrollment{
		PersonID:       e.PersonID,
		DocumentType:   e.DocumentType,
		DocumentNumber: e.DocumentNumber,
		FiscalNumber:   e.FiscalNumber,
		RegisterNumber: e.RegisterNumber,
		Name:           e.Name,
		Parent1Name:    e.Parent1Name,
		Parent1Gender:  e.Parent1Gender,
		Parent2Name:    e.Parent2Name,
		Parent2Gender:  e.Parent2Gender,
		BirthDate:      e.BirthDate,
	}
	for i, f := range e.Files {
		if r := in.files[i]; r.data != nil {
			fin, err := identity.UploadFile(ctx, m.Store, e.RegisterNumber, f.FileType, f.Sequence, r.data)
			if err != nil {
				return fmt.Errorf("enrollment %s: %w", e.RegisterNumber, err)
			}
			fin.SourcePath = r.name
			en.Files = append(en.Files, fin)
			continue
		}
		// Without media: a placeholder that resolves to nothing, so downloads 404.
		ref := fmt.Sprintf("sim://%s/%s/%s/%d", e.PersonID, e.RegisterNumber, f.FileType, f.Sequence)
		en.Files = append(en.Files, identity.FileInput{
			FileType: f.FileType, Sequence: f.Sequence, SourcePath: ref, StorageRef: ref,
		})
	}
	var opts []identity.Option
	if m != nil && m.Queue != nil {
		opts = append(opts, identity.WithEnqueuer(m.Queue))
	}
	if _, err := identity.Ingest(ctx, sqlDB, en, opts...); err != nil {
		return fmt.Errorf("enrollment %s: %w", e.RegisterNumber, err)
	}
	return nil
}

// ingestOccurrences writes every occurrence of w, which cases link to, and returns them by id.
// Ground truth (offenders, the unknown person) stays out.
func ingestOccurrences(ctx context.Context, sqlDB *sql.DB, w *world.World, logf func(string, ...any)) (map[string]world.Occurrence, error) {
	occ := make(map[string]world.Occurrence, len(w.Occurrences))
	for i, o := range w.Occurrences {
		occ[o.ID] = o
		in := occurrences.Input{
			Agency: o.Agency, Number: o.Number, Inquiry: o.Inquiry,
			Location: occurrences.LocationInput{
				LocationID: o.LocationID, Name: o.PlaceName, Lon: o.Lon, Lat: o.Lat, Precision: o.Precision,
				MunicipalityCode: world.Municipality, UF: world.UF, Neighbourhood: o.Neighbourhood, PlaceType: o.PlaceType,
			},
			OccurredFrom: o.OccurredFrom, OccurredTo: o.OccurredTo, ReportedAt: &o.RegisteredAt, RegisteredAt: o.RegisteredAt,
			Attempted: o.Attempted, OffenderCount: o.OffenderCount, VictimCount: o.VictimCount,
			Status: o.Status, Description: o.Description, Types: []string{o.Type},
		}
		if o.Number == "" {
			in.Agency = "" // registered by its inquiry: no BO, keyed by the inquiry
		}
		if _, err := occurrences.Ingest(ctx, sqlDB, in); err != nil {
			return nil, fmt.Errorf("occurrence %s: %w", o.ID, err)
		}
		if logf != nil && (i+1)%1000 == 0 {
			logf("  occurrences: %d/%d", i+1, len(w.Occurrences))
		}
	}
	return occ, nil
}

// caseMaterial is one case with its evidence drawn, ready to upload and ingest.
type caseMaterial struct {
	c         world.Case
	evidences []renderedEvidence // parallel to c.Evidences
}

type renderedEvidence struct {
	rendered
	boxes []*cases.Box // one per trace, in trace order; nil without a file
}

// prepareCase draws a case's evidence: a CCTV frame for facial evidence, a lift card for
// fingerprint evidence, each with a box per trace.
func prepareCase(m *Media, w *world.World, humans map[string]world.Human, c world.Case) (caseMaterial, error) {
	out := caseMaterial{c: c, evidences: make([]renderedEvidence, len(c.Evidences))}
	if m == nil {
		return out, nil
	}
	for i, ev := range c.Evidences {
		if ev.Capture == nil && c.Modality != "FINGERPRINT" {
			continue
		}
		r, err := renderEvidence(m, w, humans, c, ev)
		if err != nil {
			return out, fmt.Errorf("case %s evidence %d: %w", c.CaseID, ev.Sequence, err)
		}
		out.evidences[i] = r
	}
	return out, nil
}

// ingest uploads the case's evidence, writes it through cases.Ingest and registers the
// request it answers.
func (in caseMaterial) ingest(ctx context.Context, sqlDB *sql.DB, m *Media, occ map[string]world.Occurrence) error {
	c := in.c
	traceType, ok := graph.TraceTypeForModality(c.Modality)
	if !ok {
		return fmt.Errorf("case %s: unsupported modality %q", c.CaseID, c.Modality)
	}
	codType, _ := graph.CodificationTypeForTraceType(traceType)
	cin := cases.CaseInput{CaseID: c.CaseID, CaseType: c.CaseType, Modality: c.Modality, Description: c.Description}
	if o, ok := occ[c.OccurrenceID]; ok {
		cin.Occurrence = &cases.OccurrenceRef{Agency: o.Agency, Number: o.Number, Inquiry: o.Inquiry}
	}
	for k, ev := range c.Evidences {
		ein := cases.EvidenceInput{Sequence: ev.Sequence, Description: ev.Description}
		r := in.evidences[k]
		if r.data != nil {
			file, err := cases.UploadEvidenceFile(ctx, m.Store, c.CaseID, r.data, r.name)
			if err != nil {
				return fmt.Errorf("case %s evidence %d: %w", c.CaseID, ev.Sequence, err)
			}
			ein.File = &file
		}
		for i, t := range ev.Traces {
			tin := cases.TraceInput{
				Sequence:      t.Sequence,
				TraceType:     traceType,
				Codifications: []cases.CodificationInput{{Sequence: 1, CodificationType: codType}},
			}
			if r.boxes != nil {
				tin.Box = r.boxes[i]
			}
			ein.Traces = append(ein.Traces, tin)
		}
		cin.Evidences = append(cin.Evidences, ein)
	}
	var opts []cases.Option
	if m != nil && m.Queue != nil {
		opts = append(opts, cases.WithEnqueuer(m.Queue))
	}
	if _, err := cases.Ingest(ctx, sqlDB, cin, opts...); err != nil {
		return fmt.Errorf("case %s: %w", c.CaseID, err)
	}
	if err := ensureRequest(ctx, sqlDB, c, occ[c.OccurrenceID]); err != nil {
		return fmt.Errorf("case %s request: %w", c.CaseID, err)
	}
	return nil
}

// renderEvidence renders one evidence and returns it with each trace's box, in trace order: a
// CCTV frame for facial evidence, a lift card for fingerprint evidence. The box is where the
// figure's face or the latent was placed -- ground truth, not a detection, so it carries no
// detection score.
func renderEvidence(m *Media, w *world.World, humans map[string]world.Human, c world.Case, ev world.Evidence) (renderedEvidence, error) {
	var (
		data   []byte
		placed []media.Box
		name   string
		err    error
	)
	if c.Modality == "FINGERPRINT" {
		data, placed, err = m.Prints.LiftCard(c, ev, humans)
		name = fmt.Sprintf("%s_e%d_lift.png", c.CaseID, ev.Sequence)
	} else {
		figs := make([]media.Figure, len(ev.Traces))
		for i, t := range ev.Traces {
			f, err := m.Pool.Figure(humans[t.HumanID].Variant)
			if err != nil {
				return renderedEvidence{}, fmt.Errorf("trace %d: %w", t.Sequence, err)
			}
			figs[i] = f
		}
		var ps []media.Placement
		o, ok := w.OccurrenceOf(c)
		if !ok {
			return renderedEvidence{}, fmt.Errorf("case %s: occurrence %s is not in the world", c.CaseID, c.OccurrenceID)
		}
		data, ps, err = media.Evidence(w.Config.Seed, c, ev, figs, o.OccurredFrom, o.OccurredTo)
		for _, p := range ps {
			placed = append(placed, p.Box)
		}
		name = fmt.Sprintf("%s_e%d_%s.jpg", c.CaseID, ev.Sequence, ev.Capture.Level)
	}
	if err != nil {
		return renderedEvidence{}, err
	}
	boxes := make([]*cases.Box, len(placed))
	for i, b := range placed {
		boxes[i] = &cases.Box{X1: float64(b.X1), Y1: float64(b.Y1), X2: float64(b.X2), Y2: float64(b.Y2)}
	}
	return renderedEvidence{rendered{data, name}, boxes}, nil
}

// PrintedHumans lists, once each and in world order, the humans load draws a
// finger for: those with a NIST file on any record, and those who left a lift.
func PrintedHumans(w *world.World) []string {
	need := map[string]bool{}
	for _, e := range w.Enrollments {
		for _, f := range e.Files {
			if f.FileType == "nist" {
				need[e.HumanID] = true
			}
		}
	}
	for _, c := range w.Cases {
		if c.Modality != "FINGERPRINT" {
			continue
		}
		for _, ev := range c.Evidences {
			for _, t := range ev.Traces {
				need[t.HumanID] = true
			}
		}
	}
	var ids []string
	for _, h := range w.Humans {
		if need[h.ID] {
			ids = append(ids, h.ID)
		}
	}
	return ids
}

// modalityFor maps a case's (or a feature's) modality to the decision modality.
func modalityFor(modality string) string {
	if modality == "FACIAL" {
		return eval.Face
	}
	return eval.Fingerprint
}

// Features reads back every simulated biometric feature with its graph feature id
// and ground-truth human, by joining the database rows to the world. It works on
// any database previously loaded from w, so evaluation needs no in-memory state.
func Features(ctx context.Context, sqlDB *sql.DB, w *world.World) ([]eval.Feature, error) {
	humanByRegister := make(map[string]string, len(w.Enrollments))
	for _, e := range w.Enrollments {
		humanByRegister[e.RegisterNumber] = e.HumanID
	}
	type traceKey struct {
		caseID   string
		evidence int16
		trace    int16
	}
	humanByTrace := map[traceKey]string{}
	for _, c := range w.Cases {
		for _, ev := range c.Evidences {
			for _, t := range ev.Traces {
				humanByTrace[traceKey{c.CaseID, ev.Sequence, t.Sequence}] = t.HumanID
			}
		}
	}

	var out []eval.Feature

	rows, err := sqlDB.QueryContext(ctx, `
		SELECT f.id, r.register_number, f.file_type
		FROM identity_files f JOIN identity_registers r ON r.id = f.register_id
		JOIN biometric_features b ON b.identity_file_id = f.id
		WHERE r.register_number LIKE 'SIM-%' ORDER BY f.id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id int64
		var register, fileType string
		if err := rows.Scan(&id, &register, &fileType); err != nil {
			rows.Close()
			return nil, err
		}
		human, ok := humanByRegister[register]
		if !ok {
			rows.Close()
			return nil, fmt.Errorf("register %s is in the database but not in the world file", register)
		}
		featureType, _ := graph.FeatureTypeForFileType(fileType)
		modality, _ := graph.ModalityForFeatureType(featureType)
		out = append(out, eval.Feature{ID: graph.KnownFeatureID(id), HumanID: human, Modality: modalityFor(modality)})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}

	rows, err = sqlDB.QueryContext(ctx, `
		SELECT t.id, c.case_id, c.modality, e.sequence, t.sequence
		FROM case_traces t JOIN case_evidences e ON e.id = t.evidence_id
		JOIN biometric_cases c ON c.id = e.biometric_case_id
		JOIN biometric_features b ON b.case_trace_id = t.id
		WHERE c.case_id LIKE 'SIM-%' ORDER BY t.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var caseID, modality string
		var evSeq, trSeq int16
		if err := rows.Scan(&id, &caseID, &modality, &evSeq, &trSeq); err != nil {
			return nil, err
		}
		human, ok := humanByTrace[traceKey{caseID, evSeq, trSeq}]
		if !ok {
			return nil, fmt.Errorf("trace %s/%d/%d is in the database but not in the world file", caseID, evSeq, trSeq)
		}
		out = append(out, eval.Feature{ID: graph.QuestionedFeatureID(id), HumanID: human, Modality: modalityFor(modality)})
	}
	return out, rows.Err()
}

// IsQuestioned reports whether a graph feature id belongs to a case trace.
func IsQuestioned(featureID string) bool { return strings.HasPrefix(featureID, "TRACE:") }

// ensureRequest registers the request a case answers, unless it has one: the occurrence's police
// station asking, by a visibly synthetic ofício, received two days after the occurrence was
// registered, due thirty days later, sending the case's evidence files, asking the standard
// questions of its nature and naming its task (a facial review, or a fingerprint comparison), and
// citing the occurrence's BO and the inquiry it comes from (the occurrence's own, or another:
// world.Occurrence.RequestInquiry). It is derived from the case at load time, not part of
// world.json: the world holds no new rule here, only the paperwork trackid needs to write reports.
func ensureRequest(ctx context.Context, sqlDB *sql.DB, c world.Case, o world.Occurrence) error {
	existing, err := requests.List(ctx, sqlDB, c.CaseID)
	if err != nil || len(existing) > 0 {
		return err
	}
	q := db.New(sqlDB)
	files, err := q.ListRequestFiles(ctx, c.CaseID)
	if err != nil {
		return err
	}
	// The request names its occurrence: the cited inquiry may be another than the occurrence's,
	// which trackid cannot resolve from the numbers.
	linked, err := q.GetCaseOccurrenceID(ctx, c.CaseID)
	if err != nil {
		return err
	}
	var occurrenceID *int64
	if linked.OccurrenceID.Valid {
		occurrenceID = &linked.OccurrenceID.Int64
	}
	var ids []int64
	for _, f := range files {
		ids = append(ids, f.ID)
	}
	received := o.RegisteredAt
	if received.IsZero() {
		received = time.Date(c.Year, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	received = received.AddDate(0, 0, 2)
	authority := "Delegacia " + o.Agency
	if o.Agency == "" {
		authority = "Autoridade requisitante (simulada)"
	}
	questions := []string{"Há indicação da identidade da(s) pessoa(s) registrada(s) no material encaminhado?",
		"Em caso positivo, a(s) pessoa(s) indicada(s) é(são) a(s) mesma(s) do material questionado?"}
	if c.CaseType == "CIVIL" {
		questions = []string{"Qual a identidade da pessoa retratada no material encaminhado?"}
	}
	// The task it asks (trackid's docs/request-routing-plan.md): a facial review of a facial case,
	// a fingerprint comparison of a fingerprint case. The request sends the questioned evidence only;
	// references come up on the case page through matching, and the coordinator adds the pairs.
	task := "FACIAL_REVIEW"
	if c.Modality == "FINGERPRINT" {
		task = "FINGERPRINT_COMPARISON"
	}
	inquiry := o.Inquiry
	if o.RequestInquiry != "" {
		inquiry = o.RequestInquiry
	}
	_, err = requests.Create(ctx, sqlDB, c.CaseID, requests.Input{
		Authority: authority, DocumentReference: "Ofício " + c.CaseID, ReceivedOn: received.Format("2006-01-02"),
		DueOn: received.AddDate(0, 0, 30).Format("2006-01-02"), CitedReference: o.Number, CitedInquiry: inquiry,
		OccurrenceID: occurrenceID, Questions: questions, FileIDs: ids,
		Task: task,
	}, 0)
	return err
}
