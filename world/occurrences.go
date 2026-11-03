package world

import (
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"time"

	"github.com/paulmach/orb"
)

// Occurrence is a registered occurrence (a police report) in the world: what
// trackid's occurrences.Ingest receives, plus ground truth. See
// docs/OCCURRENCE.md, "Generation, re-evaluated".
type Occurrence struct {
	ID        string `json:"id"` // internal key; cases point at it
	Type      string `json:"type"`
	Attempted *bool  `json:"attempted,omitempty"`
	CaseType  string `json:"caseType"` // CRIMINAL or CIVIL: the nature of its cases

	Agency string `json:"agency"`           // the police station of the CISP, "DP-005"
	Number string `json:"number,omitempty"` // the BO number, unique per agency; empty when an inquiry registers it
	// Inquiry is the inquiry (IPL) opened for it, or, with no Number, the one that registers it
	// (opened by portaria, with no BO). RequestInquiry is another inquiry its requests come from,
	// when they do not come from its own. See docs/OCCURRENCE.md, "Inquiries".
	Inquiry        string `json:"inquiry,omitempty"`
	RequestInquiry string `json:"requestInquiry,omitempty"`

	CISP          int     `json:"cisp"`
	LocationID    string  `json:"locationId"` // shared by occurrences at the same place
	Lon           float64 `json:"lon"`
	Lat           float64 `json:"lat"`
	Precision     string  `json:"precision"`
	PlaceType     string  `json:"placeType"`
	PlaceName     string  `json:"placeName,omitempty"`
	Neighbourhood string  `json:"neighbourhood,omitempty"`
	BankBranch    string  `json:"bankBranch,omitempty"` // the branch's CNPJ, for bank types

	OccurredFrom time.Time  `json:"occurredFrom"`
	OccurredTo   *time.Time `json:"occurredTo,omitempty"`
	RegisteredAt time.Time  `json:"registeredAt"`

	Status        string `json:"status"`
	OffenderCount *int16 `json:"offenderCount,omitempty"`
	VictimCount   *int16 `json:"victimCount,omitempty"`
	Description   string `json:"description"`

	// Ground truth, never loaded: who did it, or who the unknown person is.
	Offenders []string `json:"offenders,omitempty"`
	Person    string   `json:"person,omitempty"`
}

// crimeType is a criminal occurrence type and how the world generates it. The
// case chances and hour profiles are assumptions
// (docs/OCCURRENCE.md); the counts are ISP-RJ's.
type crimeType struct {
	Code      string
	Columns   []string // ISP columns of the consummated crime
	Attempts  []string // ISP columns of its attempts (occurrences.attempted)
	Share     float64  // fraction of the ISP count that is this type (bank FRAUD); 0 = all
	Face      float64  // P(a FACIAL case)
	Lift      float64  // P(a FINGERPRINT case)
	AtBank    bool     // happens at a bank branch
	Place     string   // locations.place_type when not at a bank
	Hours     string   // hour-of-day profile
	Later     bool     // found after the fact: occurred_to is set
	MaxEvents int      // cap on one offender's occurrences of this type; 0 = Config's
}

// crimeTypes are the scene-evidence types: those that leave CCTV or latents.
var crimeTypes = []crimeType{
	{Code: "STREET_ROBBERY", Columns: []string{"roubo_transeunte"}, Face: 0.30, Lift: 0.02, Place: "PUBLIC_ROAD", Hours: "street"},
	{Code: "PHONE_ROBBERY", Columns: []string{"roubo_celular"}, Face: 0.30, Lift: 0.02, Place: "PUBLIC_ROAD", Hours: "street"},
	{Code: "STREET_THEFT", Columns: []string{"furto_transeunte"}, Face: 0.15, Lift: 0.02, Place: "PUBLIC_ROAD", Hours: "street"},
	{Code: "PHONE_THEFT", Columns: []string{"furto_celular"}, Face: 0.10, Lift: 0.01, Place: "PUBLIC_ROAD", Hours: "street"},
	{Code: "VEHICLE_ROBBERY", Columns: []string{"roubo_veiculo"}, Face: 0.20, Lift: 0.30, Place: "PUBLIC_ROAD", Hours: "night"},
	{Code: "VEHICLE_THEFT", Columns: []string{"furto_veiculos"}, Face: 0.10, Lift: 0.30, Place: "PUBLIC_ROAD", Hours: "night", Later: true},
	{Code: "CARGO_ROBBERY", Columns: []string{"roubo_carga"}, Face: 0.20, Lift: 0.30, Place: "PUBLIC_ROAD", Hours: "day"},
	{Code: "COMMERCIAL_ROBBERY", Columns: []string{"roubo_comercio"}, Face: 0.80, Lift: 0.20, Place: "COMMERCE", Hours: "day"},
	{Code: "HOME_ROBBERY", Columns: []string{"roubo_residencia"}, Face: 0.20, Lift: 0.50, Place: "RESIDENCE", Hours: "night"},
	{Code: "OTHER_THEFT", Columns: []string{"outros_furtos"}, Face: 0.05, Lift: 0.05, Place: "OTHER", Hours: "day", Later: true},
	{Code: "ATM_ROBBERY", Columns: []string{"roubo_cx_eletronico"}, Face: 0.90, Lift: 0.30, AtBank: true, Hours: "late"},
	{Code: "POST_WITHDRAWAL_ROBBERY", Columns: []string{"roubo_apos_saque"}, Face: 0.60, Lift: 0.02, AtBank: true, Hours: "bank"},
	{Code: "FORCED_WITHDRAWAL_ROBBERY", Columns: []string{"roubo_conducao_saque"}, Face: 0.90, Lift: 0.05, AtBank: true, Hours: "street"},
	{Code: "EXPRESS_KIDNAPPING", Columns: []string{"sequestro_relampago"}, Face: 0.90, Lift: 0.10, AtBank: true, Hours: "street"},
	{Code: "FRAUD", Columns: []string{"estelionato"}, Share: -1, Face: 0.80, Lift: 0.02, AtBank: true, Hours: "bank"},
	{Code: "HOMICIDE", MaxEvents: 3, Columns: []string{"hom_doloso"}, Attempts: []string{"tentat_hom"}, Face: 0.20, Lift: 0.40, Place: "PUBLIC_ROAD", Hours: "late"},
	{Code: "ROBBERY_HOMICIDE", MaxEvents: 3, Columns: []string{"latrocinio"}, Face: 0.40, Lift: 0.40, Place: "PUBLIC_ROAD", Hours: "street"},
	{Code: "FEMICIDE", MaxEvents: 3, Columns: []string{"feminicidio"}, Attempts: []string{"tentativa_feminicidio"}, Face: 0.10, Lift: 0.40, Place: "RESIDENCE", Hours: "late"},
}

// hourProfiles weigh the hour of day (0-23) an occurrence starts at.
var hourProfiles = map[string][24]float64{
	//         0  1  2  3  4  5  6  7  8  9 10 11 12 13 14 15 16 17 18 19 20 21 22 23
	"street": {3, 2, 1, 1, 1, 2, 4, 6, 6, 5, 5, 5, 6, 6, 6, 7, 8, 10, 12, 12, 11, 9, 7, 5},
	"day":    {1, 1, 0, 0, 0, 1, 2, 4, 7, 9, 10, 10, 10, 10, 10, 10, 10, 9, 7, 5, 3, 2, 1, 1},
	"night":  {7, 6, 5, 4, 3, 3, 3, 4, 4, 3, 3, 3, 3, 3, 3, 4, 5, 6, 8, 10, 11, 11, 10, 9},
	"late":   {9, 9, 8, 7, 5, 3, 2, 2, 2, 2, 2, 2, 2, 2, 2, 3, 3, 4, 5, 7, 9, 10, 10, 10},
	"bank":   {0, 0, 0, 0, 0, 0, 1, 2, 4, 8, 12, 12, 12, 12, 12, 12, 10, 6, 3, 2, 1, 1, 0, 0},
}

// brt is the time zone of the reports: Brasília time, without the daylight
// saving Brazil still had until 2019 (a simplification).
var brt = time.FixedZone("BRT", -3*3600)

// caseChance is P(at least one biometric case) for a type.
func (t crimeType) caseChance() float64 { return 1 - (1-t.Face)*(1-t.Lift) }

// share is the fraction of the ISP count the type takes.
func (t crimeType) share(cfg Config) float64 {
	switch {
	case t.Share < 0:
		return cfg.BankFraudShare
	case t.Share == 0:
		return 1
	}
	return t.Share
}

// typeWeights returns each crime type's per-area weight (ISP count, attempts
// included, times its share; zero outside areas with a bank branch for bank
// types) and its overall weight for drawing an offender's type: the area
// weights' sum times P(case), since the world holds occurrences that produced a
// biometric case.
func typeWeights(cfg Config, g *geography) (perArea [][]float64, overall []float64) {
	for _, t := range crimeTypes {
		w := g.weights(append(append([]string{}, t.Columns...), t.Attempts...))
		total := 0.0
		for i := range w {
			w[i] *= t.share(cfg)
			if t.AtBank && len(g.branches[g.areas[i].cisp]) == 0 {
				w[i] = 0
			}
			total += w[i]
		}
		perArea = append(perArea, w)
		overall = append(overall, total*t.caseChance())
	}
	return perArea, overall
}

// placed is where an occurrence happened.
type placed struct {
	area                           int
	point                          orb.Point
	locationID, precision          string
	placeType, name, neighbourhood string
	bank                           string
}

// placer draws where occurrences happen, remembering what makes places repeat:
// each offender's favourite places (offender habit: a series returns to the
// same branch, shop or corner) and each area's hotspots (repeat victimisation:
// a few attractive places per area and kind of place, hit by different
// offenders). Bank branches are already a shared pool.
type placer struct {
	cfg   Config
	g     *geography
	moved map[string]orb.Point // branches geocoded to a neighbourhood: their own point
	favs  map[int][]placed     // offender (humans index) -> favourite places
	hot   map[string][]placed  // "<area>/<placeType>" -> hotspots
}

func newPlacer(cfg Config, g *geography) *placer {
	return &placer{cfg: cfg, g: g, moved: map[string]orb.Point{}, favs: map[int][]placed{}, hot: map[string][]placed{}}
}

// place draws where an occurrence of type t by offender lead (anchored in area
// anchor) happens: one of lead's favourite places with FavouritePlaceRate
// (a favourite of the same kind of place), else a fresh place -- an area
// weighted by t's ISP count there and exp(-distance / decay), then one of its
// bank branches for bank types, one of its hotspots with HotspotRate, or a
// uniform point in it. A lead's first FavouritePlaces places become their
// favourites.
func (pl *placer) place(r *rand.Rand, t crimeType, weights []float64, lead, anchor, seq int) placed {
	kind := t.Place
	if t.AtBank {
		kind = "BANK"
	}
	var same []placed
	for _, f := range pl.favs[lead] {
		if f.placeType == kind {
			same = append(same, f)
		}
	}
	if len(same) > 0 && r.Float64() < pl.cfg.FavouritePlaceRate {
		return same[r.IntN(len(same))]
	}

	w := make([]float64, len(weights))
	for i := range w {
		w[i] = weights[i] * math.Exp(-pl.g.dist[anchor][i]/pl.cfg.JourneyDecayKm)
	}
	i := pick(r, w)
	if i < 0 {
		i = anchor
	}
	var p placed
	switch {
	case t.AtBank:
		p = pl.branch(r, i)
	case r.Float64() < pl.cfg.HotspotRate:
		hs := pl.hotspots(i, kind)
		p = hs[r.IntN(len(hs))]
	default:
		p = placed{area: i, point: pl.g.pointIn(r, i), locationID: fmt.Sprintf("SIM-L-%06d", seq), precision: "EXACT", placeType: kind}
	}
	if len(pl.favs[lead]) < pl.cfg.FavouritePlaces {
		pl.favs[lead] = append(pl.favs[lead], p)
	}
	return p
}

// branch draws one of an area's bank branches. A branch geocoded only to its
// neighbourhood gets a point of its own inside its area (fixed per branch), so
// the neighbourhood's centroid is not a false hotspot.
func (pl *placer) branch(r *rand.Rand, i int) placed {
	bs := pl.g.branches[pl.g.areas[i].cisp]
	b := bs[r.IntN(len(bs))]
	p, precision := b.point, b.precision
	if precision == "NEIGHBOURHOOD" {
		if q, ok := pl.moved[b.id]; ok {
			p = q
		} else {
			p = pl.g.pointIn(rand.New(rand.NewPCG(pl.cfg.Seed, hashString(b.id))), i)
			pl.moved[b.id] = p
		}
		precision = "EXACT"
	}
	return placed{
		area: i, point: p, locationID: "SIM-BANK-" + b.id, precision: precision, placeType: "BANK",
		name: b.bank + " - " + b.name, neighbourhood: b.neighbourhood, bank: b.id,
	}
}

// hotspots returns an area's attractive places of one kind, made on first use
// from their own stream, so they do not depend on the order occurrences are
// drawn in.
func (pl *placer) hotspots(i int, kind string) []placed {
	key := fmt.Sprintf("%d/%s", i, kind)
	if hs, ok := pl.hot[key]; ok {
		return hs
	}
	cisp := pl.g.areas[i].cisp
	r := rand.New(rand.NewPCG(pl.cfg.Seed, hashString(key)))
	var hs []placed
	for k := 1; k <= pl.cfg.HotspotsPerArea; k++ {
		hs = append(hs, placed{
			area: i, point: pl.g.pointIn(r, i), precision: "EXACT", placeType: kind,
			locationID: fmt.Sprintf("SIM-H-%03d-%s-%d", cisp, kind, k),
		})
	}
	pl.hot[key] = hs
	return hs
}

func hashString(s string) uint64 {
	var h uint64 = 14695981039346656037
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// when draws the moment an occurrence starts: a uniform day in YearFrom..YearTo
// and an hour from the profile.
func when(cfg Config, r *rand.Rand, profile string) time.Time {
	start := time.Date(cfg.YearFrom, 1, 1, 0, 0, 0, 0, brt)
	days := int(time.Date(cfg.YearTo+1, 1, 1, 0, 0, 0, 0, brt).Sub(start).Hours() / 24)
	hp := hourProfiles[profile]
	hour := pick(r, hp[:])
	return start.AddDate(0, 0, r.IntN(days)).Add(time.Duration(hour)*time.Hour + time.Duration(r.IntN(60))*time.Minute)
}

// statusFor is a report's status: older reports have moved on.
func statusFor(cfg Config, r *rand.Rand, at time.Time) string {
	if at.Year() >= cfg.YearTo-1 {
		return "UNDER_INVESTIGATION"
	}
	if r.Float64() < 0.3 {
		return "CLOSED"
	}
	return "INQUIRY_OPENED"
}

func int16p(n int) *int16 {
	v := int16(n)
	return &v
}

// Inquiry rates (assumptions, docs/OCCURRENCE.md, "Inquiries").
const (
	pInquiryRegisters = 0.10 // a criminal occurrence registered by its inquiry, with no BO
	pOtherInquiry     = 0.15 // a criminal occurrence whose requests come from another inquiry
)

// numberOccurrences orders occurrences by time and gives each its agency, a BO number unique
// per agency, "<seq>/<year>" as police reports are numbered, and its inquiries (IPL), numbered
// per agency and year as well. r is its own stream, so inquiries change nothing else in the world.
func numberOccurrences(occs []Occurrence, r *rand.Rand) {
	sort.SliceStable(occs, func(i, j int) bool { return occs[i].OccurredFrom.Before(occs[j].OccurredFrom) })
	seq, ipl := map[string]int{}, map[string]int{}
	inquiry := func(o *Occurrence) string {
		year := o.RegisteredAt.Year()
		key := fmt.Sprintf("%s/%d", o.Agency, year)
		ipl[key]++
		return fmt.Sprintf("SIM-IPL-%03d-%05d/%d", o.CISP, ipl[key], year)
	}
	for i := range occs {
		o := &occs[i]
		o.Agency = fmt.Sprintf("DP-%03d", o.CISP)
		registers, other := false, false
		if o.CaseType == "CRIMINAL" {
			registers, other = r.Float64() < pInquiryRegisters, r.Float64() < pOtherInquiry
		}
		if registers {
			// An inquiry exists from the start.
			if o.Status == "REGISTERED" || o.Status == "UNDER_INVESTIGATION" {
				o.Status = "INQUIRY_OPENED"
			}
		} else {
			key := fmt.Sprintf("%s/%d", o.Agency, o.OccurredFrom.Year())
			seq[key]++
			o.Number = fmt.Sprintf("SIM-%03d-%05d/%d", o.CISP, seq[key], o.OccurredFrom.Year())
		}
		if registers || o.Status == "INQUIRY_OPENED" || o.Status == "CLOSED" {
			o.Inquiry = inquiry(o)
		}
		if other {
			o.RequestInquiry = inquiry(o)
		}
	}
}
