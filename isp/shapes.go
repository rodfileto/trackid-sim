package isp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"

	"github.com/jonas-p/go-shp"
	"github.com/nwaples/rardecode/v2"
	"github.com/paulmach/orb"
	"github.com/paulmach/orb/geojson"
	"github.com/paulmach/orb/planar"
)

// ReadShapes reads the CISP boundaries out of ISP's CISPshp.rar (a shapefile in
// WGS 84) as GeoJSON features with properties cisp, aisp and areakm2, keeping
// only the CISPs in keep (all when keep is nil). Coordinates are rounded to 6
// decimals (about 0.1 m).
func ReadShapes(rar io.Reader, keep map[int]bool) (*geojson.FeatureCollection, error) {
	files, err := unrar(rar, ".shp", ".dbf", ".prj")
	if err != nil {
		return nil, err
	}
	if !strings.Contains(string(files[".prj"]), "WGS_1984") {
		return nil, fmt.Errorf("isp: shapes are not in WGS 84: %s", files[".prj"])
	}
	sr := shp.SequentialReaderFromExt(io.NopCloser(bytes.NewReader(files[".shp"])), io.NopCloser(bytes.NewReader(files[".dbf"])))
	defer sr.Close()
	field := map[string]int{}
	for i, f := range sr.Fields() {
		field[strings.ToLower(f.String())] = i
	}
	for _, need := range []string{"cisp", "aisp", "areakm2"} {
		if _, ok := field[need]; !ok {
			return nil, fmt.Errorf("isp: shapes have no %q field", need)
		}
	}

	fc := geojson.NewFeatureCollection()
	for sr.Next() {
		_, s := sr.Shape()
		poly, ok := s.(*shp.Polygon)
		if !ok {
			return nil, fmt.Errorf("isp: shape %T, want polygon", s)
		}
		cisp, err := strconv.Atoi(strings.TrimSpace(sr.Attribute(field["cisp"])))
		if err != nil {
			return nil, fmt.Errorf("isp: cisp %q", sr.Attribute(field["cisp"]))
		}
		if keep != nil && !keep[cisp] {
			continue
		}
		aisp, _ := strconv.Atoi(strings.TrimSpace(sr.Attribute(field["aisp"])))
		area, _ := strconv.ParseFloat(strings.TrimSpace(sr.Attribute(field["areakm2"])), 64)
		mp, err := multiPolygon(poly)
		if err != nil {
			return nil, fmt.Errorf("isp: cisp %d: %w", cisp, err)
		}
		f := geojson.NewFeature(orb.Round(mp, 1e6))
		f.Properties["cisp"] = cisp
		f.Properties["aisp"] = aisp
		f.Properties["areakm2"] = area
		fc.Append(f)
	}
	if err := sr.Err(); err != nil {
		return nil, fmt.Errorf("isp: shapes: %w", err)
	}
	return fc, nil
}

// multiPolygon turns a shapefile polygon's rings into polygons: a clockwise ring
// is an outer boundary (an island is another one), a counter-clockwise ring a
// hole in the outer ring that contains it.
func multiPolygon(p *shp.Polygon) (orb.MultiPolygon, error) {
	var outers, holes []orb.Ring
	for i := range p.Parts {
		end := int(p.NumPoints)
		if i+1 < len(p.Parts) {
			end = int(p.Parts[i+1])
		}
		var r orb.Ring
		for _, pt := range p.Points[p.Parts[i]:end] {
			r = append(r, orb.Point{pt.X, pt.Y})
		}
		if r.Orientation() == orb.CW {
			outers = append(outers, r)
		} else {
			holes = append(holes, r)
		}
	}
	if len(outers) == 0 {
		return nil, errors.New("no outer ring")
	}
	mp := make(orb.MultiPolygon, len(outers))
	for i, o := range outers {
		mp[i] = orb.Polygon{o}
	}
	for _, h := range holes {
		placed := false
		for i, o := range outers {
			if planar.RingContains(o, h[0]) {
				mp[i] = append(mp[i], h)
				placed = true
				break
			}
		}
		if !placed {
			return nil, errors.New("a hole outside every outer ring")
		}
	}
	return mp, nil
}

// unrar returns the archive's files with the given extensions, by extension.
func unrar(r io.Reader, exts ...string) (map[string][]byte, error) {
	rr, err := rardecode.NewReader(r)
	if err != nil {
		return nil, fmt.Errorf("isp: rar: %w", err)
	}
	want := map[string]bool{}
	for _, e := range exts {
		want[e] = true
	}
	out := map[string][]byte{}
	for {
		h, err := rr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("isp: rar: %w", err)
		}
		ext := strings.ToLower(path.Ext(h.Name))
		if h.IsDir || !want[ext] {
			continue
		}
		if _, dup := out[ext]; dup {
			return nil, fmt.Errorf("isp: rar holds more than one %s", ext)
		}
		// The boundaries are a few MB; cap a single file at 64 MB.
		b, err := io.ReadAll(io.LimitReader(rr, 64<<20))
		if err != nil {
			return nil, fmt.Errorf("isp: rar %s: %w", h.Name, err)
		}
		out[ext] = b
	}
	for _, e := range exts {
		if _, ok := out[e]; !ok {
			return nil, fmt.Errorf("isp: rar has no %s file", e)
		}
	}
	return out, nil
}

// CISPAt returns the CISP whose boundary contains a point, or 0.
func CISPAt(fc *geojson.FeatureCollection, p orb.Point) int {
	for _, f := range fc.Features {
		if mp, ok := f.Geometry.(orb.MultiPolygon); ok && planar.MultiPolygonContains(mp, p) {
			return propInt(f, "cisp")
		}
	}
	return 0
}

func propInt(f *geojson.Feature, key string) int {
	switch v := f.Properties[key].(type) {
	case int:
		return v
	case float64: // after a JSON round trip
		return int(v)
	}
	return 0
}
