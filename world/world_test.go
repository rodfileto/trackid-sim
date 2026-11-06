package world

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rodfileto/trackid-sim/onomastic"
)

func small() Config {
	c := DefaultConfig()
	c.Humans = 800
	return c
}

func TestGenerateIsDeterministic(t *testing.T) {
	a, err := Generate(small(), onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := Generate(small(), onomastic.SeedLexicon())
	if !reflect.DeepEqual(a, b) {
		t.Fatal("same config produced different worlds")
	}
	c := small()
	c.Seed = 2
	other, _ := Generate(c, onomastic.SeedLexicon())
	if reflect.DeepEqual(a.Humans, other.Humans) {
		t.Fatal("different seeds produced identical humans")
	}
}

func TestInvariants(t *testing.T) {
	w, err := Generate(small(), onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	humans := map[string]Human{}
	for _, h := range w.Humans {
		humans[h.ID] = h
	}
	if len(humans) != 800 {
		t.Fatalf("want 800 unique humans, got %d", len(humans))
	}

	docs, regs, persons := map[string]bool{}, map[string]bool{}, map[string]string{}
	for _, e := range w.Enrollments {
		if _, ok := humans[e.HumanID]; !ok {
			t.Fatalf("enrollment for unknown human %s", e.HumanID)
		}
		if docs[e.DocumentNumber] || regs[e.RegisterNumber] {
			t.Fatalf("duplicate document/register number: %s / %s", e.DocumentNumber, e.RegisterNumber)
		}
		docs[e.DocumentNumber], regs[e.RegisterNumber] = true, true
		if !strings.HasPrefix(e.DocumentNumber, "SIM-") || !strings.HasPrefix(e.RegisterNumber, "SIM-") {
			t.Fatalf("synthetic ids must be visibly synthetic: %s %s", e.DocumentNumber, e.RegisterNumber)
		}
		if e.FiscalNumber != "" && !strings.HasPrefix(e.FiscalNumber, "SIM-CPF-") {
			t.Fatalf("fiscal number must not look like a real CPF: %s", e.FiscalNumber)
		}
		if len(e.Files) == 0 {
			t.Fatalf("enrollment %s has no files", e.RegisterNumber)
		}
		// One person_id belongs to exactly one human, except for a double,
		// who is filed under the human they impersonate.
		if e.Kind == KindImpostor {
			continue
		}
		if h, seen := persons[e.PersonID]; seen && h != e.HumanID {
			t.Fatalf("person %s shared by humans %s and %s", e.PersonID, h, e.HumanID)
		}
		persons[e.PersonID] = e.HumanID
	}

	// Double identities: A holds one document copying B's recorded data exactly, with A's biometrics, on a
	// person of its own. It is a driver's licence or an RG from another state; some carry B's fiscal number, and
	// some A have no other record. Every record in a pair carries both biometrics, and nobody is in two pairs.
	perHuman := map[string][]Enrollment{}
	for _, e := range w.Enrollments {
		perHuman[e.HumanID] = append(perHuman[e.HumanID], e)
	}
	both := func(e Enrollment) bool { return len(e.Files) == 2 }
	doubles, offenders := 0, 0
	licences, otherState, withFiscal, withOwnRecord := 0, 0, 0, 0
	inPair := map[string]bool{}
	fakePersons := map[string]bool{}
	for _, a := range w.Humans {
		if a.Impersonates == "" {
			continue
		}
		doubles++
		if a.Offender {
			offenders++
		}
		b := humans[a.Impersonates]
		if inPair[a.ID] || inPair[b.ID] || b.Impersonates != "" {
			t.Fatalf("%s or %s is in two pairs", a.ID, b.ID)
		}
		inPair[a.ID], inPair[b.ID] = true, true
		if a.Sex != b.Sex {
			t.Fatalf("%s holds the identity of %s, of another sex", a.ID, b.ID)
		}
		var own, fake []Enrollment
		for _, e := range perHuman[a.ID] {
			if e.Kind == KindImpostor {
				fake = append(fake, e)
			} else {
				own = append(own, e)
			}
		}
		bPrimary := perHuman[b.ID][0]
		if len(fake) != 1 {
			t.Fatalf("%s: want one fake document, got %+v", a.ID, perHuman[a.ID])
		}
		f := fake[0]
		if f.PersonID == bPrimary.PersonID || fakePersons[f.PersonID] {
			t.Fatalf("%s: a fake document needs a person of its own: %s", a.ID, f.PersonID)
		}
		fakePersons[f.PersonID] = true
		if persons[f.PersonID] != "" {
			t.Fatalf("%s: person %s is also a genuine one", a.ID, f.PersonID)
		}
		if f.Name != bPrimary.Name || f.BirthDate != bPrimary.BirthDate || f.Parent1Name != bPrimary.Parent1Name || f.Parent2Name != bPrimary.Parent2Name {
			t.Fatalf("%s: document %s does not copy %s exactly: %+v, %+v", a.ID, f.RegisterNumber, b.ID, f, bPrimary)
		}
		switch {
		case f.DocumentType == "CNH":
			licences++
		case f.DocumentType == "RG" && strings.HasPrefix(f.DocumentNumber, "SIM-RG-SP-"):
			otherState++
		default:
			t.Fatalf("%s: unexpected fake document %s %s", a.ID, f.DocumentType, f.DocumentNumber)
		}
		switch f.FiscalNumber {
		case bPrimary.FiscalNumber:
			withFiscal++
		case "":
		default:
			t.Fatalf("%s: fake document has a fiscal number of its own: %s", a.ID, f.FiscalNumber)
		}
		if !both(f) || !both(bPrimary) {
			t.Fatalf("%s/%s: every record in a pair needs photo and nist", a.ID, b.ID)
		}
		if len(own) > 0 {
			withOwnRecord++
			if own[0].Kind != KindPrimary || !both(own[0]) || own[0].PersonID == bPrimary.PersonID {
				t.Fatalf("%s: own record %+v", a.ID, own[0])
			}
		}
	}
	for label, n := range map[string]int{"licences": licences, "RGs from another state": otherState, "with the fiscal number": withFiscal,
		"without it": doubles - withFiscal, "fraudsters with a record": withOwnRecord, "fraudsters without one": doubles - withOwnRecord} {
		if doubles > 0 && n == 0 {
			t.Errorf("the mix has no %s among %d", label, doubles)
		}
	}
	// No typos: every record holds its human's data exactly, and a genuine record is its own human's.
	for _, e := range w.Enrollments {
		h := humans[e.HumanID]
		if e.Kind != KindImpostor && (e.Name != h.Name || e.BirthDate != h.BirthDate || e.Parent1Name != h.Mother) {
			t.Fatalf("%s is not exact: %+v, %+v", e.RegisterNumber, e, h)
		}
	}
	if offenders == 0 {
		t.Fatal("impostors should be drawn from offenders first")
	}
	if doubles != 40 {
		t.Fatalf("want 40 doubles, got %d", doubles)
	}

	ids := map[string]bool{}
	for _, c := range w.Cases {
		if ids[c.CaseID] {
			t.Fatalf("duplicate case id %s", c.CaseID)
		}
		ids[c.CaseID] = true
		if c.CaseType != "CRIMINAL" && c.CaseType != "CIVIL" {
			t.Fatalf("bad case type %q", c.CaseType)
		}
		if c.Modality != "FACIAL" && c.Modality != "FINGERPRINT" {
			t.Fatalf("bad modality %q", c.Modality)
		}
		if len(c.Evidences) == 0 {
			t.Fatalf("case %s has no evidence", c.CaseID)
		}
		for _, ev := range c.Evidences {
			if (c.Modality == "FACIAL") != (ev.Capture != nil) {
				t.Fatalf("case %s evidence %d: modality %s, capture %v", c.CaseID, ev.Sequence, c.Modality, ev.Capture)
			}
			if c.Modality == "FACIAL" {
				seen := map[string]bool{}
				for _, tr := range ev.Traces {
					if seen[tr.HumanID] {
						t.Fatalf("case %s evidence %d: %s twice in one frame", c.CaseID, ev.Sequence, tr.HumanID)
					}
					seen[tr.HumanID] = true
				}
			}
		}
		for _, ev := range c.Evidences {
			if len(ev.Traces) == 0 {
				t.Fatalf("case %s evidence %d has no traces", c.CaseID, ev.Sequence)
			}
			for _, tr := range ev.Traces {
				if _, ok := humans[tr.HumanID]; !ok {
					t.Fatalf("trace for unknown human %s", tr.HumanID)
				}
			}
		}
	}
}

func TestPopulationShape(t *testing.T) {
	c := DefaultConfig()
	c.Humans = 5000 // large enough for stable statistics
	w, err := Generate(c, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	s := w.Summary()
	t.Logf("%+v", s)

	if s.Duplicates == 0 || s.Renewals == 0 {
		t.Fatalf("expected duplicate and renewal enrollments, got %+v", s)
	}
	if s.UnenrolledOffenders == 0 {
		t.Fatal("expected some offenders with no enrollment (unknown-subject clusters)")
	}
	if s.BystanderTraces == 0 {
		t.Fatal("expected some bystander traces")
	}
	if s.FacialCases == 0 || s.FingerprintCases == 0 {
		t.Fatalf("expected both modalities, got %+v", s)
	}

	// Heavy tail: the busiest offender is involved in far more events than the median.
	perHuman := map[string]int{}
	for _, cs := range w.Cases {
		seen := map[string]bool{}
		for _, ev := range cs.Evidences {
			for _, tr := range ev.Traces {
				if !tr.Bystander && !seen[tr.HumanID] {
					seen[tr.HumanID] = true
					perHuman[tr.HumanID]++
				}
			}
		}
	}
	maxCases := 0
	for _, n := range perHuman {
		maxCases = max(maxCases, n)
	}
	if maxCases < 8 {
		t.Fatalf("expected a heavy tail of prolific offenders, max cases per human = %d", maxCases)
	}
}

func TestValidate(t *testing.T) {
	c := DefaultConfig()
	c.Humans = 1
	if _, err := Generate(c, onomastic.SeedLexicon()); err == nil {
		t.Fatal("expected error for Humans < 2")
	}
}

func TestSummaryCountsSharedNames(t *testing.T) {
	w := &World{Humans: []Human{
		{ID: "H1", Name: "Maria Silva"},
		{ID: "H2", Name: "Maria Silva"},
		{ID: "H3", Name: "Maria Silva"},
		{ID: "H4", Name: "Jose Santos"},
		{ID: "H5", Name: "Jose Santos"},
		{ID: "H6", Name: "Ana Lima"},
	}}
	s := w.Summary()
	if s.SharedNameHumans != 5 || s.SharedNames != 2 {
		t.Fatalf("SharedNameHumans=%d SharedNames=%d, want 5 and 2", s.SharedNameHumans, s.SharedNames)
	}
}

// The embedded Censo table is a copy of the one the face pool was drawn from;
// if the two drift, world ages and face ages stop describing the same people.
func TestCensoTableMatchesFacegen(t *testing.T) {
	var world, facegen struct {
		Sex map[string]float64 `json:"sex"`
		Age map[string]float64 `json:"age"`
	}
	src, err := os.ReadFile("../tools/facegen/censo2022_shares.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(src, &facegen); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(censoJSON, &world); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(world, facegen) {
		t.Fatal("world/data/censo2022_adults.json has drifted from tools/facegen/censo2022_shares.json")
	}
}

// Everyone is an adult on every day of the simulated period, and ages follow
// the Censo table: each 10-year band's realised share is within 1.5 points of
// the table's. The tolerance is absolute, not a standard error, because
// sibling spacing and truncation at the range ends shift the marginal a little
// by design (see siblingAges).
func TestAgesAreAdultAndFollowCenso(t *testing.T) {
	c := DefaultConfig()
	c.Humans = 50000
	w, err := Generate(c, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	band := func(age int) int { return (age - censo.min()) / 10 }
	want := map[int]float64{}
	prev := 0.0
	for i, a := range censo.ages {
		want[band(a)] += censo.cum[i] - prev
		prev = censo.cum[i]
	}
	got := map[int]float64{}
	female := 0
	for _, h := range w.Humans {
		born, err := time.Parse("2006-01-02", h.BirthDate)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Date(c.YearFrom, 1, 1, 0, 0, 0, 0, time.UTC)
		if start.Before(born.AddDate(censo.min(), 0, 0)) {
			t.Fatalf("%s born %s is a minor on %s", h.ID, h.BirthDate, start.Format("2006-01-02"))
		}
		age := c.YearFrom - 1 - born.Year()
		if age > censo.max() {
			t.Fatalf("%s aged %d is above the table's range", h.ID, age)
		}
		got[band(age)] += 1 / float64(len(w.Humans))
		if h.Sex == onomastic.Female {
			female++
		}
	}
	for b := range band(censo.max()) + 1 {
		share := want[b]
		lo := censo.min() + 10*b
		t.Logf("ages %d-%d: censo %.1f%%, world %.1f%%", lo, lo+9, 100*share, 100*got[b])
		if math.Abs(got[b]-share) > 0.015 {
			t.Errorf("ages %d-%d: world %.1f%% vs censo %.1f%%", lo, lo+9, 100*got[b], 100*share)
		}
	}
	if f := float64(female) / float64(len(w.Humans)); math.Abs(f-censo.femaleRate) > 0.01 {
		t.Errorf("female share %.3f vs censo %.3f", f, censo.femaleRate)
	}
}
