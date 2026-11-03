package world

import (
	"testing"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/planar"
	"github.com/rodfileto/trackid-sim/onomastic"
)

// TestOccurrences pins the generation rules of docs/OCCURRENCE.md: one crime
// type per offender, unknown persons kept apart from criminal cases and from
// double identities, every point inside its CISP, bank types at a branch, and
// every case linked to an occurrence of its own nature.
func TestOccurrences(t *testing.T) {
	cfg := small()
	cfg.UnknownPersons = 20
	w, err := Generate(cfg, onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	g, err := loadRio()
	if err != nil {
		t.Fatal(err)
	}
	humans := map[string]Human{}
	for _, h := range w.Humans {
		humans[h.ID] = h
	}
	bankType := map[string]bool{}
	for _, ct := range crimeTypes {
		bankType[ct.Code] = ct.AtBank
	}

	occ := map[string]Occurrence{}
	kinds := map[string]map[string]bool{}
	unknown := map[string]bool{}
	numbers := map[string]bool{}
	registered, fromOther := 0, 0
	for _, o := range w.Occurrences {
		occ[o.ID] = o
		// An occurrence is identified by its BO or, with none, by the inquiry that registers it;
		// inquiries are numbered once, whether an occurrence's own or another its requests cite.
		ids := []string{o.Inquiry, o.RequestInquiry}
		if o.Number != "" {
			ids = append(ids, o.Agency+" "+o.Number)
		} else {
			registered++
			if o.Inquiry == "" || o.CaseType != "CRIMINAL" || o.Status != "INQUIRY_OPENED" && o.Status != "CLOSED" {
				t.Fatalf("%s: no BO, inquiry %q, %s, %s", o.ID, o.Inquiry, o.CaseType, o.Status)
			}
		}
		for _, n := range ids {
			if n == "" {
				continue
			}
			if numbers[n] {
				t.Fatalf("number %s twice", n)
			}
			numbers[n] = true
		}
		if o.RequestInquiry != "" {
			fromOther++
			if o.RequestInquiry == o.Inquiry || o.CaseType != "CRIMINAL" {
				t.Fatalf("%s: requests from %q, own inquiry %q", o.ID, o.RequestInquiry, o.Inquiry)
			}
		}
		i, ok := g.index[o.CISP]
		if !ok || !planar.MultiPolygonContains(g.areas[i].shape, orb.Point{o.Lon, o.Lat}) {
			// A geocoded branch is a real point: it lies in its CISP by construction
			// of bank_branches.json; a generated point must too.
			t.Fatalf("%s: (%v, %v) outside CISP %d", o.ID, o.Lon, o.Lat, o.CISP)
		}
		if bankType[o.Type] != (o.BankBranch != "") {
			t.Fatalf("%s %s: bank branch %q", o.ID, o.Type, o.BankBranch)
		}
		switch o.CaseType {
		case "CRIMINAL":
			if len(o.Offenders) == 0 || o.Person != "" {
				t.Fatalf("%s: criminal with offenders %v, person %q", o.ID, o.Offenders, o.Person)
			}
			for _, h := range o.Offenders {
				if kinds[h] == nil {
					kinds[h] = map[string]bool{}
				}
				kinds[h][o.Type] = true
			}
		case "CIVIL":
			if o.Person == "" || len(o.Offenders) > 0 {
				t.Fatalf("%s: civil with person %q, offenders %v", o.ID, o.Person, o.Offenders)
			}
			unknown[o.Person] = true
		default:
			t.Fatalf("%s: case type %q", o.ID, o.CaseType)
		}
	}
	for h, k := range kinds {
		if len(k) != 1 {
			t.Fatalf("offender %s commits %d kinds of crime: %v", h, len(k), k)
		}
	}
	if registered == 0 || fromOther == 0 {
		t.Fatalf("%d occurrences registered by an inquiry, %d with requests from another: want some of each", registered, fromOther)
	}
	if len(unknown) != cfg.UnknownPersons {
		t.Fatalf("%d unknown persons, want %d", len(unknown), cfg.UnknownPersons)
	}

	records := map[string][]Enrollment{}
	for _, e := range w.Enrollments {
		records[e.HumanID] = append(records[e.HumanID], e)
	}
	enrolledUnknown := 0
	for id := range unknown {
		h := humans[id]
		if h.Offender || h.Impersonates != "" {
			t.Fatalf("unknown person %s: offender %v, impersonates %q", id, h.Offender, h.Impersonates)
		}
		switch rs := records[id]; {
		case len(rs) > 1 || len(rs) == 1 && rs[0].Kind != KindPrimary:
			t.Fatalf("unknown person %s has more than one identity: %+v", id, rs)
		case len(rs) == 1:
			enrolledUnknown++
		}
		for _, other := range w.Humans {
			if other.Impersonates == id {
				t.Fatalf("unknown person %s is impersonated by %s", id, other.ID)
			}
		}
	}
	if enrolledUnknown == 0 || enrolledUnknown == len(unknown) {
		t.Fatalf("%d of %d unknown persons enrolled: want both kinds", enrolledUnknown, len(unknown))
	}

	for _, c := range w.Cases {
		o, ok := occ[c.OccurrenceID]
		if !ok || o.CaseType != c.CaseType {
			t.Fatalf("case %s: occurrence %q (%s) for a %s case", c.CaseID, c.OccurrenceID, o.CaseType, c.CaseType)
		}
		if c.Year != o.OccurredFrom.Year() {
			t.Fatalf("case %s: year %d, occurrence %d", c.CaseID, c.Year, o.OccurredFrom.Year())
		}
		for _, ev := range c.Evidences {
			for _, tr := range ev.Traces {
				if c.CaseType == "CRIMINAL" && unknown[tr.HumanID] {
					t.Fatalf("unknown person %s in criminal case %s", tr.HumanID, c.CaseID)
				}
				if c.CaseType == "CIVIL" && tr.HumanID != o.Person {
					t.Fatalf("civil case %s holds %s, not its person %s", c.CaseID, tr.HumanID, o.Person)
				}
			}
		}
	}
}

func TestApportion(t *testing.T) {
	got := apportion([]float64{14.26, 3.60, 0.02, 82.12}, 32)
	if got[0]+got[1]+got[2]+got[3] != 32 || got[1] != 1 || got[2] != 0 {
		t.Fatalf("apportion: %v", got)
	}
}

// TestRepeatPlaces: occurrences sharing a location share its point, area and
// kind; some offenders return to their own places (habit) and some places are
// hit by different offenders (hotspots, bank branches).
func TestRepeatPlaces(t *testing.T) {
	w, err := Generate(small(), onomastic.SeedLexicon())
	if err != nil {
		t.Fatal(err)
	}
	type where struct {
		lon, lat float64
		cisp     int
		kind     string
	}
	at := map[string]where{}
	leads := map[string]map[string]bool{}
	uses := map[string]int{}
	for _, o := range w.Occurrences {
		x := where{o.Lon, o.Lat, o.CISP, o.PlaceType}
		if prev, ok := at[o.LocationID]; ok && prev != x {
			t.Fatalf("location %s: %+v and %+v", o.LocationID, prev, x)
		}
		at[o.LocationID] = x
		uses[o.LocationID]++
		if o.CaseType == "CRIMINAL" {
			if leads[o.LocationID] == nil {
				leads[o.LocationID] = map[string]bool{}
			}
			leads[o.LocationID][o.Offenders[0]] = true
		}
	}
	habit, shared := 0, 0
	for id, n := range uses {
		if n < 2 {
			continue
		}
		if len(leads[id]) > 1 {
			shared++
		} else {
			habit++
		}
	}
	if habit == 0 || shared == 0 {
		t.Fatalf("repeat places: %d returned to by one offender, %d shared by several; want both", habit, shared)
	}
}
