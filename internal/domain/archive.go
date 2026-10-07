package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

// ObjectKind identifies the type of an archivable object.
type ObjectKind string

const (
	ObjectFormula ObjectKind = "formula"
	ObjectVessel  ObjectKind = "vessel"
	ObjectRun     ObjectKind = "run"
)

// ArchiveEntry is a self-contained bundle of objects moved out of the daily
// workspace. The entry identifier is derived from the archived content, so
// archiving the same unchanged objects always produces the same entry.
type ArchiveEntry struct {
	ID        string                     `json:"id"`
	CreatedAt time.Time                  `json:"created_at"`
	Formulas  map[string]Formula         `json:"formulas"`
	Vessels   map[string]Vessel          `json:"vessels"`
	Runs      map[string]FermentationRun `json:"runs"`
}

func NewArchiveEntry(id string, now time.Time) ArchiveEntry {
	return ArchiveEntry{
		ID:        id,
		CreatedAt: now.UTC(),
		Formulas:  map[string]Formula{},
		Vessels:   map[string]Vessel{},
		Runs:      map[string]FermentationRun{},
	}
}

func (e ArchiveEntry) Clone() ArchiveEntry {
	cloned := ArchiveEntry{
		ID:        e.ID,
		CreatedAt: e.CreatedAt,
		Formulas:  make(map[string]Formula, len(e.Formulas)),
		Vessels:   make(map[string]Vessel, len(e.Vessels)),
		Runs:      make(map[string]FermentationRun, len(e.Runs)),
	}
	for id, formula := range e.Formulas {
		cloned.Formulas[id] = formula.Clone()
	}
	for id, vessel := range e.Vessels {
		cloned.Vessels[id] = vessel.Clone()
	}
	for id, run := range e.Runs {
		cloned.Runs[id] = run.Clone()
	}
	return cloned
}

// ContentEquals reports whether two entries hold the same objects. The
// identifier and creation time are metadata and are not compared.
func (e ArchiveEntry) ContentEquals(other ArchiveEntry) bool {
	if len(e.Formulas) != len(other.Formulas) ||
		len(e.Vessels) != len(other.Vessels) ||
		len(e.Runs) != len(other.Runs) {
		return false
	}
	for id, formula := range e.Formulas {
		candidate, ok := other.Formulas[id]
		if !ok || !FormulasEqual(formula, candidate) {
			return false
		}
	}
	for id, vessel := range e.Vessels {
		candidate, ok := other.Vessels[id]
		if !ok || !VesselsEqual(vessel, candidate) {
			return false
		}
	}
	for id, run := range e.Runs {
		candidate, ok := other.Runs[id]
		if !ok || !RunsEqual(run, candidate) {
			return false
		}
	}
	return true
}

// ObjectIDs returns every archived object identifier in a stable order.
func (e ArchiveEntry) ObjectIDs() []string {
	ids := make([]string, 0, len(e.Formulas)+len(e.Vessels)+len(e.Runs))
	for id := range e.Formulas {
		ids = append(ids, id)
	}
	for id := range e.Vessels {
		ids = append(ids, id)
	}
	for id := range e.Runs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ArchiveEntryID derives a deterministic identifier from the archived
// objects. Eligible objects are immutable, so the same set of objects always
// yields the same entry identifier across retries and archive/restore cycles.
func ArchiveEntryID(formulas []Formula, vessels []Vessel, runs []FermentationRun) string {
	descriptors := make([]string, 0, len(formulas)+len(vessels)+len(runs))
	for _, formula := range formulas {
		descriptors = append(descriptors,
			objectDescriptor(ObjectFormula, formula.ID, formula.Version))
	}
	for _, vessel := range vessels {
		descriptors = append(descriptors,
			objectDescriptor(ObjectVessel, vessel.ID, vessel.Version))
	}
	for _, run := range runs {
		descriptors = append(descriptors,
			objectDescriptor(ObjectRun, run.ID, run.Version))
	}
	sort.Strings(descriptors)
	digest := sha256.Sum256([]byte("cellar-archive-v1\n" + strings.Join(descriptors, "\n")))
	return "arch_" + hex.EncodeToString(digest[:16])
}

func objectDescriptor(kind ObjectKind, id string, version int64) string {
	return string(kind) + ":" + id + ":" + strconv.FormatInt(version, 10)
}

// Archive is the durable history area. It is stored separately from the
// working snapshot so daily operations never read or write archived objects.
type Archive struct {
	Revision  int64                   `json:"revision"`
	UpdatedAt time.Time               `json:"updated_at"`
	Entries   map[string]ArchiveEntry `json:"entries"`
}

func EmptyArchive() Archive {
	return Archive{
		Revision: 0,
		Entries:  map[string]ArchiveEntry{},
	}
}

func (a Archive) Clone() Archive {
	cloned := EmptyArchive()
	cloned.Revision = a.Revision
	cloned.UpdatedAt = a.UpdatedAt
	for id, entry := range a.Entries {
		cloned.Entries[id] = entry.Clone()
	}
	return cloned
}

// FindObject locates the entry that holds the given object identifier.
func (a Archive) FindObject(objectID string) (string, ObjectKind, bool) {
	for _, entryID := range a.sortedEntryIDs() {
		entry := a.Entries[entryID]
		if _, ok := entry.Formulas[objectID]; ok {
			return entryID, ObjectFormula, true
		}
		if _, ok := entry.Vessels[objectID]; ok {
			return entryID, ObjectVessel, true
		}
		if _, ok := entry.Runs[objectID]; ok {
			return entryID, ObjectRun, true
		}
	}
	return "", "", false
}

func (a Archive) sortedEntryIDs() []string {
	ids := make([]string, 0, len(a.Entries))
	for id := range a.Entries {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func FormulasEqual(a, b Formula) bool {
	return a.ID == b.ID &&
		a.Name == b.Name &&
		a.Style == b.Style &&
		a.TargetOriginalGravity == b.TargetOriginalGravity &&
		a.TargetFinalGravity == b.TargetFinalGravity &&
		a.MaxFermentationTempC == b.MaxFermentationTempC &&
		a.MinimumDays == b.MinimumDays &&
		a.MaximumDays == b.MaximumDays &&
		a.State == b.State &&
		a.Version == b.Version &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt) &&
		timePointersEqual(a.ApprovedAt, b.ApprovedAt) &&
		timePointersEqual(a.RetiredAt, b.RetiredAt) &&
		slices.Equal(a.Steps, b.Steps)
}

func VesselsEqual(a, b Vessel) bool {
	return a.ID == b.ID &&
		a.Code == b.Code &&
		a.CapacityL == b.CapacityL &&
		a.State == b.State &&
		stringPointersEqual(a.ActiveRunID, b.ActiveRunID) &&
		a.Version == b.Version &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

func RunsEqual(a, b FermentationRun) bool {
	return a.ID == b.ID &&
		a.Code == b.Code &&
		a.FormulaID == b.FormulaID &&
		stringPointersEqual(a.VesselID, b.VesselID) &&
		a.PlannedVolumeL == b.PlannedVolumeL &&
		a.Notes == b.Notes &&
		a.State == b.State &&
		timePointersEqual(a.StartedAt, b.StartedAt) &&
		timePointersEqual(a.EndedAt, b.EndedAt) &&
		slices.EqualFunc(a.Observations, b.Observations, observationsEqual) &&
		metricsPointersEqual(a.Metrics, b.Metrics) &&
		a.Version == b.Version &&
		a.CreatedAt.Equal(b.CreatedAt) &&
		a.UpdatedAt.Equal(b.UpdatedAt)
}

func observationsEqual(a, b GravityObservation) bool {
	return a.RunID == b.RunID &&
		a.Sequence == b.Sequence &&
		a.Gravity == b.Gravity &&
		a.TemperatureC == b.TemperatureC &&
		a.ObservedAt.Equal(b.ObservedAt) &&
		a.Note == b.Note &&
		a.ReceivedAt.Equal(b.ReceivedAt)
}

func timePointersEqual(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Equal(*b)
}

func stringPointersEqual(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func metricsPointersEqual(a, b *RunMetrics) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
