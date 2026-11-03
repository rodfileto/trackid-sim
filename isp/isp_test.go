package isp

import (
	"strings"
	"testing"
)

// csvFor builds a BaseDPEvolucaoMensalCisp-shaped file: every Columns column,
// set to 1 except where vals overrides it.
func csvFor(rows [][4]string, vals map[string]string) string {
	head := []string{"cisp", "mes", "ano", "mcirc"}
	for _, c := range Columns {
		head = append(head, c.Name)
	}
	var b strings.Builder
	b.WriteString(strings.Join(head, ";") + "\n")
	for _, r := range rows {
		cells := []string{r[0], r[1], r[2], r[3]}
		for _, c := range Columns {
			v, ok := vals[c.Name]
			if !ok {
				v = "1"
			}
			cells = append(cells, v)
		}
		b.WriteString(strings.Join(cells, ";") + "\n")
	}
	return b.String()
}

func TestParseCounts(t *testing.T) {
	rows := [][4]string{
		{"5", "11", "2025", "3304557"}, // outside the 2-month window
		{"5", "12", "2025", "3304557"},
		{"5", "1", "2026", "3304557"},
		{"9", "1", "2026", "3304557"},
		{"77", "1", "2026", "3303302"}, // another municipality
	}
	c, err := ParseCounts(strings.NewReader(csvFor(rows, map[string]string{"estelionato": ""})), "3304557", 2)
	if err != nil {
		t.Fatal(err)
	}
	if c.Period != [2]string{"2025-12", "2026-01"} || len(c.CISPs) != 2 {
		t.Fatalf("period %v, cisps %v", c.Period, c.CISPs)
	}
	for _, col := range c.Columns {
		switch col.Name {
		case "estelionato":
			if col.Total != 0 || col.Missing != 3 {
				t.Errorf("empty cells: total %d, missing %d", col.Total, col.Missing)
			}
		case "hom_doloso":
			if col.Total != 3 || col.ByCISP["5"] != 2 || col.ByCISP["9"] != 1 || col.Type != "HOMICIDE" {
				t.Errorf("hom_doloso: %+v", col)
			}
		}
	}

	broken := strings.Replace(csvFor(rows, nil), "roubo_banco", "roubo_agencia", 1)
	if _, err := ParseCounts(strings.NewReader(broken), "3304557", 2); err == nil {
		t.Error("a renamed ISP column was accepted")
	}
	if _, err := ParseCounts(strings.NewReader(csvFor(rows, nil)), "3304557", 5); err == nil {
		t.Error("asking for more months than exist was accepted")
	}
}
