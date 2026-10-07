package service

// ArchiveSelection identifies the explicitly requested top-level objects of an
// archive operation. Runs are never selected directly: they join an archive as
// a referential closure of a formula or vessel.
type ArchiveSelection struct {
	FormulaIDs []string
	VesselIDs  []string
}

// RestoreSelection identifies objects to copy back from the historical
// repository. A group id restores an entire archived bundle; individual runs
// may be restored alone and pull their referents back with them.
type RestoreSelection struct {
	GroupID    string
	FormulaIDs []string
	VesselIDs  []string
	RunIDs     []string
}

// PlanBlocker explains why a plan cannot be executed. Codes reuse the stable
// domain error vocabulary.
type PlanBlocker struct {
	Code   string `json:"code"`
	Kind   string `json:"kind"`
	ID     string `json:"id"`
	Detail string `json:"detail"`
}

// ReferenceView describes one association that travels with an item or one
// index entry that an archive would remove from the live workspace.
type ReferenceView struct {
	Kind     string `json:"kind"`
	ID       string `json:"id"`
	Code     string `json:"code,omitempty"`
	Name     string `json:"name,omitempty"`
	Role     string `json:"role"`
	Location string `json:"location,omitempty"`
}

// PlanItemView is one versioned object shown in a preview. Location reports
// where the object currently lives: "live", "archive", or "live_conflict"
// during a restore when a different object occupies the same identity.
type PlanItemView struct {
	Kind             string          `json:"kind"`
	ID               string          `json:"id"`
	Code             string          `json:"code,omitempty"`
	Name             string          `json:"name,omitempty"`
	State            string          `json:"state"`
	Version          int64           `json:"version"`
	Location         string          `json:"location"`
	ObservationCount int             `json:"observation_count,omitempty"`
	RemoveIndexes    []string        `json:"remove_indexes,omitempty"`
	References       []ReferenceView `json:"references,omitempty"`
}

// Plan is the executable preview shared by archive and restore operations.
type Plan struct {
	Mode            string         `json:"mode"`
	Executable      bool           `json:"executable"`
	Outcome         string         `json:"outcome"`
	Token           string         `json:"token"`
	LiveRevision    int64          `json:"live_revision"`
	ArchiveRevision int64          `json:"archive_revision"`
	GroupID         string         `json:"group_id,omitempty"`
	Items           []PlanItemView `json:"items"`
	Blockers        []PlanBlocker  `json:"blockers,omitempty"`
}

const (
	modeArchive = "archive"
	modeRestore = "restore"

	outcomeReady           = "ready"
	outcomeBlocked         = "blocked"
	outcomeAlreadyArchived = "already_archived"
	outcomeAlreadyRestored = "already_restored"

	locationLive         = "live"
	locationArchive      = "archive"
	locationLiveConflict = "live_conflict"

	roleFormula          = "formula"
	roleVessel           = "vessel"
	roleRun              = "run"
	roleFormulaNameIndex = "formula_name_index"
	roleVesselCodeIndex  = "vessel_code_index"
	roleRunCodeIndex     = "run_code_index"
	roleActiveRun        = "active_run_id"
)
