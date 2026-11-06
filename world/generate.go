package world

import (
	"fmt"
	"math"
	"math/rand/v2"
	"slices"
	"sort"
	"time"

	"github.com/rodfileto/trackid-sim/onomastic"
)

// Config controls the population and offending model. Every rate is a
// probability in [0,1] unless noted.
type Config struct {
	Seed   uint64 `json:"seed"`
	Humans int    `json:"humans"`

	// Enrollment. Offenders are enrolled more often than the general population
	// (arrest -> enrollment), which is what makes identification possible at all.
	EnrolledFraction     float64 `json:"enrolledFraction"`
	OffenderEnrolledRate float64 `json:"offenderEnrolledRate"`
	RenewalRate          float64 `json:"renewalRate"`         // also holds a 2nd document under the same person
	DuplicatePersonRate  float64 `json:"duplicatePersonRate"` // enrolled again under a different person_id
	MissingPhotoRate     float64 `json:"missingPhotoRate"`    // enrollment lacks a face photo
	MissingFingerprints  float64 `json:"missingFingerprints"` // enrollment lacks a ten-print
	FatherUnknownRate    float64 `json:"fatherUnknownRate"`   // father not declared on the record

	// Double identity: for this share of Humans, a human A holds a document copying an enrolled human
	// B's name, parents and birth date, with A's own biometrics, on a person of its own (see
	// genEnrollments). The shares say how: the document is a driver's licence (else an RG from another
	// state), it carries B's fiscal number, and A also has a record of their own under their own name.
	DoubleIdentityRate    float64 `json:"doubleIdentityRate"`
	FraudLicenceRate      float64 `json:"fraudLicenceRate"`
	FraudWithFiscalRate   float64 `json:"fraudWithFiscalRate"`
	FraudsterEnrolledRate float64 `json:"fraudsterEnrolledRate"`

	// Offending: heavy-tailed events per offender (discrete Pareto), with
	// habitual partners so co-offending links otherwise unrelated cases.
	OffenderFraction     float64 `json:"offenderFraction"`
	ParetoAlpha          float64 `json:"paretoAlpha"` // smaller = heavier tail
	MinEventsPerOffender int     `json:"minEventsPerOffender"`
	MaxEventsPerOffender int     `json:"maxEventsPerOffender"`
	PartnerRate          float64 `json:"partnerRate"`     // offender has a habitual partner
	CoOffendingRate      float64 `json:"coOffendingRate"` // event includes the partner

	// Traces left behind.
	FaceCaseRate    float64 `json:"faceCaseRate"`    // event yields a FACIAL case (CCTV)
	LiftCaseRate    float64 `json:"liftCaseRate"`    // event yields a FINGERPRINT case (scene lifts)
	FaceCaptureRate float64 `json:"faceCaptureRate"` // each present person is captured on camera
	LiftCaptureRate float64 `json:"liftCaptureRate"` // each present person leaves a usable lift
	RepeatTraceRate float64 `json:"repeatTraceRate"` // a captured person is captured twice
	BystanderRate   float64 `json:"bystanderRate"`   // a non-offender's trace is in the case (victim, staff)
	GoodCaptureRate float64 `json:"goodCaptureRate"` // a facial evidence is a good capture; the rest are medium

	// Geography (docs/OCCURRENCE.md). Each offender commits one occurrence
	// type, anchored in one CISP; whether an occurrence leaves a FACIAL or
	// FINGERPRINT case is the type's own chance (crimeTypes), not a global rate.
	BankFraudShare float64 `json:"bankFraudShare"` // share of ISP's estelionato that happens in person at a bank
	JourneyDecayKm float64 `json:"journeyDecayKm"` // distance scale of exp(-d/scale) from the offender's anchor

	// Repeat places (see placer). An offender returns to one of their first
	// FavouritePlaces places with FavouritePlaceRate; a fresh place is one of the
	// area's HotspotsPerArea attractive places (per kind of place) with
	// HotspotRate, shared by every offender.
	FavouritePlaces    int     `json:"favouritePlaces"`
	FavouritePlaceRate float64 `json:"favouritePlaceRate"`
	HotspotsPerArea    int     `json:"hotspotsPerArea"`
	HotspotRate        float64 `json:"hotspotRate"`

	// Unknown persons: non-offenders with one identity (at most a primary
	// enrollment), never in a criminal case, each found in a CIVIL occurrence.
	UnknownPersons       int     `json:"unknownPersons"`
	UnknownEnrolledShare float64 `json:"unknownEnrolledShare"` // share of them enrolled, hence identifiable
	BodyFoundRate        float64 `json:"bodyFoundRate"`        // BODY_FOUND; the rest UNIDENTIFIED_LIVING_PERSON
	MissingReportRate    float64 `json:"missingReportRate"`    // a MISSING_PERSON report was filed before
	CivilFaceRate        float64 `json:"civilFaceRate"`        // the civil occurrence also yields a FACIAL case

	YearFrom int `json:"yearFrom"`
	YearTo   int `json:"yearTo"`
}

func DefaultConfig() Config {
	return Config{
		Seed: 1,
		// 300 fits the fixed face pool: about 180 humans need a face, against
		// 200 base faces and 146 kept variants (see AssignFaces).
		Humans: 300,

		EnrolledFraction:     0.45,
		OffenderEnrolledRate: 0.70,
		RenewalRate:          0.15,
		DuplicatePersonRate:  0.05,
		MissingPhotoRate:     0.05,
		MissingFingerprints:  0.10,
		FatherUnknownRate:    0.06,
		DoubleIdentityRate:   0.05,

		FraudLicenceRate:      0.5,
		FraudWithFiscalRate:   0.5,
		FraudsterEnrolledRate: 0.5,

		OffenderFraction:     0.15,
		ParetoAlpha:          1.6,
		MinEventsPerOffender: 3,
		MaxEventsPerOffender: 40,
		PartnerRate:          0.30,
		CoOffendingRate:      0.35,

		FaceCaseRate:    0.50,
		LiftCaseRate:    0.50,
		FaceCaptureRate: 0.70,
		LiftCaptureRate: 0.50,
		RepeatTraceRate: 0.25,
		BystanderRate:   0.15,
		GoodCaptureRate: 0.30,

		BankFraudShare: 0.02,
		JourneyDecayKm: 5,

		FavouritePlaces:    2,
		FavouritePlaceRate: 0.20,
		HotspotsPerArea:    3,
		HotspotRate:        0.20,

		UnknownPersons:       12,
		UnknownEnrolledShare: 0.5,
		BodyFoundRate:        0.7,
		MissingReportRate:    0.5,
		CivilFaceRate:        0.6,

		YearFrom: 2015,
		YearTo:   2025,
	}
}

func (c Config) validate() error {
	switch {
	case c.Humans < 2:
		return fmt.Errorf("world: Humans must be >= 2")
	case c.YearFrom > c.YearTo:
		return fmt.Errorf("world: YearFrom > YearTo")
	case c.ParetoAlpha <= 0:
		return fmt.Errorf("world: ParetoAlpha must be > 0")
	case c.MinEventsPerOffender < 1 || c.MaxEventsPerOffender < c.MinEventsPerOffender:
		return fmt.Errorf("world: need 1 <= MinEventsPerOffender <= MaxEventsPerOffender")
	case c.JourneyDecayKm <= 0:
		return fmt.Errorf("world: JourneyDecayKm must be > 0")
	case c.HotspotRate > 0 && c.HotspotsPerArea < 1:
		return fmt.Errorf("world: HotspotRate needs HotspotsPerArea >= 1")
	case c.UnknownPersons < 0:
		return fmt.Errorf("world: UnknownPersons must be >= 0")
	case c.GoodCaptureRate < 0 || c.GoodCaptureRate > 1:
		return fmt.Errorf("world: GoodCaptureRate must be in [0, 1]")
	}
	return nil
}

// Generate builds a World from cfg. Same cfg + lexicon => identical World.
func Generate(cfg Config, lex *onomastic.Lexicon) (*World, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	r := rand.New(rand.NewPCG(cfg.Seed, cfg.Seed^0x9e3779b97f4a7c15))
	gen := onomastic.NewGenerator(lex, r)

	g, err := loadRio()
	if err != nil {
		return nil, err
	}

	w := &World{Config: cfg}
	w.Humans = genHumans(cfg, gen, r)
	markOffenders(cfg, w.Humans, r)
	w.Enrollments = genEnrollments(cfg, w.Humans, r)
	// Unknown persons are chosen before any case, so none is drawn as a
	// bystander, and from their own stream, so the criminal side is unaffected.
	cr := rand.New(rand.NewPCG(cfg.Seed, 0xc1f11))
	unknown := chooseUnknown(cfg, w.Humans, w.Enrollments, cr)
	occs, cases := genCriminal(cfg, g, w.Humans, unknown, r)
	civOccs, civCases := genCivil(cfg, g, w.Humans, unknown, cr)
	w.Occurrences = append(occs, civOccs...)
	numberOccurrences(w.Occurrences, rand.New(rand.NewPCG(cfg.Seed, 0x1b1)))
	w.Cases = numberCases(append(cases, civCases...), w.Occurrences)
	assignCaptures(cfg, w.Cases)
	return w, nil
}

func genHumans(cfg Config, gen *onomastic.Generator, r *rand.Rand) []Human {
	var humans []Human
	for len(humans) < cfg.Humans {
		family := gen.Family()
		size := 1
		for size < 6 && r.Float64() < 0.45 { // geometric: mean ~1.8 children
			size++
		}
		// Ages are as of 1 January of YearFrom, drawn from the Censo 2022 adult
		// table: everyone is an adult for the whole simulated period. The table
		// is 2022's, applied to YearFrom -- an approximation, stated here.
		for _, age := range censo.siblingAges(r, size) {
			if len(humans) >= cfg.Humans {
				break
			}
			sex := onomastic.Male
			if r.Float64() < censo.femaleRate {
				sex = onomastic.Female
			}
			fatherKnown := r.Float64() >= cfg.FatherUnknownRate
			father := family.Father.Full()
			if !fatherKnown {
				father = ""
			}
			humans = append(humans, Human{
				ID:          fmt.Sprintf("H%06d", len(humans)+1),
				Sex:         sex,
				BirthDate:   fmt.Sprintf("%04d-%02d-%02d", cfg.YearFrom-1-age, 1+r.IntN(12), 1+r.IntN(28)),
				Name:        gen.Child(family, sex, fatherKnown).Full(),
				Mother:      family.Mother.Full(),
				Father:      father,
				FatherKnown: fatherKnown,
			})
		}
	}
	return humans
}

func markOffenders(cfg Config, humans []Human, r *rand.Rand) {
	for i := range humans {
		humans[i].Offender = r.Float64() < cfg.OffenderFraction
	}
}

func genEnrollments(cfg Config, humans []Human, r *rand.Rand) []Enrollment {
	var out []Enrollment
	var personSeq, registerSeq, docSeq, fiscalSeq int

	// Records carry the human's data exactly. Registers do have typos in real life, but they are not
	// what this world is for.
	newEnrollment := func(h Human, kind, personID, docType, fiscal string) Enrollment {
		registerSeq++
		docSeq++
		e := Enrollment{
			HumanID:        h.ID,
			Kind:           kind,
			PersonID:       personID,
			DocumentType:   docType,
			DocumentNumber: fmt.Sprintf("SIM-%s-%07d", docType, docSeq),
			FiscalNumber:   fiscal,
			RegisterNumber: fmt.Sprintf("SIM-R-%07d", registerSeq),
			Name:           h.Name,
			Parent1Name:    h.Mother,
			Parent1Gender:  onomastic.Female,
			Parent2Gender:  onomastic.Male,
			BirthDate:      h.BirthDate,
		}
		if h.FatherKnown {
			e.Parent2Name = h.Father
		} else {
			e.Parent2Gender = "U"
		}
		if r.Float64() >= cfg.MissingPhotoRate {
			e.Files = append(e.Files, File{FileType: "photo", Sequence: 1})
		}
		if r.Float64() >= cfg.MissingFingerprints {
			e.Files = append(e.Files, File{FileType: "nist", Sequence: 1})
		}
		if len(e.Files) == 0 {
			e.Files = append(e.Files, File{FileType: "photo", Sequence: 1})
		}
		return e
	}

	primary := map[string]Enrollment{} // human ID -> primary enrollment
	var enrolled []int                 // indexes into humans
	for i, h := range humans {
		p := cfg.EnrolledFraction
		if h.Offender {
			p = cfg.OffenderEnrolledRate
		}
		if r.Float64() >= p {
			continue
		}
		personSeq++
		personID := fmt.Sprintf("SIM-P-%06d", personSeq)
		fiscalSeq++
		// "SIM-CPF-" can never validate as a real CPF, so a synthetic record
		// cannot collide with a real person's taxpayer number.
		fiscal := fmt.Sprintf("SIM-CPF-%06d", fiscalSeq)

		out = append(out, newEnrollment(h, KindPrimary, personID, "RG", fiscal))
		primary[h.ID] = out[len(out)-1]
		enrolled = append(enrolled, i)
		if r.Float64() < cfg.RenewalRate {
			out = append(out, newEnrollment(h, KindRenewal, personID, "CNH", fiscal))
		}
		if r.Float64() < cfg.DuplicatePersonRate {
			personSeq++
			dupFiscal := ""
			if r.IntN(2) == 0 { // duplicate registrations often lack the taxpayer number
				fiscalSeq++
				dupFiscal = fmt.Sprintf("SIM-CPF-%06d", fiscalSeq)
			}
			// An RG from another state: a document of its own, so a person of its own, and normal.
			dup := newEnrollment(h, KindDuplicate, fmt.Sprintf("SIM-P-%06d", personSeq), "RG", dupFiscal)
			dup.DocumentNumber = otherStateRG(docSeq)
			out = append(out, dup)
		}
	}

	// Double identities: a human A (an offender when there are enough) holds a document copying enrolled
	// human B's recorded name, parents and birth date, with A's own face and fingerprints. It is a driver's
	// licence or an RG from another state, on a person of its own: nothing merges people, so what ties it to B
	// is the data it copies. Some carry B's fiscal number, the rest none. Some A also have a record of their
	// own, whose cluster then spans two names; the others appear nowhere under their own name, so the only
	// trace is one identity with two sets of biometrics. A crime A commits resolves, on paper, to B. B is of
	// A's sex (the name is gendered) and the closest in age, each human is in at most one pair, and every record
	// in a pair holds both biometrics, since the point is that biometrics contradict the record.
	//
	// These draws have their own stream, so the shares can change without moving anything else.
	fr := rand.New(rand.NewPCG(cfg.Seed, 0xf4a0d))
	n := min(int(math.Round(cfg.DoubleIdentityRate*float64(cfg.Humans))), len(enrolled)/2)
	isEnrolled := make(map[int]bool, len(enrolled))
	for _, i := range enrolled {
		isEnrolled[i] = true
	}
	offendersFirst := func(list []int) []int {
		fr.Shuffle(len(list), func(i, j int) { list[i], list[j] = list[j], list[i] })
		sort.SliceStable(list, func(i, j int) bool { return humans[list[i]].Offender && !humans[list[j]].Offender })
		return list
	}
	var never []int
	for i := range humans {
		if !isEnrolled[i] {
			never = append(never, i)
		}
	}
	withRecord, withoutRecord := offendersFirst(slices.Clone(enrolled)), offendersFirst(never)
	both := []File{{FileType: "photo", Sequence: 1}, {FileType: "nist", Sequence: 1}}
	byRegister := make(map[string]int, len(out))
	holdsLicence := map[string]bool{}
	for k, e := range out {
		byRegister[e.RegisterNumber] = k
		if e.Kind == KindRenewal {
			holdsLicence[e.PersonID] = true
		}
	}
	setFiles := func(register string) { out[byRegister[register]].Files = slices.Clone(both) }
	born := make([]int, len(humans))
	for i := range humans {
		born[i] = birthYear(humans[i].BirthDate)
	}
	paired := map[int]bool{}
	next := func(list []int) int {
		for _, i := range list {
			if !paired[i] {
				return i
			}
		}
		return -1
	}
	// Exact shares rather than a draw per pair: a world this small could otherwise leave a kind of fraud
	// nearly untested.
	share := func(rate float64) []bool {
		list := make([]bool, n)
		for i := range min(int(math.Round(rate*float64(n))), n) {
			list[i] = true
		}
		fr.Shuffle(n, func(i, j int) { list[i], list[j] = list[j], list[i] })
		return list
	}
	ownRecord, carriesFiscal, licence := share(cfg.FraudsterEnrolledRate), share(cfg.FraudWithFiscalRate), share(cfg.FraudLicenceRate)
	for k := range n {
		a := next(withRecord)
		if b := next(withoutRecord); a < 0 || (b >= 0 && !ownRecord[k]) {
			a = b
		}
		if a < 0 {
			break
		}
		paired[a] = true
		victim, gap := -1, 0
		for _, b := range enrolled {
			if b == a || paired[b] || humans[b].Sex != humans[a].Sex {
				continue
			}
			g := abs(born[a] - born[b])
			if victim < 0 || g < gap {
				victim, gap = b, g
			}
		}
		if victim < 0 {
			continue
		}
		paired[victim] = true
		humans[a].Impersonates = humans[victim].ID
		p := primary[humans[victim].ID]
		setFiles(p.RegisterNumber)
		if own, ok := primary[humans[a].ID]; ok {
			setFiles(own.RegisterNumber)
		}
		fiscal := ""
		if carriesFiscal[k] {
			fiscal = p.FiscalNumber
		}
		personSeq++
		docType := "RG"
		if licence[k] && !holdsLicence[p.PersonID] {
			docType = "CNH"
		}
		e := newEnrollment(humans[victim], KindImpostor, fmt.Sprintf("SIM-P-%06d", personSeq), docType, fiscal)
		if docType == "RG" {
			e.DocumentNumber = otherStateRG(docSeq)
		}
		e.HumanID = humans[a].ID
		e.Files = slices.Clone(both)
		out = append(out, e)
	}
	return out
}

// otherStateRG is the number of an RG issued in another state.
func otherStateRG(seq int) string {
	return fmt.Sprintf("SIM-RG-SP-%07d", seq)
}

func birthYear(iso string) int {
	var y int
	fmt.Sscanf(iso, "%d", &y)
	return y
}

// paretoCount draws a discrete heavy-tailed count in [min, max].
func paretoCount(r *rand.Rand, alpha float64, min, max int) int {
	u := 1 - r.Float64() // (0,1]
	k := int(float64(min) * math.Pow(u, -1/alpha))
	if k > max {
		k = max
	}
	return k
}

// apportion splits n among weights by the largest-remainder method: each gets
// the floor of its exact share, and the leftovers go to the largest fractions
// (ties to the earlier). Offenders are split among crime types this way rather
// than drawn, so a type expected to have one offender has one in every seed.
func apportion(weights []float64, n int) []int {
	total := sum(weights)
	out := make([]int, len(weights))
	if total <= 0 || n <= 0 {
		return out
	}
	type rem struct {
		i    int
		frac float64
	}
	var rems []rem
	left := n
	for i, w := range weights {
		exact := float64(n) * w / total
		out[i] = int(exact)
		left -= out[i]
		rems = append(rems, rem{i, exact - float64(out[i])})
	}
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for j := 0; j < left; j++ {
		out[rems[j].i]++
	}
	return out
}

// event is one criminal occurrence before it is placed: who did it and what.
type event struct {
	kind    int   // index into crimeTypes
	members []int // indexes into humans; members[0] leads
}

// genEvents gives every offender one occurrence type (drawn by typeWeights'
// overall weight) and an anchor CISP (by the type's counts there), then a
// heavy-tailed number of occurrences. Habitual partners and one-off accomplices
// are offenders of the same type, so an offender only ever commits one kind of
// crime.
func genEvents(cfg Config, g *geography, humans []Human, perArea [][]float64, overall []float64, r *rand.Rand) ([]event, map[int]int) {
	var offenders []int
	for i, h := range humans {
		if h.Offender {
			offenders = append(offenders, i)
		}
	}
	kind, anchor := map[int]int{}, map[int]int{}
	byKind := map[int][]int{}
	shuffled := append([]int(nil), offenders...)
	r.Shuffle(len(shuffled), func(i, j int) { shuffled[i], shuffled[j] = shuffled[j], shuffled[i] })
	n := 0
	for k, q := range apportion(overall, len(offenders)) {
		for ; q > 0; q-- {
			o := shuffled[n]
			n++
			kind[o] = k
			anchor[o] = pick(r, perArea[k])
			byKind[k] = append(byKind[k], o)
		}
	}

	partner := map[int]int{}
	for k := range crimeTypes {
		same := append([]int(nil), byKind[k]...)
		r.Shuffle(len(same), func(i, j int) { same[i], same[j] = same[j], same[i] })
		for i := 0; i+1 < len(same); i += 2 {
			if r.Float64() < cfg.PartnerRate {
				partner[same[i]], partner[same[i+1]] = same[i+1], same[i]
			}
		}
	}

	var events []event
	for _, lead := range offenders {
		k := kind[lead]
		max := cfg.MaxEventsPerOffender
		if m := crimeTypes[k].MaxEvents; m > 0 {
			max = m // nobody commits 25 homicides
		}
		for n := paretoCount(r, cfg.ParetoAlpha, min(cfg.MinEventsPerOffender, max), max); n > 0; n-- {
			members := []int{lead}
			if p, ok := partner[lead]; ok && r.Float64() < cfg.CoOffendingRate {
				members = append(members, p)
			}
			if same := byKind[k]; len(same) > 1 && r.Float64() < cfg.CoOffendingRate/3 { // an occasional one-off accomplice
				if other := same[r.IntN(len(same))]; !containsInt(members, other) {
					members = append(members, other)
				}
			}
			events = append(events, event{kind: k, members: members})
		}
	}
	return events, anchor
}

// genCriminal places and times every event, and draws its biometric cases.
func genCriminal(cfg Config, g *geography, humans []Human, unknown map[int]bool, r *rand.Rand) ([]Occurrence, []Case) {
	perArea, overall := typeWeights(cfg, g)
	events, anchor := genEvents(cfg, g, humans, perArea, overall, r)
	pl := newPlacer(cfg, g)

	var occs []Occurrence
	var cases []Case
	for i, ev := range events {
		t := crimeTypes[ev.kind]
		at := pl.place(r, t, perArea[ev.kind], ev.members[0], anchor[ev.members[0]], i+1)
		from := when(cfg, r, t.Hours)
		o := Occurrence{
			ID: fmt.Sprintf("O%06d", i+1), Type: t.Code, CaseType: "CRIMINAL",
			CISP: g.areas[at.area].cisp, LocationID: at.locationID, Lon: at.point[0], Lat: at.point[1],
			Precision: at.precision, PlaceType: at.placeType, PlaceName: at.name, Neighbourhood: at.neighbourhood, BankBranch: at.bank,
			OccurredFrom:  from,
			OffenderCount: int16p(len(ev.members)), VictimCount: int16p(1),
		}
		if len(t.Attempts) > 0 {
			done, tried := sum(g.weights(t.Columns)), sum(g.weights(t.Attempts))
			attempted := r.Float64() < tried/(done+tried)
			o.Attempted = &attempted
		}
		if t.Later {
			to := from.Add(time.Duration(1+r.IntN(12)) * time.Hour)
			o.OccurredTo = &to
		}
		last := from
		if o.OccurredTo != nil {
			last = *o.OccurredTo
		}
		o.RegisteredAt = last.Add(time.Duration(30+r.IntN(48*60)) * time.Minute)
		o.Status = statusFor(cfg, r, from)
		o.Description = fmt.Sprintf("SIMULATED %s", t.Code)
		for _, m := range ev.members {
			o.Offenders = append(o.Offenders, humans[m].ID)
		}
		occs = append(occs, o)
		cases = append(cases, criminalCases(cfg, humans, unknown, ev, t, o, r)...)
	}
	return occs, cases
}

func sum(xs []float64) float64 {
	t := 0.0
	for _, x := range xs {
		t += x
	}
	return t
}

// criminalCases draws which cases an occurrence yields -- each modality with
// the type's chance, redrawn until at least one, as the world holds
// occurrences that produced a biometric case -- and their traces. A case whose
// every capture failed is dropped; the occurrence stays.
func criminalCases(cfg Config, humans []Human, unknown map[int]bool, ev event, t crimeType, o Occurrence, r *rand.Rand) []Case {
	var face, lift bool
	for !face && !lift {
		face, lift = r.Float64() < t.Face, r.Float64() < t.Lift
	}
	var out []Case
	for _, s := range []struct {
		on       bool
		modality string
		capture  float64
	}{{face, "FACIAL", cfg.FaceCaptureRate}, {lift, "FINGERPRINT", cfg.LiftCaptureRate}} {
		if !s.on {
			continue
		}
		var traces []Trace
		for _, m := range ev.members {
			if r.Float64() >= s.capture {
				continue
			}
			traces = append(traces, Trace{HumanID: humans[m].ID})
			if r.Float64() < cfg.RepeatTraceRate {
				traces = append(traces, Trace{HumanID: humans[m].ID})
			}
		}
		if r.Float64() < cfg.BystanderRate {
			if b := r.IntN(len(humans)); !containsInt(ev.members, b) && !unknown[b] {
				traces = append(traces, Trace{HumanID: humans[b].ID, Bystander: true})
			}
		}
		if len(traces) == 0 {
			continue
		}
		out = append(out, Case{
			CaseType:     "CRIMINAL",
			Modality:     s.modality,
			Description:  fmt.Sprintf("SIMULATED %s case (%s)", s.modality, t.Code),
			OccurrenceID: o.ID,
			Evidences:    spread(r, traces, s.modality == "FACIAL"),
		})
	}
	return out
}

// numberCases takes each case's year from its occurrence, orders cases by
// year and numbers them within each year.
func numberCases(cases []Case, occs []Occurrence) []Case {
	year := map[string]int{}
	for _, o := range occs {
		year[o.ID] = o.OccurredFrom.Year()
	}
	for i := range cases {
		cases[i].Year = year[cases[i].OccurrenceID]
	}
	sort.SliceStable(cases, func(i, j int) bool { return cases[i].Year < cases[j].Year })
	perYear := map[int]int{}
	for i := range cases {
		perYear[cases[i].Year]++
		cases[i].CaseID = fmt.Sprintf("SIM-%d-%05d", cases[i].Year, perYear[cases[i].Year])
	}
	return cases
}

// spread distributes traces over 1-3 evidence items (a lift card, a still...).
func spread(r *rand.Rand, traces []Trace, distinct bool) []Evidence {
	n := 1 + r.IntN(min(3, len(traces)))
	buckets := make([][]Trace, n)
	for _, t := range traces {
		i := r.IntN(n)
		if distinct {
			i = freeBucket(buckets, i, t.HumanID)
			if i == len(buckets) {
				buckets = append(buckets, nil)
			}
		}
		buckets[i] = append(buckets[i], t)
	}
	var out []Evidence
	for _, b := range buckets {
		if len(b) == 0 {
			continue
		}
		seq := int16(len(out) + 1)
		for j := range b {
			b[j].Sequence = int16(j + 1)
		}
		out = append(out, Evidence{Sequence: seq, Description: fmt.Sprintf("Simulated evidence item %d", seq), Traces: b})
	}
	return out
}

// freeBucket returns the first bucket from i on (wrapping) that does not yet
// hold human, or len(buckets) when all do. A facial evidence is one camera
// frame, and one person is in a frame once: their repeat capture is another
// image. It draws nothing from the rand, so worlds without a clash are unchanged.
func freeBucket(buckets [][]Trace, i int, human string) int {
	for k := range buckets {
		j := (i + k) % len(buckets)
		if !slices.ContainsFunc(buckets[j], func(t Trace) bool { return t.HumanID == human }) {
			return j
		}
	}
	return len(buckets)
}

func containsInt(list []int, x int) bool {
	for _, v := range list {
		if v == x {
			return true
		}
	}
	return false
}
