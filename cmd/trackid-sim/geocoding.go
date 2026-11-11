package main

// measure-geocoding measures trackid's geocoder on the world's geography (trackid's
// docs/geocoding-plan.md, phase 4), against a trackid database with IBGE's census address list
// (CNEFE) for the world's state loaded (load-cnefe) and the world's municipality in
// municipalities. It sends nothing outside: no map server is configured, and the OpenStreetMap
// points it compares with are the ones fetch-banks cached in data/rio.
//
// Three measurements, written as counts only, so the output can be published:
//
//   - reverse: a click on a building whose address is not in the list gets the nearest address
//     that is. Each sampled census address is left out in turn and the nearest other one found:
//     grouped by how far that one is (what the reverse limit sees), how often it is on the left-out
//     address's street. And for the world's own occurrence points, how far the nearest census
//     address is, so how many clicks each limit would answer.
//   - search: address strings written from census addresses as people write them: in full with
//     the neighbourhood, abbreviated with no neighbourhood, street and number only (the map's
//     centre, a world occurrence point, gives the municipality), a number not in the list, two
//     letters swapped, no number. Which step answers each and how far from the address's point.
//   - branches: the bank branches' register addresses through the census list, against the point
//     OpenStreetMap gave the same address: how far apart the two sources put one address.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geo"

	"github.com/rodfileto/trackid-sim/world"
	"github.com/rodfileto/trackid/geocode"
)

type censusAddress struct {
	streetKey, street, locality string
	number                      int
	point                       orb.Point
}

// band counts results by an upper bound in metres; the last band has none.
type band struct {
	UpTo  float64 `json:"upToMeters"` // 0: no bound
	Count int     `json:"count"`
	// Only for reverse: of Count, how many were on the left-out address's street.
	RightStreet int `json:"rightStreet,omitempty"`
}

func newBands(bounds ...float64) []band {
	out := make([]band, len(bounds)+1)
	for i, b := range bounds {
		out[i].UpTo = b
	}
	return out
}

func bandOf(bands []band, meters float64) *band {
	for i := range bands[:len(bands)-1] {
		if meters <= bands[i].UpTo {
			return &bands[i]
		}
	}
	return &bands[len(bands)-1]
}

type searchOutcome struct {
	Variant  string         `json:"variant"`
	Asked    int            `json:"asked"`
	Steps    map[string]int `json:"steps"`    // which step answered
	Distance []band         `json:"distance"` // answers from the census list: metres from the address's point
}

type geocodingReport struct {
	Municipality     string          `json:"municipality"`
	CensusAddresses  int             `json:"censusAddresses"` // numbered census addresses in the municipality
	Seed             uint64          `json:"seed"`
	ReverseLeftOut   []band          `json:"reverseLeftOut"`   // by metres from a left-out address to the nearest other
	OccurrencePoints []band          `json:"occurrencePoints"` // by metres to the nearest census address
	Search           []searchOutcome `json:"search"`           // by how the address is written
	BranchesByOSM    map[string]int  `json:"branchesByOsm"`    // branches by the precision OpenStreetMap placed them at
	BranchSteps      map[string]int  `json:"branchSteps"`      // which step answered each branch
	BranchesApart    []band          `json:"branchesApart"`    // census list vs OpenStreetMap, OSM GEOCODED branches answered by number
	BranchesApartAll []band          `json:"branchesApartAll"` // the same, every branch the census list answered
}

func cmdMeasureGeocoding(args []string) {
	fs := flag.NewFlagSet("measure-geocoding", flag.ExitOnError)
	n := fs.Int("n", 2000, "census addresses sampled for search, and occurrence points")
	reverseN := fs.Int("reverse-n", 50000, "census addresses left out in turn: few are far from all others, so many are needed")
	seed := fs.Uint64("seed", 1, "seed for the samples and the clicks")
	examples := fs.Int("examples", 0, "print up to this many failed searches per way of writing, for diagnosis (not in the output file)")
	outPath := fs.String("out", "out/geocoding-measure.json", "where to write the counts")
	fs.Parse(args)
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		log.Fatal("measure-geocoding: DATABASE_URL is required (a trackid database with the census list loaded)")
	}
	db, err := sql.Open("pgx", dsn)
	must(err, "database")
	defer db.Close()
	ctx := context.Background()

	addresses, err := censusAddresses(ctx, db, world.Municipality)
	must(err, "census addresses")
	if len(addresses) == 0 {
		log.Fatalf("measure-geocoding: no census addresses for %s; load the state with trackid's load-cnefe", world.Municipality)
	}
	var municipalities int
	must(db.QueryRowContext(ctx, `SELECT count(*) FROM municipalities WHERE code = $1`, world.Municipality).Scan(&municipalities), "municipalities")
	if municipalities == 0 {
		log.Fatalf("measure-geocoding: municipality %s is not in municipalities; load it with trackid's load-municipalities", world.Municipality)
	}
	r := rand.New(rand.NewPCG(*seed, 0x6e0c0de))
	r.Shuffle(len(addresses), func(i, j int) { addresses[i], addresses[j] = addresses[j], addresses[i] })
	// Disjoint samples: the first -reverse-n addresses for reverse, the next -n for search.
	reverseSample := addresses[:min(*reverseN, len(addresses))]
	searchSample := addresses[len(reverseSample):min(len(reverseSample)+*n, len(addresses))]
	report := geocodingReport{Municipality: world.Municipality, CensusAddresses: len(addresses), Seed: *seed}
	cnefe := geocode.NewCNEFE(db)
	layered := geocode.NewLayered(db, nil)

	// Reverse: each address left out, the nearest other one.
	report.ReverseLeftOut = newBands(10, 25, 50, 75, 100, 150, 200, 300)
	for _, a := range reverseSample {
		var streetKey string
		var meters float64
		err := db.QueryRowContext(ctx, `SELECT street_key, ST_Distance(geom::geography, ST_SetSRID(ST_MakePoint($1::float8, $2::float8), 4326)::geography)
			FROM cnefe_addresses WHERE NOT (street_key = $3 AND number = $4)
			ORDER BY geom <-> ST_SetSRID(ST_MakePoint($1::float8, $2::float8), 4326) LIMIT 1`,
			a.point[0], a.point[1], a.streetKey, a.number).Scan(&streetKey, &meters)
		must(err, "nearest other address")
		b := bandOf(report.ReverseLeftOut, meters)
		b.Count++
		if streetKey == a.streetKey {
			b.RightStreet++
		}
	}

	// The world's own occurrence points.
	points, err := world.OccurrencePoints(*seed, *n)
	must(err, "occurrence points")
	report.OccurrencePoints = newBands(25, 50, 100, 150, 200, 300, 500, 1000)
	for _, p := range points {
		got, found, err := cnefe.Reverse(ctx, p[0], p[1], math.Inf(1))
		must(err, "reverse")
		if found {
			bandOf(report.OccurrencePoints, geo.Distance(p, orb.Point{got.Lon, got.Lat})).Count++
		} else {
			report.OccurrencePoints[len(report.OccurrencePoints)-1].Count++
		}
	}

	// Search: addresses written as people write them.
	listed := map[string]bool{}
	for _, a := range addresses {
		listed[a.streetKey+"|"+strconv.Itoa(a.number)] = true
	}
	variants := []string{"full", "abbreviated", "no_city", "other_number", "typo", "no_number"}
	outcomes := map[string]*searchOutcome{}
	for _, v := range variants {
		outcomes[v] = &searchOutcome{Variant: v, Steps: map[string]int{}, Distance: newBands(50, 200, 1000)}
	}
	city := "Rio de Janeiro/RJ"
	for i, a := range searchSample {
		street, locality := titleCase(a.street), titleCase(a.locality)
		other := a.number
		for k := 1; k <= 20 && listed[a.streetKey+"|"+strconv.Itoa(other)]; k++ {
			other = a.number + k
		}
		texts := map[string]geocode.Query{
			"full":         {Text: fmt.Sprintf("%s, %d - %s, %s", street, a.number, locality, city)},
			"abbreviated":  {Text: fmt.Sprintf("%s %d, Rio de Janeiro - RJ", abbreviate(street), a.number)},
			"no_city":      {Text: fmt.Sprintf("%s, %d", street, a.number), Near: &geocode.Point{Lon: points[i%len(points)][0], Lat: points[i%len(points)][1]}},
			"other_number": {Text: fmt.Sprintf("%s, %d - %s, %s", street, other, locality, city)},
			"typo":         {Text: fmt.Sprintf("%s, %d - %s, %s", typo(street, r), a.number, locality, city)},
			"no_number":    {Text: fmt.Sprintf("%s - %s, %s", street, locality, city)},
		}
		for _, v := range variants {
			o := outcomes[v]
			o.Asked++
			results, err := layered.Search(ctx, texts[v])
			step := "none"
			if !errors.Is(err, geocode.ErrNotFound) {
				must(err, "search")
				step = stepOf(results[0])
			}
			o.Steps[step]++
			meters := 0.0
			if strings.HasPrefix(step, "cnefe") {
				meters = geo.Distance(a.point, orb.Point{results[0].Lon, results[0].Lat})
				bandOf(o.Distance, meters).Count++
			}
			if (!strings.HasPrefix(step, "cnefe") || meters > 1000) && o.Steps["shown"] < *examples {
				o.Steps["shown"]++
				fmt.Fprintf(os.Stderr, "%-12s %-14s %6.0f m  %q\n", v, step, meters, texts[v].Text)
			}
		}
	}
	for _, v := range variants {
		delete(outcomes[v].Steps, "shown")
		report.Search = append(report.Search, *outcomes[v])
	}

	// Branches: the census list against OpenStreetMap.
	branches, err := world.BankBranches()
	must(err, "bank branches")
	report.BranchesByOSM, report.BranchSteps = map[string]int{}, map[string]int{}
	report.BranchesApart, report.BranchesApartAll = newBands(50, 200, 1000, 2000), newBands(50, 200, 1000, 2000)
	for _, b := range branches {
		report.BranchesByOSM[b.Precision]++
		text := fmt.Sprintf("%s, %s - %s, %s", b.Street, b.Number, b.Neighbourhood, city)
		if strings.TrimSpace(b.Number) == "" {
			text = fmt.Sprintf("%s - %s, %s", b.Street, b.Neighbourhood, city)
		}
		results, err := layered.Search(ctx, geocode.Query{Text: text})
		if errors.Is(err, geocode.ErrNotFound) {
			report.BranchSteps["none"]++
			continue
		}
		must(err, "search")
		step := stepOf(results[0])
		report.BranchSteps[step]++
		if !strings.HasPrefix(step, "cnefe") {
			continue
		}
		apart := geo.Distance(orb.Point{b.Lon, b.Lat}, orb.Point{results[0].Lon, results[0].Lat})
		bandOf(report.BranchesApartAll, apart).Count++
		if b.Precision == "GEOCODED" && step == "cnefe_exact" {
			bandOf(report.BranchesApart, apart).Count++
		}
	}

	must(os.MkdirAll(filepath.Dir(*outPath), 0o755), "output directory")
	data, err := json.MarshalIndent(report, "", "  ")
	must(err, "report")
	must(os.WriteFile(*outPath, append(data, '\n'), 0o644), "write")
	printGeocodingReport(report)
}

// censusAddresses reads a municipality's numbered census addresses, in a fixed order so a seed
// gives the same sample.
func censusAddresses(ctx context.Context, db *sql.DB, municipality string) ([]censusAddress, error) {
	rows, err := db.QueryContext(ctx, `SELECT street_key, street, locality, number, lon, lat FROM cnefe_addresses
		WHERE municipality_code = $1 ORDER BY street_key, locality_key, number`, municipality)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []censusAddress
	for rows.Next() {
		var a censusAddress
		if err := rows.Scan(&a.streetKey, &a.street, &a.locality, &a.number, &a.point[0], &a.point[1]); err != nil {
			return nil, err
		}
		a.locality = strings.TrimSpace(a.locality)
		out = append(out, a)
	}
	return out, rows.Err()
}

// stepOf names the step a result came from, by its source and flags.
func stepOf(r geocode.Result) string {
	switch {
	case slices.Contains(r.Flags, geocode.FlagMunicipalityOnly):
		return "municipality"
	case !strings.HasPrefix(r.Source, "cnefe2022:"):
		return "other"
	case slices.Contains(r.Flags, geocode.FlagNearestNumber):
		return "cnefe_nearest"
	case slices.Contains(r.Flags, geocode.FlagStreetAdjusted):
		return "cnefe_adjusted"
	case r.HouseNumber != "":
		return "cnefe_exact"
	default:
		return "cnefe_place"
	}
}

// shift moves a point by meters towards a bearing (radians from north).
func shift(p orb.Point, meters, bearing float64) orb.Point {
	const earth = 6371008.8
	lat := p[1] + meters*math.Cos(bearing)/earth*180/math.Pi
	lon := p[0] + meters*math.Sin(bearing)/(earth*math.Cos(p[1]*math.Pi/180))*180/math.Pi
	return orb.Point{lon, lat}
}

var lowerWords = map[string]bool{"da": true, "de": true, "do": true, "das": true, "dos": true, "e": true}

// titleCase writes IBGE's upper-case name as people do: "RUA DOUTOR JOÃO DA SILVA" is
// "Rua Doutor João da Silva".
func titleCase(s string) string {
	words := strings.Fields(strings.ToLower(s))
	for i, w := range words {
		if i > 0 && lowerWords[w] {
			continue
		}
		runes := []rune(w)
		runes[0] = unicode.ToUpper(runes[0])
		words[i] = string(runes)
	}
	return strings.Join(words, " ")
}

var abbreviations = map[string]string{
	"Rua": "R.", "Avenida": "Av.", "Travessa": "Tv.", "Estrada": "Estr.", "Praça": "Pç.", "Alameda": "Al.",
	"Rodovia": "Rod.", "Doutor": "Dr.", "Professor": "Prof.", "General": "Gal.", "Coronel": "Cel.",
	"Senador": "Sen.", "Presidente": "Pres.", "Marechal": "Mal.", "Santa": "Sta.", "Engenheiro": "Eng.",
}

// abbreviate shortens a street's type and titles as people write them: "R. Dr. João da Silva".
func abbreviate(street string) string {
	words := strings.Fields(street)
	for i, w := range words {
		if short, ok := abbreviations[w]; ok {
			words[i] = short
		}
	}
	return strings.Join(words, " ")
}

// typo swaps two neighbouring letters inside the street name's longest word (not its type).
func typo(street string, r *rand.Rand) string {
	words := strings.Fields(street)
	longest := -1
	for i := 1; i < len(words); i++ {
		if len([]rune(words[i])) >= 5 && (longest < 0 || len([]rune(words[i])) > len([]rune(words[longest]))) {
			longest = i
		}
	}
	if longest < 0 {
		return street
	}
	runes := []rune(words[longest])
	at := 1 + r.IntN(len(runes)-2)
	runes[at], runes[at+1] = runes[at+1], runes[at]
	words[longest] = string(runes)
	return strings.Join(words, " ")
}

func printGeocodingReport(report geocodingReport) {
	bound := func(b band) string {
		if b.UpTo == 0 {
			return "more"
		}
		return fmt.Sprintf("≤ %.0f m", b.UpTo)
	}
	pct := func(part, whole int) string {
		if whole == 0 {
			return "–"
		}
		return fmt.Sprintf("%.0f%%", 100*float64(part)/float64(whole))
	}
	fmt.Printf("Municipality %s: %d numbered census addresses; seed %d.\n\n", report.Municipality, report.CensusAddresses, report.Seed)
	fmt.Println("Each sampled census address left out, by how far the nearest other address is:")
	fmt.Println("\n| nearest other address | addresses | on the same street |")
	fmt.Println("|---|---:|---:|")
	for _, b := range report.ReverseLeftOut {
		fmt.Printf("| %s | %d | %s |\n", bound(b), b.Count, pct(b.RightStreet, b.Count))
	}
	fmt.Println("\nThe world's occurrence points, by how far the nearest census address is:")
	fmt.Println("\n| nearest address | points |")
	fmt.Println("|---|---:|")
	for _, b := range report.OccurrencePoints {
		fmt.Printf("| %s | %d |\n", bound(b), b.Count)
	}
	steps := []string{"cnefe_exact", "cnefe_nearest", "cnefe_adjusted", "cnefe_place", "municipality", "other", "none"}
	fmt.Println("\nSearch, by how the address is written: the step that answered, and the census list's answers' distance from the address:")
	fmt.Printf("\n| written | asked | %s | ≤ 50 m | ≤ 200 m | ≤ 1 km | more |\n", strings.Join(steps, " | "))
	fmt.Println("|---|---:|" + strings.Repeat("---:|", len(steps)+4))
	for _, o := range report.Search {
		row := []string{o.Variant, strconv.Itoa(o.Asked)}
		for _, s := range steps {
			row = append(row, strconv.Itoa(o.Steps[s]))
		}
		for _, b := range o.Distance {
			row = append(row, strconv.Itoa(b.Count))
		}
		fmt.Println("| " + strings.Join(row, " | ") + " |")
	}
	fmt.Printf("\nBank branches by OpenStreetMap's precision: %v; by the census list's step: %v.\n", report.BranchesByOSM, report.BranchSteps)
	fmt.Println("\n| census list vs OpenStreetMap | OSM by number, census by number | every census answer |")
	fmt.Println("|---|---:|---:|")
	for i, b := range report.BranchesApart {
		fmt.Printf("| %s | %d | %d |\n", bound(b), b.Count, report.BranchesApartAll[i].Count)
	}
}
