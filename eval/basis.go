package eval

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/rodfileto/trackid/cluster"
	"github.com/rodfileto/trackid/db"
)

// BasisTally scores the identifications people made, by what settled each one
// (trackid's cluster.ReviewedLink): a lead from a review, a laudo at or above
// the identification level, or a laudo below it that only supports the link. A
// QUESTIONED feature counts once, under its strongest direct reviewed link to a
// KNOWN feature; it is correct when that KNOWN feature is its own human's.
type BasisTally struct {
	Modality string
	Basis    string // LEAD | LAUDO_IDENTIFIED | LAUDO_SUPPORTED
	Correct  int
	Wrong    int
}

func (b BasisTally) String() string {
	return fmt.Sprintf("%-11s %-17s correct=%d wrong-person=%d\n", b.Modality, b.Basis, b.Correct, b.Wrong)
}

func basisLabel(l cluster.Link) string {
	switch {
	case l.Basis == cluster.LinkLead:
		return "LEAD"
	case l.Identified:
		return "LAUDO_IDENTIFIED"
	}
	return "LAUDO_SUPPORTED"
}

func strength(l cluster.Link) int {
	switch basisLabel(l) {
	case "LAUDO_IDENTIFIED":
		return 3
	case "LEAD":
		return 2
	}
	return 1
}

// ScoreBasis reads every decision touching a QUESTIONED feature and tallies its
// identification by basis against the truth.
func ScoreBasis(ctx context.Context, sqlDB *sql.DB, features []Feature, isQuestioned func(string) bool) ([]BasisTally, error) {
	byID := make(map[string]Feature, len(features))
	var questioned []string
	for _, f := range features {
		byID[f.ID] = f
		if isQuestioned(f.ID) {
			questioned = append(questioned, f.ID)
		}
	}
	if len(questioned) == 0 {
		return nil, nil
	}
	q := db.New(sqlDB)
	level, err := cluster.IdentificationLevel(ctx, q)
	if err != nil {
		return nil, err
	}
	rows, err := q.ListBiometricDecisionsForFeatures(ctx, questioned)
	if err != nil {
		return nil, err
	}
	chains := map[[2]string][]cluster.ChainEntry{}
	for _, r := range rows {
		if isQuestioned(r.CounterpartID) {
			continue // a link between two questioned features is not an identification
		}
		k := [2]string{r.FeatureID, r.CounterpartID}
		chains[k] = append(chains[k], cluster.ChainEntry{Role: cluster.Role(r.Role), Decision: cluster.Decision(r.Decision),
			Analysis: r.CaseAnalysisID.Int64, AnalysisType: r.AnalysisType.String, Conclusion: cluster.Conclusion(r.Conclusion.String)})
	}
	type best struct {
		link  cluster.Link
		known string
	}
	strongest := map[string]best{}
	for k, chain := range chains {
		l, ok := cluster.ReviewedLink(chain, level)
		if !ok {
			continue
		}
		if b, seen := strongest[k[0]]; !seen || strength(l) > strength(b.link) {
			strongest[k[0]] = best{l, k[1]}
		}
	}
	tallies := map[[2]string]*BasisTally{}
	for feature, b := range strongest {
		f, ok := byID[feature]
		if !ok {
			continue
		}
		key := [2]string{f.Modality, basisLabel(b.link)}
		t := tallies[key]
		if t == nil {
			t = &BasisTally{Modality: f.Modality, Basis: key[1]}
			tallies[key] = t
		}
		if byID[b.known].HumanID == f.HumanID {
			t.Correct++
		} else {
			t.Wrong++
		}
	}
	out := make([]BasisTally, 0, len(tallies))
	for _, t := range tallies {
		out = append(out, *t)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.Compare(out[i].Modality+out[i].Basis, out[j].Modality+out[j].Basis) < 0
	})
	return out, nil
}
