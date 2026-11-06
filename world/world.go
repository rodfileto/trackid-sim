// Package world generates the synthetic population: who exists, who is enrolled
// (and how messily), who offends, and what traces their offending leaves in
// cases. It is pure -- no database access -- and fully determined by Config.Seed.
//
// GROUND TRUTH. HumanID fields (Enrollment.HumanID, Trace.HumanID) say which real
// human each record belongs to. They exist so the decision simulator and the
// evaluator can score the system; the loader NEVER writes them to Postgres.
package world

import (
	"encoding/json"
	"fmt"
	"os"
)

// Enrollment kinds (ground truth about how a record relates to its human).
const (
	KindPrimary   = "primary"   // the human's first enrollment
	KindRenewal   = "renewal"   // a later document filed under the SAME person
	KindDuplicate = "duplicate" // the same human enrolled again under a DIFFERENT person: an RG from another state, normal
	KindImpostor  = "impostor"  // a document copying another human's data with this human's biometrics, on a person of its own
)

type Human struct {
	ID          string `json:"id"`
	Sex         string `json:"sex"`
	BirthDate   string `json:"birthDate"`
	Name        string `json:"name"`
	Mother      string `json:"mother"`
	Father      string `json:"father"` // empty when undeclared
	FatherKnown bool   `json:"fatherKnown"`
	Offender    bool   `json:"offender"`

	// Impersonates is the human whose identity this one also holds: a KindImpostor document copying that
	// human's data, on a person of its own, carrying this human's face and fingerprints. This human may
	// have no other record.
	Impersonates string `json:"impersonates,omitempty"`

	// Face and Variant bind the human to the face pool (AssignFaces): Face is
	// the base face used for enrollment photos, Variant the edited image used
	// for camera captures. Empty when the human has none.
	Face    string `json:"face,omitempty"`
	Variant string `json:"variant,omitempty"`
}

type File struct {
	FileType string `json:"fileType"` // "photo" or "nist"
	Sequence int16  `json:"sequence"`
}

// Enrollment is one identity.Ingest call's worth of data plus its ground truth.
type Enrollment struct {
	HumanID string `json:"humanId"` // ground truth
	Kind    string `json:"kind"`    // ground truth

	PersonID       string `json:"personId"`
	DocumentType   string `json:"documentType"`
	DocumentNumber string `json:"documentNumber"`
	FiscalNumber   string `json:"fiscalNumber,omitempty"`
	RegisterNumber string `json:"registerNumber"`
	Name           string `json:"name"`
	Parent1Name    string `json:"parent1Name"`
	Parent1Gender  string `json:"parent1Gender"`
	Parent2Name    string `json:"parent2Name"`
	Parent2Gender  string `json:"parent2Gender"`
	BirthDate      string `json:"birthDate"`
	Files          []File `json:"files"`
}

type Trace struct {
	Sequence  int16  `json:"sequence"`
	HumanID   string `json:"humanId"`   // ground truth
	Bystander bool   `json:"bystander"` // ground truth: not an offender in this event
}

type Evidence struct {
	Sequence    int16   `json:"sequence"`
	Description string  `json:"description"`
	Traces      []Trace `json:"traces"`
	// Capture is set on facial evidence only: the frame's quality level and
	// degradation parameters (media renders it).
	Capture *Capture `json:"capture,omitempty"`
}

type Case struct {
	CaseID       string     `json:"caseId"`
	CaseType     string     `json:"caseType"` // CRIMINAL (an offending occurrence) or CIVIL (an unknown person)
	Modality     string     `json:"modality"` // "FACIAL" | "FINGERPRINT"
	Description  string     `json:"description"`
	Year         int        `json:"year"`         // the occurrence's
	OccurrenceID string     `json:"occurrenceId"` // World.Occurrences key; loaded as the case's occurrence
	Evidences    []Evidence `json:"evidences"`
}

type World struct {
	Config      Config       `json:"config"`
	FacePool    *FacePoolRef `json:"facePool,omitempty"` // set by AssignFaces
	Humans      []Human      `json:"humans"`
	Enrollments []Enrollment `json:"enrollments"`
	Occurrences []Occurrence `json:"occurrences"`
	Cases       []Case       `json:"cases"`
}

// OccurrenceOf is the occurrence a case investigates.
func (w *World) OccurrenceOf(c Case) (Occurrence, bool) {
	for _, o := range w.Occurrences {
		if o.ID == c.OccurrenceID {
			return o, true
		}
	}
	return Occurrence{}, false
}

func (w *World) Save(path string) error {
	data, err := json.MarshalIndent(w, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func Load(path string) (*World, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var w World
	if err := json.Unmarshal(data, &w); err != nil {
		return nil, fmt.Errorf("world: parse %s: %w", path, err)
	}
	return &w, nil
}

// Summary is a one-glance description of a generated world.
type Summary struct {
	Humans, Offenders, Enrollments, Duplicates, Renewals int
	DoubleIdentities                                     int // humans also holding a record in another human's identity
	Cases, FacialCases, FingerprintCases, Traces         int
	BystanderTraces, UnenrolledOffenders                 int
	Occurrences, CivilOccurrences, CivilCases            int

	// SharedNameHumans is how many humans carry the same full (true, uncorrupted)
	// name as at least one other human, across SharedNames distinct names -- the
	// homonyms name-based linkage cannot tell apart.
	SharedNameHumans, SharedNames int
}

func (w *World) Summary() Summary {
	var s Summary
	s.Humans = len(w.Humans)
	enrolled := map[string]bool{}
	for _, e := range w.Enrollments {
		s.Enrollments++
		enrolled[e.HumanID] = true
		switch e.Kind {
		case KindDuplicate:
			s.Duplicates++
		case KindRenewal:
			s.Renewals++
		case KindImpostor:
			s.DoubleIdentities++
		}
	}
	byName := map[string]int{}
	for _, h := range w.Humans {
		byName[h.Name]++
	}
	for _, n := range byName {
		if n > 1 {
			s.SharedNames++
			s.SharedNameHumans += n
		}
	}
	for _, h := range w.Humans {
		if h.Offender {
			s.Offenders++
			if !enrolled[h.ID] {
				s.UnenrolledOffenders++
			}
		}
	}
	for _, o := range w.Occurrences {
		s.Occurrences++
		if o.CaseType == "CIVIL" {
			s.CivilOccurrences++
		}
	}
	for _, c := range w.Cases {
		s.Cases++
		if c.CaseType == "CIVIL" {
			s.CivilCases++
		}
		if c.Modality == "FACIAL" {
			s.FacialCases++
		} else {
			s.FingerprintCases++
		}
		for _, ev := range c.Evidences {
			for _, t := range ev.Traces {
				s.Traces++
				if t.Bystander {
					s.BystanderTraces++
				}
			}
		}
	}
	return s
}
