package world

import (
	"fmt"
	"math/rand/v2"
	"time"
)

// chooseUnknown picks cfg.UnknownPersons humans to be found as unknown persons,
// kept simple: a non-offender, in no double-identity pair, with one identity at
// most -- a share with exactly one (primary) enrollment, so they can be
// identified, the rest never enrolled. It returns indexes into humans.
func chooseUnknown(cfg Config, humans []Human, enrollments []Enrollment, r *rand.Rand) map[int]bool {
	records := map[string]int{}
	for _, e := range enrollments {
		records[e.HumanID]++
	}
	paired := map[string]bool{}
	for _, h := range humans {
		if h.Impersonates != "" {
			paired[h.ID], paired[h.Impersonates] = true, true
		}
	}
	var enrolled, never []int
	for i, h := range humans {
		if h.Offender || paired[h.ID] {
			continue
		}
		switch records[h.ID] {
		case 0:
			never = append(never, i)
		case 1:
			enrolled = append(enrolled, i)
		}
	}
	r.Shuffle(len(enrolled), func(i, j int) { enrolled[i], enrolled[j] = enrolled[j], enrolled[i] })
	r.Shuffle(len(never), func(i, j int) { never[i], never[j] = never[j], never[i] })

	nEnrolled := min(int(float64(cfg.UnknownPersons)*cfg.UnknownEnrolledShare+0.5), len(enrolled))
	nNever := min(cfg.UnknownPersons-nEnrolled, len(never))
	out := map[int]bool{}
	for _, i := range enrolled[:nEnrolled] {
		out[i] = true
	}
	for _, i := range never[:nNever] {
		out[i] = true
	}
	return out
}

// genCivil gives each unknown person a CIVIL occurrence -- a body found, or a
// living person who cannot say who they are -- with a FINGERPRINT case (their
// prints taken as questioned material) and, sometimes, a FACIAL one; and, for
// some, a MISSING_PERSON report filed earlier, with no case of its own.
func genCivil(cfg Config, g *geography, humans []Human, unknown map[int]bool, r *rand.Rand) ([]Occurrence, []Case) {
	var who []int
	for i := range humans {
		if unknown[i] {
			who = append(who, i)
		}
	}
	found := g.weights([]string{"encontro_cadaver"})
	missing := g.weights([]string{"pessoas_desaparecidas"})

	var occs []Occurrence
	var cases []Case
	for n, i := range who {
		h := humans[i]
		kind := "UNIDENTIFIED_LIVING_PERSON"
		weights := missing
		if r.Float64() < cfg.BodyFoundRate {
			kind, weights = "BODY_FOUND", found
		}
		a := pick(r, weights)
		p := g.pointIn(r, a)
		at := when(cfg, r, "day")
		o := Occurrence{
			ID: fmt.Sprintf("C%06d", n+1), Type: kind, CaseType: "CIVIL",
			CISP: g.areas[a].cisp, LocationID: fmt.Sprintf("SIM-LC-%06d", n+1), Lon: p[0], Lat: p[1],
			Precision: "EXACT", PlaceType: "PUBLIC_ROAD",
			OccurredFrom: at, RegisteredAt: at.Add(time.Duration(30+r.IntN(6*60)) * time.Minute),
			Status: "UNDER_INVESTIGATION", Description: "SIMULATED " + kind, Person: h.ID,
		}
		occs = append(occs, o)

		// Prints of several fingers as questioned lifts, and maybe a photo.
		var lifts []Trace
		for k := 0; k < 3; k++ {
			lifts = append(lifts, Trace{HumanID: h.ID})
		}
		cases = append(cases, Case{CaseType: "CIVIL", Modality: "FINGERPRINT", Description: "SIMULATED FINGERPRINT case (" + kind + ")",
			OccurrenceID: o.ID, Evidences: spread(r, lifts, false)})
		if r.Float64() < cfg.CivilFaceRate {
			cases = append(cases, Case{CaseType: "CIVIL", Modality: "FACIAL", Description: "SIMULATED FACIAL case (" + kind + ")",
				OccurrenceID: o.ID, Evidences: spread(r, []Trace{{HumanID: h.ID}}, true)})
		}

		if r.Float64() < cfg.MissingReportRate {
			ma := pick(r, missing)
			mp := g.pointIn(r, ma)
			before := at.Add(-time.Duration(1+r.IntN(120)) * 24 * time.Hour)
			occs = append(occs, Occurrence{
				ID: fmt.Sprintf("M%06d", n+1), Type: "MISSING_PERSON", CaseType: "CIVIL",
				CISP: g.areas[ma].cisp, LocationID: fmt.Sprintf("SIM-LM-%06d", n+1), Lon: mp[0], Lat: mp[1],
				Precision: "EXACT", PlaceType: "RESIDENCE",
				OccurredFrom: before, RegisteredAt: before.Add(time.Duration(1+r.IntN(48)) * time.Hour),
				Status: "UNDER_INVESTIGATION", Description: "SIMULATED MISSING_PERSON", Person: h.ID,
			})
		}
	}
	return occs, cases
}
