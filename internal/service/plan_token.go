package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
)

// planToken binds a confirmed operation to the exact previewed content and the
// revisions of both repositories at preview time. Any drift before confirmation
// changes the derived token and rejects the commit.
func planToken(mode string, liveRevision, archiveRevision int64, selection any, items []PlanItemView) string {
	payload := struct {
		Mode            string         `json:"mode"`
		LiveRevision    int64          `json:"live_revision"`
		ArchiveRevision int64          `json:"archive_revision"`
		Selection       any            `json:"selection"`
		Items           []PlanItemView `json:"items"`
	}{
		Mode:            mode,
		LiveRevision:    liveRevision,
		ArchiveRevision: archiveRevision,
		Selection:       selection,
		Items:           sortedItems(items),
	}
	encoded, _ := json.Marshal(payload)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:16])
}

func sortedItems(items []PlanItemView) []PlanItemView {
	copied := append([]PlanItemView(nil), items...)
	sort.SliceStable(copied, func(i, j int) bool {
		if copied[i].Kind != copied[j].Kind {
			return copied[i].Kind < copied[j].Kind
		}
		return copied[i].ID < copied[j].ID
	})
	return copied
}
