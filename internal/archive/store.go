package archive

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// Store is the independent historical repository. It serializes access with
// its own in-process mutex and never touches the live state file.
type Store struct {
	mu       sync.RWMutex
	path     string
	snapshot Snapshot
	now      func() time.Time
}

func Open(archiveDir string) (*Store, error) {
	if err := os.MkdirAll(archiveDir, 0o755); err != nil {
		return nil, fmt.Errorf("create archive directory: %w", err)
	}
	store := &Store{
		path:     filepath.Join(archiveDir, archiveFileName),
		snapshot: EmptySnapshot(),
		now:      func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	var loaded Snapshot
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return fmt.Errorf("decode archive: %w", err)
	}
	normalized, err := normalizeSnapshot(loaded)
	if err != nil {
		return err
	}
	s.snapshot = normalized
	return nil
}

func normalizeSnapshot(value Snapshot) (Snapshot, error) {
	if value.SchemaVersion < 0 || value.SchemaVersion > schemaVersion {
		return Snapshot{}, fmt.Errorf("archive schema version is unsupported")
	}
	if value.Revision < 0 {
		return Snapshot{}, fmt.Errorf("archive revision cannot be negative")
	}
	if value.Entries == nil {
		value.Entries = map[string]Entry{}
	}
	if value.Groups == nil {
		value.Groups = []Group{}
	}
	if value.Events == nil {
		value.Events = []Event{}
	}
	for key, entry := range value.Entries {
		if EntryKey(entry.Kind, entry.ID) != key {
			return Snapshot{}, fmt.Errorf("archive ledger key does not match its entry")
		}
		if err := validateEntry(entry); err != nil {
			return Snapshot{}, err
		}
	}
	for _, group := range value.Groups {
		for _, key := range group.EntryKeys {
			if _, ok := value.Entries[key]; !ok {
				return Snapshot{}, fmt.Errorf("archive group %s references a missing entry", group.ID)
			}
		}
	}
	return value, nil
}

func validateEntry(entry Entry) error {
	switch entry.Kind {
	case KindFormula:
		if entry.Formula == nil || entry.Vessel != nil || entry.Run != nil {
			return fmt.Errorf("archive entry %s has a mismatched formula payload", entry.ID)
		}
	case KindVessel:
		if entry.Vessel == nil || entry.Formula != nil || entry.Run != nil {
			return fmt.Errorf("archive entry %s has a mismatched vessel payload", entry.ID)
		}
	case KindRun:
		if entry.Run == nil || entry.Formula != nil || entry.Vessel != nil {
			return fmt.Errorf("archive entry %s has a mismatched run payload", entry.ID)
		}
	default:
		return fmt.Errorf("archive entry %s has an unknown kind", entry.ID)
	}
	return nil
}

func (s *Store) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.snapshot.Revision
}

// View gives the visitor a detached copy of the historical snapshot.
func (s *Store) View(visitor func(Snapshot) error) error {
	s.mu.RLock()
	copied := s.snapshot.Clone()
	s.mu.RUnlock()
	return visitor(copied)
}

// CommitEntry contains one new immutable entry and whether it must replace an
// identical ledger key. Entries are immutable; the mutator rejects a key that
// already holds a different payload.
type CommitEntry struct {
	Entry Entry
}

// CommitResult reports what a durable archive commit accomplished.
type CommitResult struct {
	EventID      string
	GroupID      string
	AddedEntries int
	Revision     int64
}

// CommitArchive durably appends any missing entries and always records an
// event endorsing the listed entry keys. A group is created only when the
// commit introduced at least one new entry. The in-memory snapshot changes
// only after the durable write succeeds.
func (s *Store) CommitArchive(entries []CommitEntry) (CommitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.snapshot.Clone()
	committedKeys := make([]string, 0, len(entries))
	addedKeys := make([]string, 0, len(entries))
	for _, addition := range entries {
		if err := validateEntry(addition.Entry); err != nil {
			return CommitResult{}, domain.NewError(domain.CodeStorage, err.Error())
		}
		key := EntryKey(addition.Entry.Kind, addition.Entry.ID)
		if existing, ok := candidate.Entries[key]; ok {
			if !entriesEqual(existing, addition.Entry) {
				return CommitResult{}, domain.NewError(domain.CodeRestoreConflict,
					"archive already stores a different version of "+key)
			}
		} else {
			candidate.Entries[key] = cloneEntry(addition.Entry)
			addedKeys = append(addedKeys, key)
		}
		committedKeys = append(committedKeys, key)
	}

	stamp := s.now()
	result := CommitResult{AddedEntries: len(addedKeys), Revision: candidate.Revision + 1}
	if len(addedKeys) > 0 {
		groupID, err := newGroupID()
		if err != nil {
			return CommitResult{}, domain.NewError(domain.CodeStorage, err.Error())
		}
		result.GroupID = groupID
		candidate.Groups = append(candidate.Groups, Group{
			ID:         groupID,
			EntryKeys:  append([]string(nil), addedKeys...),
			Revision:   result.Revision,
			ArchivedAt: stamp,
		})
	}
	eventID, err := newEventID()
	if err != nil {
		return CommitResult{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	result.EventID = eventID
	candidate.Events = append(candidate.Events, Event{
		ID:         eventID,
		EntryKeys:  committedKeys,
		GroupID:    result.GroupID,
		Revision:   result.Revision,
		RecordedAt: stamp,
	})
	candidate.Revision = result.Revision
	candidate.UpdatedAt = stamp
	if err := writeArchive(s.path, candidate); err != nil {
		return CommitResult{}, domain.NewError(domain.CodeStorage, err.Error())
	}
	s.snapshot = candidate
	return result, nil
}

func entriesEqual(existing, candidate Entry) bool {
	switch candidate.Kind {
	case KindFormula:
		return existing.Formula != nil && reflect.DeepEqual(existing.Formula, candidate.Formula)
	case KindVessel:
		return vesselsEqual(existing.Vessel, candidate.Vessel)
	case KindRun:
		return existing.Run != nil && reflect.DeepEqual(existing.Run, candidate.Run)
	default:
		return false
	}
}

func vesselsEqual(existing, candidate *domain.Vessel) bool {
	if existing == nil || candidate == nil {
		return existing == candidate
	}
	runMatches := (existing.ActiveRunID == nil && candidate.ActiveRunID == nil) ||
		(existing.ActiveRunID != nil && candidate.ActiveRunID != nil &&
			*existing.ActiveRunID == *candidate.ActiveRunID)
	return runMatches &&
		existing.ID == candidate.ID && existing.Code == candidate.Code &&
		existing.CapacityL == candidate.CapacityL && existing.State == candidate.State &&
		existing.Version == candidate.Version &&
		existing.CreatedAt.Equal(candidate.CreatedAt) &&
		existing.UpdatedAt.Equal(candidate.UpdatedAt)
}
