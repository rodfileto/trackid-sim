package eval

// Planted is one pair the class's planting account decided (trackid-sim plant), as its record
// keeps it: the truth, what it decided, and whether that was wrong on purpose.
type Planted struct {
	AnalysisID int64
	CaseID     string
	A, B       string
	Genuine    bool
	Decision   string
	Wrong      bool
}

// Right is the decision the truth calls for.
func (p Planted) Right() string {
	if p.Genuine {
		return Positive
	}
	return Negative
}

// PlantedOutcome is what the class made of one planted pair: the blind verifier's decision, the
// third examiner's when the two disagreed, and where the pair ended.
type PlantedOutcome struct {
	Planted
	Verifier, VerifierDecision string // "" until verified
	Third, ThirdDecision       string // "" unless the pair went to inconsistence and was decided
	// Final is the pair's settled decision in this analysis: the third examiner's, or the one
	// planter and verifier agreed on; "" while it is still open.
	Final string
}

// Disputed reports whether the verifier disagreed with the planted decision: for a wrong one, the
// error was caught.
func (o PlantedOutcome) Disputed() bool {
	return o.VerifierDecision != "" && o.VerifierDecision != o.Decision
}

// SettledRight reports whether the pair ended on the truth.
func (o PlantedOutcome) SettledRight() bool { return o.Final != "" && o.Final == o.Right() }

// PlantedOutcomes follows each planted pair through the class's decisions in its analysis.
func PlantedOutcomes(planted []Planted, ds []Decision) []PlantedOutcome {
	type key struct {
		analysis int64
		a, b     string
		role     string
	}
	byRole := map[key]Decision{}
	for _, d := range ds {
		byRole[key{d.AnalysisID, d.A, d.B, d.Role}] = d
	}
	out := make([]PlantedOutcome, len(planted))
	for i, p := range planted {
		o := PlantedOutcome{Planted: p}
		if d, ok := byRole[key{p.AnalysisID, p.A, p.B, "REVIEWER"}]; ok {
			o.Verifier, o.VerifierDecision = d.Username, d.Decision
		}
		if d, ok := byRole[key{p.AnalysisID, p.A, p.B, "INCONSISTENCE"}]; ok {
			o.Third, o.ThirdDecision = d.Username, d.Decision
		}
		switch {
		case o.ThirdDecision != "":
			o.Final = o.ThirdDecision
		case o.VerifierDecision != "" && !o.Disputed():
			o.Final = o.Decision
		}
		out[i] = o
	}
	return out
}
