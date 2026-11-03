package eval

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Decision values, as biometric_decisions.decision spells them.
const (
	Positive     = "POSITIVE"
	Inconclusive = "INCONCLUSIVE"
	Negative     = "NEGATIVE"
)

// NoSystem is the band of a pair the matcher never decided (e.g. one an admin
// listed for a comparison).
const NoSystem = "NONE"

// Decision is one human decision (VERIFICATOR, REVIEWER or INCONSISTENCE) as
// trackid stores it, with the analysis it was made in and the matcher's own
// decision on the same pair, its band.
type Decision struct {
	A, B         string // graph feature ids
	Modality     string
	Role         string
	Decision     string
	Username     string
	AnalysisID   int64  // 0 for a decision made outside any analysis
	AnalysisType string // "" for a decision made outside any analysis
	System       string // the pair's SYSTEM decision (POSITIVE, INCONCLUSIVE), or NoSystem
}

// Decisions reads every human decision in biometric_decisions.
func Decisions(ctx context.Context, sqlDB *sql.DB) ([]Decision, error) {
	rows, err := sqlDB.QueryContext(ctx, `
		SELECT d.feature_a_id, d.feature_b_id, d.modality, d.role, d.decision, d.username,
		       COALESCE(d.case_analysis_id, 0), COALESCE(a.analysis_type, ''), COALESCE(s.decision, $1)
		FROM biometric_decisions d
		LEFT JOIN case_analyses a ON a.id = d.case_analysis_id
		LEFT JOIN biometric_decisions s
		       ON s.feature_a_id = d.feature_a_id AND s.feature_b_id = d.feature_b_id AND s.role = 'SYSTEM'
		WHERE d.role <> 'SYSTEM'
		ORDER BY d.id`, NoSystem)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Decision
	for rows.Next() {
		var d Decision
		if err := rows.Scan(&d.A, &d.B, &d.Modality, &d.Role, &d.Decision, &d.Username,
			&d.AnalysisID, &d.AnalysisType, &d.System); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Outcomes counts decisions by value.
type Outcomes struct{ Positive, Inconclusive, Negative int }

func (o Outcomes) Total() int { return o.Positive + o.Inconclusive + o.Negative }

func (o *Outcomes) add(decision string) {
	switch decision {
	case Positive:
		o.Positive++
	case Inconclusive:
		o.Inconclusive++
	case Negative:
		o.Negative++
	}
}

// Tally scores a group of decisions against the truth: Genuine pairs join one
// human (POSITIVE is right, NEGATIVE a false exclusion), Impostor pairs two
// (NEGATIVE is right, POSITIVE a false identification). Unscored counts
// decisions on a feature the world does not know (made in the UI, not loaded).
type Tally struct {
	Genuine, Impostor Outcomes
	Unscored          int
}

// FalseIdentification is the share of impostor pairs decided POSITIVE.
func (t Tally) FalseIdentification() float64 {
	return share(t.Impostor.Positive, t.Impostor.Total())
}

// FalseExclusion is the share of genuine pairs decided NEGATIVE.
func (t Tally) FalseExclusion() float64 { return share(t.Genuine.Negative, t.Genuine.Total()) }

// DecisionReport scores one modality's human decisions.
type DecisionReport struct {
	Modality   string
	ByExaminer map[string]*Tally // username
	ByRole     map[string]*Tally // VERIFICATOR, REVIEWER, INCONSISTENCE
	ByBand     map[string]*Tally // the pair's SYSTEM decision, or NoSystem

	// Pairs decided by both a VERIFICATOR and a REVIEWER in one analysis, and how
	// many of them the two decided alike.
	Verified, Agreed int
}

// ScoreDecisions scores ds against the features' humans.
func ScoreDecisions(features []Feature, ds []Decision) []DecisionReport {
	human := make(map[string]string, len(features))
	for _, f := range features {
		human[f.ID] = f.HumanID
	}
	var out []DecisionReport
	for _, modality := range []string{Face, Fingerprint} {
		r := DecisionReport{
			Modality:   modality,
			ByExaminer: map[string]*Tally{},
			ByRole:     map[string]*Tally{},
			ByBand:     map[string]*Tally{},
		}
		type round struct {
			analysis int64
			a, b     string
		}
		verificator, reviewer := map[round]string{}, map[round]string{}
		for _, d := range ds {
			if d.Modality != modality {
				continue
			}
			ha, okA := human[d.A]
			hb, okB := human[d.B]
			for _, t := range []*Tally{
				tally(r.ByExaminer, d.Username), tally(r.ByRole, d.Role), tally(r.ByBand, d.System),
			} {
				switch {
				case !okA || !okB:
					t.Unscored++
				case ha == hb:
					t.Genuine.add(d.Decision)
				default:
					t.Impostor.add(d.Decision)
				}
			}
			if d.AnalysisID == 0 {
				continue
			}
			k := round{d.AnalysisID, d.A, d.B}
			switch d.Role {
			case "VERIFICATOR":
				verificator[k] = d.Decision
			case "REVIEWER":
				reviewer[k] = d.Decision
			}
		}
		for k, v := range verificator {
			if rv, ok := reviewer[k]; ok {
				r.Verified++
				if rv == v {
					r.Agreed++
				}
			}
		}
		out = append(out, r)
	}
	return out
}

func tally(m map[string]*Tally, key string) *Tally {
	t, ok := m[key]
	if !ok {
		t = &Tally{}
		m[key] = t
	}
	return t
}

func share(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return float64(a) / float64(b)
}

func (r DecisionReport) String() string {
	var b strings.Builder
	n := 0
	for _, t := range r.ByRole {
		n += t.Genuine.Total() + t.Impostor.Total() + t.Unscored
	}
	fmt.Fprintf(&b, "%-11s %d human decisions\n", r.Modality, n)
	if n == 0 {
		return b.String()
	}
	fmt.Fprintf(&b, "  %-24s %-26s %-26s %8s %8s %s\n", "", "genuine pos/inc/neg", "impostor pos/inc/neg", "false id", "false ex", "unscored")
	section := func(title string, m map[string]*Tally) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "  %s\n", title)
		for _, k := range keys {
			t := m[k]
			fmt.Fprintf(&b, "    %-22s %-26s %-26s %8.3f %8.3f %d\n", k,
				fmt.Sprintf("%d: %d/%d/%d", t.Genuine.Total(), t.Genuine.Positive, t.Genuine.Inconclusive, t.Genuine.Negative),
				fmt.Sprintf("%d: %d/%d/%d", t.Impostor.Total(), t.Impostor.Positive, t.Impostor.Inconclusive, t.Impostor.Negative),
				t.FalseIdentification(), t.FalseExclusion(), t.Unscored)
		}
	}
	section("by examiner", r.ByExaminer)
	section("by role", r.ByRole)
	section("by SYSTEM band", r.ByBand)
	fmt.Fprintf(&b, "  verification  %d pairs decided by both VERIFICATOR and REVIEWER: %d agreed (%.3f)\n",
		r.Verified, r.Agreed, share(r.Agreed, r.Verified))
	return b.String()
}
