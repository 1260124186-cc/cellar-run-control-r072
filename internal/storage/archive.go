package storage

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

const archiveFileName = "archive.json"

// ArchiveStore is the independent history repository. It keeps archived
// object bundles in a dedicated file with the same durable replace semantics
// as the working snapshot, guarded by its own lock.
type ArchiveStore struct {
	mu      sync.RWMutex
	path    string
	archive domain.Archive
	now     func() time.Time
}

func OpenArchive(dataDir string) (*ArchiveStore, error) {
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		return nil, fmt.Errorf("create data directory: %w", err)
	}
	store := &ArchiveStore{
		path:    filepath.Join(dataDir, archiveFileName),
		archive: domain.EmptyArchive(),
		now:     func() time.Time { return time.Now().UTC() },
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *ArchiveStore) load() error {
	raw, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read archive: %w", err)
	}
	var loaded domain.Archive
	if err := json.Unmarshal(raw, &loaded); err != nil {
		return fmt.Errorf("decode archive: %w", err)
	}
	normalized, err := normalizeArchive(loaded)
	if err != nil {
		return err
	}
	s.archive = normalized
	return nil
}

func normalizeArchive(value domain.Archive) (domain.Archive, error) {
	if value.Revision < 0 {
		return domain.Archive{}, fmt.Errorf("archive revision cannot be negative")
	}
	if value.Entries == nil {
		value.Entries = map[string]domain.ArchiveEntry{}
	}
	owners := make(map[string]string)
	for key, entry := range value.Entries {
		if entry.ID == "" || key != entry.ID {
			return domain.Archive{}, fmt.Errorf("archive entry key does not match its identifier")
		}
		if entry.Formulas == nil {
			entry.Formulas = map[string]domain.Formula{}
		}
		if entry.Vessels == nil {
			entry.Vessels = map[string]domain.Vessel{}
		}
		if entry.Runs == nil {
			entry.Runs = map[string]domain.FermentationRun{}
		}
		value.Entries[key] = entry
		for _, objectID := range entry.ObjectIDs() {
			if owner, found := owners[objectID]; found && owner != entry.ID {
				return domain.Archive{}, fmt.Errorf(
					"archive object %s appears in more than one entry", objectID)
			}
			owners[objectID] = entry.ID
		}
	}
	return value, nil
}

func (s *ArchiveStore) Revision() int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.archive.Revision
}

func (s *ArchiveStore) View(visitor func(domain.Archive) error) error {
	s.mu.RLock()
	copied := s.archive.Clone()
	s.mu.RUnlock()
	return visitor(copied)
}

// Put adds entries to the history area. Adding an entry that already exists
// with identical content is a no-op, so a retried archive confirm after a
// partial failure converges on the same stored record.
func (s *ArchiveStore) Put(entries []domain.ArchiveEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.archive.Clone()
	for _, entry := range entries {
		if existing, found := candidate.Entries[entry.ID]; found {
			if !existing.ContentEquals(entry) {
				return domain.NewError(domain.CodeArchiveConflict,
					"an archive entry with the same identifier holds different content")
			}
			continue
		}
		candidate.Entries[entry.ID] = entry.Clone()
	}
	candidate.Revision = s.archive.Revision + 1
	candidate.UpdatedAt = s.now()
	if err := writeSnapshot(s.path, candidate); err != nil {
		return domain.NewError(domain.CodeStorage, err.Error())
	}
	s.archive = candidate
	return nil
}

// Remove deletes entries from the history area. Missing entries are ignored
// so a retried restore confirm after a partial failure stays deterministic.
func (s *ArchiveStore) Remove(entryIDs []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	candidate := s.archive.Clone()
	for _, id := range entryIDs {
		delete(candidate.Entries, id)
	}
	candidate.Revision = s.archive.Revision + 1
	candidate.UpdatedAt = s.now()
	if err := writeSnapshot(s.path, candidate); err != nil {
		return domain.NewError(domain.CodeStorage, err.Error())
	}
	s.archive = candidate
	return nil
}
