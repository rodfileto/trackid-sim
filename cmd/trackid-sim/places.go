package main

// fetch-isp and fetch-banks build the committed place data for the simulated
// world's geography (docs/OCCURRENCE.md): ISP-RJ's occurrence counts per
// police-station area and those areas' boundaries, and the bank branches with
// coordinates. Raw downloads and geocoder answers are cached under -cache
// (gitignored); only the derived files are written to -out.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/paulmach/orb/geojson"
	"github.com/rodfileto/trackid-sim/banks"
	"github.com/rodfileto/trackid-sim/isp"
	"github.com/rodfileto/trackid/geocode"
)

const userAgent = "trackid-sim (+https://github.com/rodfileto/trackid)"

func cmdFetchISP(args []string) {
	fs := flag.NewFlagSet("fetch-isp", flag.ExitOnError)
	municipality := fs.String("municipality", "3304557", "IBGE code of the municipality (ISP's mcirc); default Rio de Janeiro")
	months := fs.Int("months", 12, "how many of the latest months to sum")
	outDir := fs.String("out", "world/data/rio", "directory for isp_counts.json and cisp.geojson")
	cacheDir := fs.String("cache", "out/cache", "where raw downloads are kept")
	fs.Parse(args)

	csvData, sum := download(isp.CountsURL, *cacheDir)
	counts, err := isp.ParseCounts(bytes.NewReader(csvData), *municipality, *months)
	must(err, "parse ISP counts")
	counts.SHA256, counts.Fetched = sum, time.Now().Format("2006-01-02")

	keep := map[int]bool{}
	for _, c := range counts.CISPs {
		keep[c] = true
	}
	rar, _ := download(isp.ShapesURL, *cacheDir)
	fc, err := isp.ReadShapes(bytes.NewReader(rar), keep)
	must(err, "read CISP shapes")
	got := map[int]bool{}
	for _, f := range fc.Features {
		got[int(f.Properties["cisp"].(int))] = true
	}
	for c := range keep {
		if !got[c] {
			log.Fatalf("CISP %d has counts but no boundary", c)
		}
	}
	fc.ExtraMembers = geojson.Properties{
		"source":  isp.Source + ", " + isp.ShapesURL + " (credits ISP-RJ, SEPOL, SEPM)",
		"licence": isp.Licence,
		"fetched": counts.Fetched,
	}
	sort.Slice(fc.Features, func(i, j int) bool {
		return fc.Features[i].Properties["cisp"].(int) < fc.Features[j].Properties["cisp"].(int)
	})

	must(os.MkdirAll(*outDir, 0o755), "create output dir")
	writeJSON(filepath.Join(*outDir, "isp_counts.json"), counts)
	// Compact: indented, every coordinate would take a line.
	geo, err := json.Marshal(fc)
	must(err, "encode CISP boundaries")
	must(os.WriteFile(filepath.Join(*outDir, "cisp.geojson"), append(geo, '\n'), 0o644), "write CISP boundaries")
	log.Printf("%d CISPs, %s..%s; wrote %s/isp_counts.json and cisp.geojson", len(counts.CISPs), counts.Period[0], counts.Period[1], *outDir)
}

func cmdFetchBanks(args []string) {
	fs := flag.NewFlagSet("fetch-banks", flag.ExitOnError)
	municipality := fs.String("municipality", "3304557", "IBGE code of the municipality")
	city := fs.String("city", "Rio de Janeiro", "city name for the geocoder")
	state := fs.String("state", "Rio de Janeiro", "state name for the geocoder")
	cispPath := fs.String("cisp", "world/data/rio/cisp.geojson", "the municipality's CISP boundaries (from fetch-isp): a point must fall in one")
	out := fs.String("out", "world/data/rio/bank_branches.json", "where to write the branches")
	cacheDir := fs.String("cache", "out/cache", "where the branch register and geocoder answers are kept")
	limit := fs.Int("limit", 0, "geocode only the first N branches (a trial); 0 = all")
	fs.Parse(args)

	ctx := context.Background()
	data, err := os.ReadFile(*cispPath)
	must(err, "read CISP boundaries")
	cisps, err := geojson.UnmarshalFeatureCollection(data)
	must(err, "parse CISP boundaries")

	// The register is cached so a rerun (or a trial followed by the full run)
	// geocodes the same snapshot.
	must(os.MkdirAll(*cacheDir, 0o755), "create cache dir")
	regPath := filepath.Join(*cacheDir, "bcb_agencias.json")
	var raw []banks.Raw
	if b, err := os.ReadFile(regPath); err == nil {
		var v struct {
			Value []banks.Raw `json:"value"`
		}
		must(json.Unmarshal(b, &v), "parse cached register")
		raw = v.Value
	} else {
		var body []byte
		raw, body, err = banks.FetchRaw(ctx, &http.Client{Timeout: 5 * time.Minute})
		must(err, "fetch branch register")
		must(os.WriteFile(regPath, body, 0o644), "cache branch register")
	}

	var here []banks.Raw
	for _, r := range raw {
		if r.MunicipioIbge == *municipality && banks.IsBank(r.Segmento) {
			here = append(here, r)
		}
	}
	sort.Slice(here, func(i, j int) bool {
		return here[i].CnpjBase+here[i].CnpjSequencial < here[j].CnpjBase+here[j].CnpjSequencial
	})
	if len(here) == 0 {
		log.Fatalf("no bank branches in municipality %s", *municipality)
	}
	if *limit > 0 && *limit < len(here) {
		here = here[:*limit]
	}

	// OpenStreetMap's public instance unless GEOCODER_URL names another: a few hundred
	// addresses, once, a second apart, and cached so a rerun sends nothing already answered.
	g, err := geocode.NewNominatim(geocode.NominatimConfig{
		BaseURL:   envOr("GEOCODER_URL", geocode.PublicNominatim),
		UserAgent: envOr("GEOCODER_USER_AGENT", userAgent),
		Email:     os.Getenv("GEOCODER_EMAIL"),
	})
	must(err, "geocoder")
	cache, err := openGeoCache(filepath.Join(*cacheDir, "nominatim.jsonl"))
	must(err, "open geocoder cache")
	defer cache.Close()

	built := banks.Built{
		Source:       banks.BranchesSource + "; coordinates from Nominatim (OpenStreetMap)",
		Licence:      banks.Licence,
		Attribution:  "Banco Central do Brasil; © OpenStreetMap contributors",
		BranchesURL:  banks.BranchesURL,
		BranchesAsOf: here[0].Posicao,
		Geocoder:     "Nominatim " + envOr("GEOCODER_URL", geocode.PublicNominatimURL),
		Built:        banks.Now(),
		Municipality: *municipality,
	}
	for i, r := range here {
		b, err := banks.Geocode(ctx, g, cache, cisps, r, *city, *state)
		// A busy public server answers 503/429: back off and retry the branch
		// (its answers so far are cached) rather than hammer it or give up.
		for wait := time.Minute; err != nil && !errors.Is(err, banks.ErrUnplaced) && wait <= 4*time.Minute; wait *= 2 {
			log.Printf("geocode %s: %v; retrying in %v", r.CnpjBase+r.CnpjSequencial+r.CnpjDv, err, wait)
			time.Sleep(wait)
			b, err = banks.Geocode(ctx, g, cache, cisps, r, *city, *state)
		}
		switch {
		case errors.Is(err, banks.ErrUnplaced):
			built.Failures = append(built.Failures, banks.Failure{ID: b.ID, Address: fmt.Sprintf("%s %s, %s, %s", b.Street, b.Number, b.Neighbourhood, b.Postcode), Reason: err.Error()})
		case err != nil:
			log.Fatalf("geocode %s: %v (answers so far are cached; rerun to resume)", b.ID, err)
		default:
			built.Branches = append(built.Branches, b)
		}
		if (i+1)%25 == 0 || i+1 == len(here) {
			log.Printf("%d/%d branches (%d placed, %d not)", i+1, len(here), len(built.Branches), len(built.Failures))
		}
	}
	if *limit > 0 {
		log.Printf("trial of %d: not writing %s", *limit, *out)
		for _, b := range built.Branches {
			log.Printf("  %-40s %-14s cisp %3d  %-15s %.5f,%.5f  %s", b.Street+" "+b.Number, b.Precision, b.CISP, b.MatchedBy, b.Lat, b.Lon, b.OSMName)
		}
		for _, f := range built.Failures {
			log.Printf("  FAILED %s", f.Address)
		}
		return
	}
	must(os.MkdirAll(filepath.Dir(*out), 0o755), "create output dir")
	writeJSON(*out, built)
	byPrecision := map[string]int{}
	for _, b := range built.Branches {
		byPrecision[b.Precision]++
	}
	log.Printf("wrote %s: %d placed %v, %d not", *out, len(built.Branches), byPrecision, len(built.Failures))
}

// download returns url's bytes and their sha256, from cacheDir when already
// fetched.
func download(url, cacheDir string) ([]byte, string) {
	must(os.MkdirAll(cacheDir, 0o755), "create cache dir")
	path := filepath.Join(cacheDir, filepath.Base(url))
	data, err := os.ReadFile(path)
	if err != nil {
		req, err := http.NewRequest(http.MethodGet, url, nil)
		must(err, "request "+url)
		req.Header.Set("User-Agent", userAgent)
		resp, err := (&http.Client{Timeout: 10 * time.Minute}).Do(req)
		must(err, "download "+url)
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			log.Fatalf("download %s: %s", url, resp.Status)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 512<<20))
		must(err, "download "+url)
		must(os.WriteFile(path, data, 0o644), "cache "+url)
	}
	h := sha256.Sum256(data)
	return data, hex.EncodeToString(h[:])
}

func writeJSON(path string, v any) {
	data, err := json.MarshalIndent(v, "", " ")
	must(err, "encode "+path)
	must(os.WriteFile(path, append(data, '\n'), 0o644), "write "+path)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// geoCache is banks.Cache on an append-only JSON-lines file.
type geoCache struct {
	f    *os.File
	seen map[string]geoEntry
}

type geoEntry struct {
	Key      string           `json:"key"`
	Results  []geocode.Result `json:"results"`
	NotFound bool             `json:"notFound"`
}

func openGeoCache(path string) (*geoCache, error) {
	c := &geoCache{seen: map[string]geoEntry{}}
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		for sc.Scan() {
			var e geoEntry
			if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
				f.Close()
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			c.seen[e.Key] = e
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	c.f = f
	return c, nil
}

func (c *geoCache) Get(key string) ([]geocode.Result, bool, bool) {
	e, ok := c.seen[key]
	return e.Results, e.NotFound, ok
}

func (c *geoCache) Put(key string, res []geocode.Result, notFound bool) error {
	e := geoEntry{Key: key, Results: res, NotFound: notFound}
	line, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := c.f.Write(append(line, '\n')); err != nil {
		return err
	}
	c.seen[key] = e
	return nil
}

func (c *geoCache) Close() error { return c.f.Close() }

// readJSON decodes path into v; the error wraps os.ErrNotExist when the file is missing.
func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}
