package service

import (
	"sort"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/archive"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// ConfirmRestore copies the previewed entries back in one live snapshot
// transaction. Conflict checks are re-run against the current snapshot inside
// that transaction, so a failure leaves the workspace exactly as it was: a
// restore can never return half of a group.
func (s *Service) ConfirmRestore(selection RestoreSelection, token string) (Plan, error) {
	if s.archives == nil {
		return Plan{}, domain.NewError(domain.CodeArchiveNotConfigured,
			"the historical archive repository is not configured")
	}
	plan, err := s.buildRestorePlan(selection)
	if err != nil {
		return Plan{}, err
	}
	if token == "" || token != plan.Token {
		return Plan{}, domain.NewError(domain.CodePlanTokenInvalid,
			"the confirmation token does not match the current restore preview")
	}
	if !plan.Executable {
		return plan, domain.NewError(domain.CodeRestoreBlocked,
			"the restore preview lists conflicts that must be resolved first")
	}
	if plan.Outcome == outcomeAlreadyRestored {
		return plan, nil
	}

	var history archive.Snapshot
	if err := s.archives.View(func(snapshot archive.Snapshot) error {
		history = snapshot
		return nil
	}); err != nil {
		return Plan{}, err
	}

	err = s.store.Update(nil, func(snapshot *domain.Snapshot) error {
		// References resolve in dependency order, regardless of preview order:
		// formulas, vessels, then runs.
		for _, kind := range []string{archive.KindFormula, archive.KindVessel, archive.KindRun} {
			for _, item := range plan.Items {
				if item.Kind != kind {
					continue
				}
				if err := restoreItem(snapshot, history, item); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return Plan{}, err
	}

	return s.buildRestorePlan(selection)
}

func restoreItem(snapshot *domain.Snapshot, history archive.Snapshot, item PlanItemView) error {
	if item.Location == locationLive {
		return nil // identical object already back
	}
	key := archive.EntryKey(item.Kind, item.ID)
	entry, ok := history.Entries[key]
	if !ok {
		return domain.NewError(domain.CodeArchiveEntryNotFound,
			"archived entry vanished before restore: "+key)
	}
	switch item.Kind {
	case archive.KindFormula:
		if current, exists := snapshot.Formulas[item.ID]; exists {
			if !formulaEqual(current, *entry.Formula) {
				return domain.NewError(domain.CodeRestoreConflict,
					"a different live formula now occupies this id")
			}
			return nil
		}
		nameKey := domain.NameKey(entry.Formula.Name)
		if occupant, exists := snapshot.NameIndex[nameKey]; exists && occupant != item.ID {
			return domain.NewError(domain.CodeFormulaNameConflict,
				"normalized name is now used by another live formula")
		}
		restored := entry.Formula.Clone()
		snapshot.Formulas[item.ID] = restored
		snapshot.NameIndex[nameKey] = item.ID
	case archive.KindVessel:
		if current, exists := snapshot.Vessels[item.ID]; exists {
			if !vesselEqual(&current, entry.Vessel) {
				return domain.NewError(domain.CodeRestoreConflict,
					"a different live vessel now occupies this id")
			}
			return nil
		}
		if occupant, exists := snapshot.VesselIndex[entry.Vessel.Code]; exists && occupant != item.ID {
			return domain.NewError(domain.CodeVesselConflict,
				"vessel code is now used by another live vessel")
		}
		vessel := *entry.Vessel
		if entry.Vessel.ActiveRunID != nil {
			runID := *entry.Vessel.ActiveRunID
			vessel.ActiveRunID = &runID
		}
		snapshot.Vessels[item.ID] = vessel
		snapshot.VesselIndex[vessel.Code] = item.ID
	case archive.KindRun:
		if current, exists := snapshot.Runs[item.ID]; exists {
			if !reflectDeepEqualRun(&current, entry.Run) {
				return domain.NewError(domain.CodeRestoreConflict,
					"a different live run now occupies this id")
			}
			return nil
		}
		if occupant, exists := snapshot.RunIndex[entry.Run.Code]; exists && occupant != item.ID {
			return domain.NewError(domain.CodeRunConflict,
				"run code is now used by another live run")
		}
		if _, formulaExists := snapshot.Formulas[entry.Run.FormulaID]; !formulaExists {
			return domain.NewError(domain.CodeRestoreBlocked,
				"referenced formula is not available in the workspace")
		}
		if entry.Run.VesselID != nil {
			if _, vesselExists := snapshot.Vessels[*entry.Run.VesselID]; !vesselExists {
				return domain.NewError(domain.CodeRestoreBlocked,
					"referenced vessel is not available in the workspace")
			}
		}
		snapshot.Runs[item.ID] = entry.Run.Clone()
		snapshot.RunIndex[entry.Run.Code] = item.ID
	}
	return nil
}

// ArchiveEntryView is one ledger row in an audit listing.
type ArchiveEntryView struct {
	Kind             string    `json:"kind"`
	ID               string    `json:"id"`
	Version          int64     `json:"version"`
	Name             string    `json:"name,omitempty"`
	Code             string    `json:"code,omitempty"`
	State            string    `json:"state,omitempty"`
	ObservationCount int       `json:"observation_count,omitempty"`
	ArchivedAt       time.Time `json:"archived_at"`
}

// ArchiveGroupView describes one archive bundle for audits.
type ArchiveGroupView struct {
	ID         string    `json:"id"`
	EntryKeys  []string  `json:"entry_keys"`
	Revision   int64     `json:"revision"`
	ArchivedAt time.Time `json:"archived_at"`
}

// ArchiveListing is the audit view of the historical repository.
type ArchiveListing struct {
	Revision   int64              `json:"revision"`
	Entries    []ArchiveEntryView `json:"entries"`
	Groups     []ArchiveGroupView `json:"groups"`
	EventCount int                `json:"event_count"`
}

// ListArchive reads the historical repository without touching live state.
func (s *Service) ListArchive() (ArchiveListing, error) {
	if s.archives == nil {
		return ArchiveListing{}, domain.NewError(domain.CodeArchiveNotConfigured,
			"the historical archive repository is not configured")
	}
	listing := ArchiveListing{Entries: []ArchiveEntryView{}, Groups: []ArchiveGroupView{}}
	err := s.archives.View(func(history archive.Snapshot) error {
		listing.Revision = history.Revision
		listing.EventCount = len(history.Events)
		keys := make([]string, 0, len(history.Entries))
		for key := range history.Entries {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			entry := history.Entries[key]
			view := ArchiveEntryView{
				Kind:       entry.Kind,
				ID:         entry.ID,
				Version:    entry.Version,
				ArchivedAt: entry.ArchivedAt,
			}
			switch entry.Kind {
			case archive.KindFormula:
				view.Name = entry.Formula.Name
				view.State = string(entry.Formula.State)
			case archive.KindVessel:
				view.Code = entry.Vessel.Code
				view.State = string(entry.Vessel.State)
			case archive.KindRun:
				view.Code = entry.Run.Code
				view.State = string(entry.Run.State)
				view.ObservationCount = len(entry.Run.Observations)
			}
			listing.Entries = append(listing.Entries, view)
		}
		for _, group := range history.Groups {
			listing.Groups = append(listing.Groups, ArchiveGroupView{
				ID:         group.ID,
				EntryKeys:  append([]string(nil), group.EntryKeys...),
				Revision:   group.Revision,
				ArchivedAt: group.ArchivedAt,
			})
		}
		return nil
	})
	if err != nil {
		return ArchiveListing{}, err
	}
	return listing, nil
}
