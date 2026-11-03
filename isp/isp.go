// Package isp reads the open data of ISP-RJ (Instituto de Segurança Pública do
// Rio de Janeiro): monthly occurrence counts per police-station area (CISP), and
// the CISP boundaries. Occurrence types follow ISP's classification (trackid's
// db/migrations/003_occurrences.sql); Columns maps each ISP column to its type.
//
// Neither file states a licence (nor does ISP's Open Data Plan): ISP is cited as
// the source, and the licence is recorded as unconfirmed.
package isp

import (
	"encoding/csv"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/encoding/charmap"
)

const (
	CountsURL = "https://www.ispdados.rj.gov.br/Arquivos/BaseDPEvolucaoMensalCisp.csv"
	ShapesURL = "https://www.ispdados.rj.gov.br/Arquivos/CISPshp.rar"

	Source = "ISP-RJ (Instituto de Segurança Pública do Rio de Janeiro), ispdados.rj.gov.br"
	// Licence is what we know of the terms: nothing published.
	Licence = "none stated by ISP-RJ (checked 2026-10-12, incl. its Plano de Dados Abertos); cited as source"
)

// Column is one ISP category and the occurrence type it counts.
type Column struct {
	Name      string `json:"name"`                // ISP's column
	Type      string `json:"type"`                // occurrence_types.code
	Attempted bool   `json:"attempted,omitempty"` // counts attempts (occurrences.attempted)
}

// Columns are the ISP categories that are occurrence types. Left out: ISP's
// totals (cvli, letalidade_violenta, roubo_rua, total_roubos, total_furtos,
// registro_ocorrencias), police productivity (apreensao_drogas,
// apreensao_drogas_sem_autor, recuperacao_veiculos, apf, aaapai, cmp, cmba) and
// police killed on duty (within the lethal types).
var Columns = []Column{
	{"hom_doloso", "HOMICIDE", false},
	{"tentat_hom", "HOMICIDE", true},
	{"feminicidio", "FEMICIDE", false},
	{"tentativa_feminicidio", "FEMICIDE", true},
	{"latrocinio", "ROBBERY_HOMICIDE", false},
	{"lesao_corp_morte", "BODILY_INJURY_DEATH", false},
	{"hom_por_interv_policial", "POLICE_INTERVENTION_DEATH", false},
	{"hom_culposo", "NEGLIGENT_HOMICIDE", false},
	{"lesao_corp_dolosa", "ASSAULT", false},
	{"lesao_corp_culposa", "NEGLIGENT_BODILY_INJURY", false},
	{"estupro", "RAPE", false},
	{"sequestro", "KIDNAPPING", false},
	{"sequestro_relampago", "EXPRESS_KIDNAPPING", false},
	{"extorsao", "EXTORTION", false},
	{"ameaca", "THREAT", false},
	{"roubo_transeunte", "STREET_ROBBERY", false},
	{"roubo_celular", "PHONE_ROBBERY", false},
	{"roubo_em_coletivo", "PUBLIC_TRANSPORT_ROBBERY", false},
	{"roubo_veiculo", "VEHICLE_ROBBERY", false},
	{"roubo_carga", "CARGO_ROBBERY", false},
	{"roubo_comercio", "COMMERCIAL_ROBBERY", false},
	{"roubo_residencia", "HOME_ROBBERY", false},
	{"roubo_banco", "BANK_ROBBERY", false},
	{"roubo_cx_eletronico", "ATM_ROBBERY", false},
	{"roubo_conducao_saque", "FORCED_WITHDRAWAL_ROBBERY", false},
	{"roubo_apos_saque", "POST_WITHDRAWAL_ROBBERY", false},
	{"roubo_bicicleta", "BICYCLE_ROBBERY", false},
	{"outros_roubos", "OTHER_ROBBERY", false},
	{"furto_veiculos", "VEHICLE_THEFT", false},
	{"furto_transeunte", "STREET_THEFT", false},
	{"furto_coletivo", "PUBLIC_TRANSPORT_THEFT", false},
	{"furto_celular", "PHONE_THEFT", false},
	{"furto_bicicleta", "BICYCLE_THEFT", false},
	{"outros_furtos", "OTHER_THEFT", false},
	{"estelionato", "FRAUD", false},
	{"posse_drogas", "DRUG_POSSESSION", false},
	{"trafico_drogas", "DRUG_TRAFFICKING", false},
	{"pessoas_desaparecidas", "MISSING_PERSON", false},
	{"encontro_cadaver", "BODY_FOUND", false},
	{"encontro_ossada", "SKELETAL_REMAINS_FOUND", false},
}

// Counts is one municipality's counts over a period, per ISP column and CISP.
type Counts struct {
	Source       string         `json:"source"`
	Licence      string         `json:"licence"`
	URL          string         `json:"url"`
	SHA256       string         `json:"sha256"`
	Fetched      string         `json:"fetched"`
	Municipality string         `json:"municipality"` // IBGE code
	Period       [2]string      `json:"period"`       // first and last month, "YYYY-MM"
	Months       int            `json:"months"`
	CISPs        []int          `json:"cisps"`
	Note         string         `json:"note"`
	Columns      []ColumnCounts `json:"columns"`
}

// ColumnCounts is one ISP column's total and its split over CISPs.
type ColumnCounts struct {
	Column
	Total int `json:"total"`
	// ByCISP is keyed by the CISP number as a string (JSON object keys).
	ByCISP map[string]int `json:"byCisp"`
	// Missing counts the (CISP, month) cells left empty, read as 0.
	Missing int `json:"missing,omitempty"`
}

// ParseCounts reads BaseDPEvolucaoMensalCisp.csv (Latin-1, ';'-separated) and
// sums every column in Columns over the last months months present for
// municipality (an IBGE code, ISP's mcirc). It fails if a column is missing,
// so a renamed ISP column cannot silently become zeros.
func ParseCounts(r io.Reader, municipality string, months int) (*Counts, error) {
	cr := csv.NewReader(charmap.ISO8859_1.NewDecoder().Reader(r))
	cr.Comma = ';'
	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("isp: header: %w", err)
	}
	col := map[string]int{}
	for i, h := range header {
		col[h] = i
	}
	for _, need := range []string{"cisp", "mes", "ano", "mcirc"} {
		if _, ok := col[need]; !ok {
			return nil, fmt.Errorf("isp: no %q column", need)
		}
	}
	for _, c := range Columns {
		if _, ok := col[c.Name]; !ok {
			return nil, fmt.Errorf("isp: no %q column: ISP's classification changed", c.Name)
		}
	}

	type row struct {
		month string
		cisp  int
		cells []string
	}
	var rows []row
	present := map[string]bool{}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("isp: %w", err)
		}
		if rec[col["mcirc"]] != municipality {
			continue
		}
		year, err1 := strconv.Atoi(rec[col["ano"]])
		mon, err2 := strconv.Atoi(rec[col["mes"]])
		cisp, err3 := strconv.Atoi(rec[col["cisp"]])
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("isp: bad ano/mes/cisp in %v", rec[:4])
		}
		m := fmt.Sprintf("%04d-%02d", year, mon)
		present[m] = true
		rows = append(rows, row{m, cisp, rec})
	}
	var all []string
	for m := range present {
		all = append(all, m)
	}
	sort.Strings(all)
	if len(all) < months {
		return nil, fmt.Errorf("isp: %d months for municipality %s, want %d", len(all), municipality, months)
	}
	keep := map[string]bool{}
	for _, m := range all[len(all)-months:] {
		keep[m] = true
	}

	out := &Counts{
		Source: Source, Licence: Licence, URL: CountsURL, Municipality: municipality,
		Period: [2]string{all[len(all)-months], all[len(all)-1]}, Months: months,
		Note: "Sums of ISP-RJ's monthly counts per CISP, computed by trackid-sim (isp package). " +
			"ISP-RJ is the source of the underlying data and is not responsible for this transformation.",
	}
	cisps := map[int]bool{}
	byCol := make([]ColumnCounts, len(Columns))
	for i, c := range Columns {
		byCol[i] = ColumnCounts{Column: c, ByCISP: map[string]int{}}
	}
	for _, r := range rows {
		if !keep[r.month] {
			continue
		}
		cisps[r.cisp] = true
		for i, c := range Columns {
			cell := strings.TrimSpace(r.cells[col[c.Name]])
			if cell == "" {
				byCol[i].Missing++
				continue
			}
			n, err := strconv.Atoi(cell)
			if err != nil {
				return nil, fmt.Errorf("isp: %s = %q (cisp %d, %s)", c.Name, cell, r.cisp, r.month)
			}
			byCol[i].Total += n
			byCol[i].ByCISP[strconv.Itoa(r.cisp)] += n
		}
	}
	for c := range cisps {
		out.CISPs = append(out.CISPs, c)
	}
	sort.Ints(out.CISPs)
	out.Columns = byCol
	return out, nil
}
