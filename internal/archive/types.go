// Package archive implements the independent historical repository. It owns
// its own snapshot file, revision counter, mutex, and durable atomic write, so
// it never shares a file with the live state repository.
package archive

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const (
	archiveFileName = "archive.json"
	schemaVersion   = 1

	KindFormula = "formula"
	KindVessel  = "vessel"
	KindRun     = "run"
)

// Entry is one immutable historical object. The typed payload is the exact
// domain value that was removed from the live workspace; the kind and identity
// timestamps make the ledger self-describing for audits.
type Entry struct {
	Kind       string                  `json:"kind"`
	ID         string                  `json:"id"`
	Version    int64                   `json:"version"`
	Formula    *domain.Formula         `json:"formula,omitempty"`
	Vessel     *domain.Vessel          `json:"vessel,omitempty"`
	Run        *domain.FermentationRun `json:"run,omitempty"`
	ArchivedAt time.Time               `json:"archived_at"`
}

// Group records the deterministic bundle of entry keys that moved together in
// one confirmed archive operation. It allows an auditor to restore a whole
// group, not only a single object.
type Group struct {
	ID         string    `json:"id"`
	EntryKeys  []string  `json:"entry_keys"`
	Revision   int64     `json:"revision"`
	ArchivedAt time.Time `json:"archived_at"`
}

// Event durably records one confirmed archive attempt. Live objects are only
// removed after the event that endorses their departure is on disk, so a failed
// history write can never delete an online object.
type Event struct {
	ID         string    `json:"id"`
	EntryKeys  []string  `json:"entry_keys"`
	GroupID    string    `json:"group_id,omitempty"`
	Revision   int64     `json:"revision"`
	RecordedAt time.Time `json:"recorded_at"`
}

// Snapshot is the complete durable content of the historical repository.
type Snapshot struct {
	SchemaVersion int64            `json:"schema_version"`
	Revision      int64            `json:"revision"`
	UpdatedAt     time.Time        `json:"updated_at"`
	Entries       map[string]Entry `json:"entries"`
	Groups        []Group          `json:"groups"`
	Events        []Event          `json:"events"`
}

func EmptySnapshot() Snapshot {
	return Snapshot{
		SchemaVersion: schemaVersion,
		Entries:       map[string]Entry{},
		Groups:        []Group{},
		Events:        []Event{},
	}
}

func EntryKey(kind, id string) string {
	return kind + ":" + id
}

// SplitKey parses a ledger key returned in API views.
func SplitKey(key string) (kind, id string, err error) {
	for _, candidate := range []string{KindFormula, KindVessel, KindRun} {
		prefix := candidate + ":"
		if len(key) > len(prefix) && key[:len(prefix)] == prefix {
			return candidate, key[len(prefix):], nil
		}
	}
	return "", "", fmt.Errorf("invalid archive entry key: %s", key)
}

// Clone returns a detached copy; callers never mutate repository-owned values.
func (s Snapshot) Clone() Snapshot {
	cloned := EmptySnapshot()
	cloned.SchemaVersion = s.SchemaVersion
	cloned.Revision = s.Revision
	cloned.UpdatedAt = s.UpdatedAt
	for key, entry := range s.Entries {
		cloned.Entries[key] = cloneEntry(entry)
	}
	cloned.Groups = append([]Group(nil), s.Groups...)
	cloned.Events = append([]Event(nil), s.Events...)
	return cloned
}

func cloneEntry(entry Entry) Entry {
	copied := entry
	if entry.Formula != nil {
		value := entry.Formula.Clone()
		copied.Formula = &value
	}
	if entry.Vessel != nil {
		value := *entry.Vessel
		if entry.Vessel.ActiveRunID != nil {
			runID := *entry.Vessel.ActiveRunID
			value.ActiveRunID = &runID
		}
		copied.Vessel = &value
	}
	if entry.Run != nil {
		value := entry.Run.Clone()
		copied.Run = &value
	}
	return copied
}
