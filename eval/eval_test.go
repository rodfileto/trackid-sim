package eval

import (
	"strings"
	"testing"
)

func q(id string) bool { return strings.HasPrefix(id, "TRACE:") }

func f(id, human string) Feature {
	return Feature{ID: id, HumanID: human, Modality: Face}
}

func TestPerfectClustering(t *testing.T) {
	fs := []Feature{f("1", "A"), f("TRACE:1#feature", "A"), f("TRACE:2#feature", "A"), f("2", "B"), f("TRACE:3#feature", "B")}
	assign := map[string]int64{"1": 10, "TRACE:1#feature": 10, "TRACE:2#feature": 10, "2": 20, "TRACE:3#feature": 20}
	m := Evaluate(fs, assign, q)[0]
	if m.Precision != 1 || m.Recall != 1 || m.ImpureClusters != 0 || m.FragmentedHumans != 0 {
		t.Fatalf("perfect clustering scored %+v", m)
	}
	if m.Identifiable != 3 || m.Correct != 3 {
		t.Fatalf("all 3 traces should identify correctly: %+v", m)
	}
}

func TestFalseMergeAndSplit(t *testing.T) {
	// A's features split across two clusters; B's lone feature merged into one of them.
	fs := []Feature{f("1", "A"), f("2", "A"), f("3", "A"), f("4", "B")}
	assign := map[string]int64{"1": 1, "2": 1, "3": 2, "4": 2}
	m := Evaluate(fs, assign, q)[0]
	// predicted pairs: {1,2} and {3,4}; true pairs: {1,2},{1,3},{2,3}; TP = {1,2} only.
	if m.TP != 1 || m.FP != 1 || m.FN != 2 {
		t.Fatalf("TP/FP/FN = %d/%d/%d, want 1/1/2", m.TP, m.FP, m.FN)
	}
	if m.ImpureClusters != 1 || m.FragmentedHumans != 1 {
		t.Fatalf("impure=%d fragmented=%d, want 1/1", m.ImpureClusters, m.FragmentedHumans)
	}
}

func TestUnassignedFeaturesAreSingletons(t *testing.T) {
	fs := []Feature{f("1", "A"), f("2", "A")}
	m := Evaluate(fs, map[string]int64{}, q)[0]
	if m.Clusters != 2 || m.Singles != 2 || m.Recall != 0 {
		t.Fatalf("unassigned features must be singletons: %+v", m)
	}
}

func TestIdentificationOutcomes(t *testing.T) {
	fs := []Feature{
		f("1", "A"), f("TRACE:1#feature", "A"), // correct
		f("2", "B"), f("TRACE:2#feature", "B"), // wrong person: B's trace sits with C's known
		f("3", "C"),
		f("4", "D"), f("TRACE:4#feature", "D"), // unresolved: D's trace not clustered with 4
		f("TRACE:5#feature", "Z"), // unknown subject falsely linked to E
		f("5", "E"),
	}
	assign := map[string]int64{
		"1": 1, "TRACE:1#feature": 1,
		"3": 2, "TRACE:2#feature": 2, "2": 9,
		"4": 4, "TRACE:4#feature": 5,
		"5": 6, "TRACE:5#feature": 6,
	}
	m := Evaluate(fs, assign, q)[0]
	if m.Correct != 1 || m.WrongPerson != 1 || m.Unresolved != 1 {
		t.Fatalf("correct/wrong/unresolved = %d/%d/%d, want 1/1/1", m.Correct, m.WrongPerson, m.Unresolved)
	}
	if m.UnknownSubjects != 1 || m.FalselyLinked != 1 {
		t.Fatalf("unknown=%d falselyLinked=%d, want 1/1", m.UnknownSubjects, m.FalselyLinked)
	}
}

func TestStability(t *testing.T) {
	prev := map[string]int64{"a": 1, "b": 2, "c": 3, "d": 4}
	cur := map[string]int64{"a": 1, "b": 1, "c": 9, "d": 4, "e": 5}
	// cluster 2 merged into 1 (explains b); c moved 3 -> 9 with no merge (unexplained).
	s := CompareSnapshots(prev, cur, map[int64]int64{2: 1})
	if s.Unchanged != 2 || s.ViaMerge != 1 || s.Unexplained != 1 || s.NewFeatures != 1 {
		t.Fatalf("stability = %+v", s)
	}
	// Chained merges resolve transitively.
	s = CompareSnapshots(map[string]int64{"x": 3}, map[string]int64{"x": 1}, map[int64]int64{3: 2, 2: 1})
	if s.ViaMerge != 1 {
		t.Fatalf("chained merge not followed: %+v", s)
	}
}

func TestStabilitySplit(t *testing.T) {
	// Cluster 1 {a,b,c} splits: a keeps 1 (its anchor), b and c split off into
	// the new cluster 7. d moves to 1, which existed before: a move a split
	// does not explain.
	prev := map[string]int64{"a": 1, "b": 1, "c": 1, "d": 2}
	cur := map[string]int64{"a": 1, "b": 7, "c": 7, "d": 1}
	s := CompareSnapshots(prev, cur, nil)
	if s.Unchanged != 1 || s.SplitOff != 2 || s.Splits != 1 || s.Unexplained != 1 {
		t.Fatalf("stability = %+v", s)
	}
}

func TestScoreDecisions(t *testing.T) {
	features := []Feature{
		{ID: "1", HumanID: "H1", Modality: Face},
		{ID: "TRACE:1#feature", HumanID: "H1", Modality: Face},
		{ID: "2", HumanID: "H2", Modality: Face},
	}
	genuine, impostor := [2]string{"1", "TRACE:1#feature"}, [2]string{"2", "TRACE:1#feature"}
	d := func(pair [2]string, role, decision, user string, analysis int64, system string) Decision {
		return Decision{A: pair[0], B: pair[1], Modality: Face, Role: role, Decision: decision,
			Username: user, AnalysisID: analysis, System: system}
	}
	ds := []Decision{
		d(genuine, "VERIFICATOR", Positive, "ana", 1, Positive),
		d(genuine, "REVIEWER", Negative, "bia", 1, Positive),
		d(genuine, "INCONSISTENCE", Positive, "caio", 1, Positive),
		d(impostor, "VERIFICATOR", Positive, "ana", 1, Inconclusive),
		d(impostor, "REVIEWER", Positive, "bia", 1, Inconclusive),
		d([2]string{"1", "TRACE:99#feature"}, "VERIFICATOR", Negative, "ana", 2, NoSystem),
	}
	r := ScoreDecisions(features, ds)[0]
	ana := r.ByExaminer["ana"]
	if ana.Genuine.Positive != 1 || ana.Impostor.Positive != 1 || ana.Unscored != 1 || ana.FalseIdentification() != 1 {
		t.Fatalf("ana = %+v", *ana)
	}
	if bia := r.ByExaminer["bia"]; bia.FalseExclusion() != 1 || bia.FalseIdentification() != 1 {
		t.Fatalf("bia = %+v", *bia)
	}
	if band := r.ByBand[Inconclusive]; band.Impostor.Positive != 2 || band.Genuine.Total() != 0 {
		t.Fatalf("INCONCLUSIVE band = %+v", *band)
	}
	if r.Verified != 2 || r.Agreed != 1 {
		t.Fatalf("verified=%d agreed=%d", r.Verified, r.Agreed)
	}
}

func TestPlantedOutcomes(t *testing.T) {
	g := Planted{AnalysisID: 1, A: "1", B: "TRACE:1#feature", Genuine: true, Decision: Negative, Wrong: true}
	i := Planted{AnalysisID: 1, A: "2", B: "TRACE:1#feature", Genuine: false, Decision: Negative}
	m := Planted{AnalysisID: 2, A: "3", B: "TRACE:2#feature", Genuine: false, Decision: Positive, Wrong: true}
	d := func(p Planted, role, decision, user string) Decision {
		return Decision{A: p.A, B: p.B, AnalysisID: p.AnalysisID, Role: role, Decision: decision, Username: user}
	}
	out := PlantedOutcomes([]Planted{g, i, m}, []Decision{
		d(g, "REVIEWER", Positive, "e01"), d(g, "INCONSISTENCE", Positive, "e02"), // caught, settled right
		d(i, "REVIEWER", Negative, "e01"), // agreed on a right decision
		d(m, "REVIEWER", Positive, "e03"), // missed: agreed with the planted error
	})
	if !out[0].Disputed() || out[0].Final != Positive || !out[0].SettledRight() || out[0].Third != "e02" {
		t.Fatalf("caught error: %+v", out[0])
	}
	if out[1].Disputed() || out[1].Final != Negative || !out[1].SettledRight() {
		t.Fatalf("right decision agreed: %+v", out[1])
	}
	if out[2].Disputed() || out[2].Final != Positive || out[2].SettledRight() {
		t.Fatalf("missed error: %+v", out[2])
	}
	if open := PlantedOutcomes([]Planted{g}, nil)[0]; open.Final != "" || open.Disputed() {
		t.Fatalf("unverified pair: %+v", open)
	}
}
