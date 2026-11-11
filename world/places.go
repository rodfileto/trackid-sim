package world

import (
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"sort"

	"github.com/paulmach/orb"
)

// What other tools measure against the world's geography (trackid-sim measure-geocoding).

// BankBranch is a bank branch of data/rio: its address as the Banco Central's register gives it,
// and the point OpenStreetMap's Nominatim gave that address when fetch-banks ran.
type BankBranch struct {
	ID, Street, Number, Neighbourhood, Postcode string
	Lon, Lat                                    float64
	Precision                                   string // of the OpenStreetMap point: GEOCODED, STREET or NEIGHBOURHOOD
}

// BankBranches returns data/rio's branches with their addresses.
func BankBranches() ([]BankBranch, error) {
	data, err := rioFS.ReadFile("data/rio/bank_branches.json")
	if err != nil {
		return nil, err
	}
	var banks struct {
		Branches []struct {
			ID            string  `json:"id"`
			Street        string  `json:"street"`
			Number        string  `json:"number"`
			Neighbourhood string  `json:"neighbourhood"`
			Postcode      string  `json:"postcode"`
			Lon           float64 `json:"lon"`
			Lat           float64 `json:"lat"`
			Precision     string  `json:"precision"`
		} `json:"branches"`
	}
	if err := json.Unmarshal(data, &banks); err != nil {
		return nil, fmt.Errorf("world: bank_branches.json: %w", err)
	}
	out := make([]BankBranch, len(banks.Branches))
	for i, b := range banks.Branches {
		out[i] = BankBranch{ID: b.ID, Street: b.Street, Number: b.Number, Neighbourhood: b.Neighbourhood,
			Postcode: b.Postcode, Lon: b.Lon, Lat: b.Lat, Precision: b.Precision}
	}
	return out, nil
}

// OccurrencePoints draws n points as the world places an occurrence that is not at a bank: a CISP
// chosen by its share of all of ISP's counts, then a uniform point inside it.
func OccurrencePoints(seed uint64, n int) ([]orb.Point, error) {
	g, err := loadRio()
	if err != nil {
		return nil, err
	}
	columns := make([]string, 0, len(g.counts))
	for c := range g.counts {
		columns = append(columns, c)
	}
	sort.Strings(columns) // the same sums, so the same points, on every run
	w := g.weights(columns)
	r := rand.New(rand.NewPCG(seed, 0x9e0c0de))
	out := make([]orb.Point, n)
	for i := range out {
		out[i] = g.pointIn(r, pick(r, w))
	}
	return out, nil
}
