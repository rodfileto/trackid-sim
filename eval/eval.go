// Package eval scores a clustering against ground truth. It needs only a
// feature -> cluster assignment and the features' true humans, so it is
// independent of how the clusters were produced.
package eval

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

// Modalities, as biometric_decisions.modality spells them.
const (
	Face        = "FACIAL"
	Fingerprint = "FINGERPRINT"
)

// Feature is one biometric feature in the database, by its graph feature id,
// with the human it truly belongs to (ground truth from the world file).
type Feature struct {
	ID       string
	HumanID  string
	Modality string
}

// Snapshot reads the persisted feature -> cluster id assignment of one cluster
// layer (cluster_members.basis): trackid keeps an AUTOMATIC layer, from every
// confirmed pair, and a REVIEWED one, from the pairs people confirmed. A feature
// sits in one cluster per layer.
func Snapshot(ctx context.Context, sqlDB *sql.DB, basis string) (map[string]int64, error) {
	rows, err := sqlDB.QueryContext(ctx, `SELECT feature_id, cluster_id FROM cluster_members WHERE basis = $1`, basis)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var f string
		var c int64
		if err := rows.Scan(&f, &c); err != nil {
			return nil, err
		}
		out[f] = c
	}
	return out, rows.Err()
}

// Merges reads one layer's cluster_merges as from -> to.
func Merges(ctx context.Context, sqlDB *sql.DB, basis string) (map[int64]int64, error) {
	rows, err := sqlDB.QueryContext(ctx, `SELECT from_cluster_id, to_cluster_id FROM cluster_merges WHERE basis = $1`, basis)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var f, t int64
		if err := rows.Scan(&f, &t); err != nil {
			return nil, err
		}
		out[f] = t
	}
	return out, rows.Err()
}

// Metrics scores one modality.
type Metrics struct {
	Modality string
	Features int
	Clusters int // distinct clusters, singletons included
	Singles  int // clusters holding one feature

	// Pairwise: over all feature pairs, "same cluster" vs "same human".
	TP, FP, FN            int64
	Precision, Recall, F1 float64
	ImpureClusters        int // clusters holding >1 human (false merges)
	FragmentedHumans      int // humans with >=2 features split over >1 cluster
	MultiFeatureHumans    int

	// Identification of QUESTIONED features whose human has a KNOWN feature of this
	// modality: is the cluster tied to that human?
	Identifiable                     int
	Correct, WrongPerson, Unresolved int
	// Unknown subjects (no KNOWN feature) wrongly landing in a cluster with a KNOWN
	// feature -- the "unknown person reported as person X" error.
	UnknownSubjects, FalselyLinked int
}

func pairs(n int) int64 { return int64(n) * int64(n-1) / 2 }

// Evaluate scores assign (feature id -> cluster id) against the features' humans.
// A feature absent from assign counts as its own singleton cluster.
func Evaluate(features []Feature, assign map[string]int64, isQuestioned func(string) bool) []Metrics {
	var out []Metrics
	for _, modality := range []string{Face, Fingerprint} {
		out = append(out, evaluateOne(modality, features, assign, isQuestioned))
	}
	return out
}

func evaluateOne(modality string, all []Feature, assign map[string]int64, isQuestioned func(string) bool) Metrics {
	m := Metrics{Modality: modality}

	type cell struct {
		cluster int64
		human   string
	}
	clusterSize := map[int64]int{}
	humanSize := map[string]int{}
	cells := map[cell]int{}
	clusterHumans := map[int64]map[string]bool{}
	knownHumansIn := map[int64]map[string]bool{}
	humanClusters := map[string]map[int64]bool{}
	hasKnown := map[string]bool{}

	var fs []Feature
	next := int64(-1) // synthetic ids for unassigned features, disjoint from real (positive) ids
	cid := map[string]int64{}
	for _, f := range all {
		if f.Modality != modality {
			continue
		}
		fs = append(fs, f)
		c, ok := assign[f.ID]
		if !ok {
			c = next
			next--
		}
		cid[f.ID] = c
		if !isQuestioned(f.ID) {
			hasKnown[f.HumanID] = true
		}
	}

	for _, f := range fs {
		c := cid[f.ID]
		clusterSize[c]++
		humanSize[f.HumanID]++
		cells[cell{c, f.HumanID}]++
		if clusterHumans[c] == nil {
			clusterHumans[c] = map[string]bool{}
		}
		clusterHumans[c][f.HumanID] = true
		if humanClusters[f.HumanID] == nil {
			humanClusters[f.HumanID] = map[int64]bool{}
		}
		humanClusters[f.HumanID][c] = true
		if !isQuestioned(f.ID) {
			if knownHumansIn[c] == nil {
				knownHumansIn[c] = map[string]bool{}
			}
			knownHumansIn[c][f.HumanID] = true
		}
	}

	m.Features = len(fs)
	m.Clusters = len(clusterSize)
	var predicted, actual int64
	for c, n := range clusterSize {
		predicted += pairs(n)
		if n == 1 {
			m.Singles++
		}
		if len(clusterHumans[c]) > 1 {
			m.ImpureClusters++
		}
	}
	for h, n := range humanSize {
		actual += pairs(n)
		if n >= 2 {
			m.MultiFeatureHumans++
			if len(humanClusters[h]) > 1 {
				m.FragmentedHumans++
			}
		}
	}
	for _, n := range cells {
		m.TP += pairs(n)
	}
	m.FP, m.FN = predicted-m.TP, actual-m.TP
	m.Precision, m.Recall = ratio(m.TP, predicted), ratio(m.TP, actual)
	if m.Precision+m.Recall > 0 {
		m.F1 = 2 * m.Precision * m.Recall / (m.Precision + m.Recall)
	}

	for _, f := range fs {
		if !isQuestioned(f.ID) {
			continue
		}
		known := knownHumansIn[cid[f.ID]]
		if hasKnown[f.HumanID] {
			m.Identifiable++
			switch {
			case known[f.HumanID]:
				m.Correct++
			case len(known) > 0:
				m.WrongPerson++
			default:
				m.Unresolved++
			}
		} else {
			m.UnknownSubjects++
			if len(known) > 0 {
				m.FalselyLinked++
			}
		}
	}
	return m
}

func ratio(a, b int64) float64 {
	if b == 0 {
		return 1 // nothing to get wrong / nothing to find
	}
	return float64(a) / float64(b)
}

func (m Metrics) String() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%-11s features=%d clusters=%d (singletons=%d)\n", m.Modality, m.Features, m.Clusters, m.Singles)
	fmt.Fprintf(&b, "  pairwise   precision=%.4f recall=%.4f F1=%.4f  (TP=%d FP=%d FN=%d)\n", m.Precision, m.Recall, m.F1, m.TP, m.FP, m.FN)
	fmt.Fprintf(&b, "  structure  impure clusters=%d  fragmented humans=%d of %d with >=2 features\n", m.ImpureClusters, m.FragmentedHumans, m.MultiFeatureHumans)
	fmt.Fprintf(&b, "  identify   %d identifiable traces: correct=%d wrong-person=%d unresolved=%d\n", m.Identifiable, m.Correct, m.WrongPerson, m.Unresolved)
	fmt.Fprintf(&b, "  unknowns   %d unknown-subject traces: %d falsely linked to an enrolled person\n", m.UnknownSubjects, m.FalselyLinked)
	return b.String()
}

// Stability compares two snapshots for the referential-stability invariant: a
// feature's cluster id may change only because its old cluster was merged into
// another (recorded in cluster_merges) or split. trackid records no split: the
// part holding the cluster's anchor keeps the id and the rest get new ones, so a
// feature counts as split off when its old cluster still exists and its new one
// did not. Anything else is an unexplained change.
type Stability struct {
	Compared    int
	Unchanged   int
	ViaMerge    int // changed, and cluster_merges explains it
	SplitOff    int // left a cluster that still exists, for a new one
	Splits      int // clusters that features split off from
	Unexplained int // changed with no merge or split -- would break a cited cluster number
	NewFeatures int
}

func CompareSnapshots(prev, cur map[string]int64, merges map[int64]int64) Stability {
	var s Stability
	prevIDs, curIDs := map[int64]bool{}, map[int64]bool{}
	for _, c := range prev {
		prevIDs[c] = true
	}
	for _, c := range cur {
		curIDs[c] = true
	}
	split := map[int64]bool{}
	ids := make([]string, 0, len(cur))
	for f := range cur {
		ids = append(ids, f)
	}
	sort.Strings(ids)
	for _, f := range ids {
		p, ok := prev[f]
		if !ok {
			s.NewFeatures++
			continue
		}
		s.Compared++
		c := cur[f]
		switch {
		case p == c:
			s.Unchanged++
		case follow(merges, p) == c:
			s.ViaMerge++
		case curIDs[p] && !prevIDs[c]:
			s.SplitOff++
			split[p] = true
		default:
			s.Unexplained++
		}
	}
	s.Splits = len(split)
	return s
}

func (s Stability) String() string {
	return fmt.Sprintf("  stability  %d features compared: unchanged=%d via-merge=%d split-off=%d (from %d clusters) unexplained=%d; %d new\n",
		s.Compared, s.Unchanged, s.ViaMerge, s.SplitOff, s.Splits, s.Unexplained, s.NewFeatures)
}

// follow resolves a cluster id through the merge chain (bounded to avoid cycles).
func follow(merges map[int64]int64, id int64) int64 {
	for i := 0; i < 1000; i++ {
		next, ok := merges[id]
		if !ok || next == id {
			return id
		}
		id = next
	}
	return id
}
