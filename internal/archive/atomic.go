package archive

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// writeArchive commits the snapshot durably with a same-directory temp file,
// fsync, close, and atomic rename. A failed call never removes an existing
// archive file.
func writeArchive(path string, snapshot any) error {
	directory := filepath.Dir(path)
	handle, err := os.CreateTemp(directory, ".archive-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary archive: %w", err)
	}
	tempName := handle.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tempName)
		}
	}()

	encoder := json.NewEncoder(handle)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(snapshot); err != nil {
		_ = handle.Close()
		return fmt.Errorf("encode archive: %w", err)
	}
	if err := handle.Sync(); err != nil {
		_ = handle.Close()
		return fmt.Errorf("commit archive bytes: %w", err)
	}
	if err := handle.Close(); err != nil {
		return fmt.Errorf("close temporary archive: %w", err)
	}
	if err := os.Rename(tempName, path); err != nil {
		return fmt.Errorf("replace archive: %w", err)
	}
	removeTemp = false
	return nil
}
