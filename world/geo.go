package world

import (
	"embed"
	"encoding/json"
	"fmt"
	"math"
	"math/rand/v2"
	"sort"
	"strconv"
	"sync"

	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geo"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/planar"
)

// The geography the world is placed in: Rio de Janeiro's police-station areas
// (CISPs), ISP-RJ's occurrence counts per area, and the geocoded bank branches,
// all built by `trackid-sim fetch-isp` / `fetch-banks` (docs/OCCURRENCE.md).
//
//go:embed data/rio/isp_counts.json data/rio/cisp.geojson data/rio/bank_branches.json
var rioFS embed.FS

// Municipality and UF are where the world is placed: Rio de Janeiro, the
// municipality of data/rio (isp_counts.json's).
const (
	Municipality = "3304557"
	UF           = "RJ"
)

type area struct {
	cisp     int
	shape    orb.MultiPolygon
	bound    orb.Bound
	centroid orb.Point
}

type branch struct {
	id, bank, name, neighbourhood, precision string
	point                                    orb.Point
}

type geography struct {
	areas []area      // by CISP number
	index map[int]int // CISP -> areas index
	// counts[column][i] is ISP's count for that column in areas[i].
	counts   map[string][]float64
	branches map[int][]branch // CISP -> its bank branches
	dist     [][]float64      // km between area centroids
}

var (
	rioOnce sync.Once
	rio     *geography
	rioErr  error
)

// loadRio parses the embedded data once.
func loadRio() (*geography, error) {
	rioOnce.Do(func() { rio, rioErr = parseRio() })
	return rio, rioErr
}

func parseRio() (*geography, error) {
	g := &geography{index: map[int]int{}, counts: map[string][]float64{}, branches: map[int][]branch{}}

	data, err := rioFS.ReadFile("data/rio/cisp.geojson")
	if err != nil {
		return nil, err
	}
	fc, err := geojson.UnmarshalFeatureCollection(data)
	if err != nil {
		return nil, fmt.Errorf("world: cisp.geojson: %w", err)
	}
	for _, f := range fc.Features {
		mp, ok := f.Geometry.(orb.MultiPolygon)
		if !ok {
			return nil, fmt.Errorf("world: cisp.geojson: geometry %T", f.Geometry)
		}
		c, _ := planar.CentroidArea(mp)
		g.areas = append(g.areas, area{cisp: int(f.Properties.MustFloat64("cisp")), shape: mp, bound: mp.Bound(), centroid: c})
	}
	sort.Slice(g.areas, func(i, j int) bool { return g.areas[i].cisp < g.areas[j].cisp })
	for i, a := range g.areas {
		g.index[a.cisp] = i
	}
	g.dist = make([][]float64, len(g.areas))
	for i := range g.areas {
		g.dist[i] = make([]float64, len(g.areas))
		for j := range g.areas {
			g.dist[i][j] = geo.Distance(g.areas[i].centroid, g.areas[j].centroid) / 1000
		}
	}

	var counts struct {
		Municipality string `json:"municipality"`
		Columns      []struct {
			Name   string         `json:"name"`
			ByCISP map[string]int `json:"byCisp"`
		} `json:"columns"`
	}
	if data, err = rioFS.ReadFile("data/rio/isp_counts.json"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &counts); err != nil {
		return nil, fmt.Errorf("world: isp_counts.json: %w", err)
	}
	if counts.Municipality != Municipality {
		return nil, fmt.Errorf("world: isp_counts.json is for municipality %s, not %s", counts.Municipality, Municipality)
	}
	for _, c := range counts.Columns {
		v := make([]float64, len(g.areas))
		for k, n := range c.ByCISP {
			cisp, _ := strconv.Atoi(k)
			i, ok := g.index[cisp]
			if !ok {
				return nil, fmt.Errorf("world: isp_counts.json: CISP %d has no boundary", cisp)
			}
			v[i] = float64(n)
		}
		g.counts[c.Name] = v
	}

	var banks struct {
		Branches []struct {
			ID            string  `json:"id"`
			Bank          string  `json:"bank"`
			Name          string  `json:"name"`
			Neighbourhood string  `json:"neighbourhood"`
			Lon           float64 `json:"lon"`
			Lat           float64 `json:"lat"`
			Precision     string  `json:"precision"`
			CISP          int     `json:"cisp"`
		} `json:"branches"`
	}
	if data, err = rioFS.ReadFile("data/rio/bank_branches.json"); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &banks); err != nil {
		return nil, fmt.Errorf("world: bank_branches.json: %w", err)
	}
	for _, b := range banks.Branches {
		g.branches[b.CISP] = append(g.branches[b.CISP], branch{
			id: b.ID, bank: b.Bank, name: b.Name, neighbourhood: b.Neighbourhood,
			precision: b.Precision, point: orb.Point{b.Lon, b.Lat},
		})
	}
	return g, nil
}

// weights returns, per area, the summed ISP counts of columns.
func (g *geography) weights(columns []string) []float64 {
	w := make([]float64, len(g.areas))
	for _, c := range columns {
		for i, n := range g.counts[c] {
			w[i] += n
		}
	}
	return w
}

// pointIn draws a uniform point inside an area (rejection sampling on its
// bounding box), rounded to 6 decimals like the boundaries.
func (g *geography) pointIn(r *rand.Rand, i int) orb.Point {
	a := g.areas[i]
	for {
		p := orb.Point{
			a.bound.Min[0] + r.Float64()*(a.bound.Max[0]-a.bound.Min[0]),
			a.bound.Min[1] + r.Float64()*(a.bound.Max[1]-a.bound.Min[1]),
		}
		if planar.MultiPolygonContains(a.shape, p) {
			return orb.Point{math.Round(p[0]*1e6) / 1e6, math.Round(p[1]*1e6) / 1e6}
		}
	}
}

// pick draws an index with probability proportional to w; -1 when all are 0.
func pick(r *rand.Rand, w []float64) int {
	total := 0.0
	for _, x := range w {
		total += x
	}
	if total <= 0 {
		return -1
	}
	u := r.Float64() * total
	for i, x := range w {
		if u < x {
			return i
		}
		u -= x
	}
	for i := len(w) - 1; i >= 0; i-- {
		if w[i] > 0 {
			return i
		}
	}
	return -1
}
