package service

import (
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/archive"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// PreviewArchive computes the deterministic set of versions, observations, and
// associated references that an archive would take, without changing either
// repository.
func (s *Service) PreviewArchive(selection ArchiveSelection) (Plan, error) {
	if s.archives == nil {
		return Plan{}, domain.NewError(domain.CodeArchiveNotConfigured,
			"the historical archive repository is not configured")
	}
	plan, _, _, err := s.buildArchivePlan(selection)
	if err != nil {
		return Plan{}, err
	}
	return plan, nil
}

// ConfirmArchive first copies every selected version into the historical
// repository durably, and only then removes the live objects. A failure while
// writing history never deletes an online object.
func (s *Service) ConfirmArchive(selection ArchiveSelection, token string) (Plan, error) {
	if s.archives == nil {
		return Plan{}, domain.NewError(domain.CodeArchiveNotConfigured,
			"the historical archive repository is not configured")
	}
	plan, liveSnapshot, _, err := s.buildArchivePlan(selection)
	if err != nil {
		return Plan{}, err
	}
	if token == "" || token != plan.Token {
		return Plan{}, domain.NewError(domain.CodePlanTokenInvalid,
			"the confirmation token does not match the current archive preview")
	}
	if !plan.Executable {
		return plan, domain.NewError(domain.CodeArchiveBlocked,
			"the archive preview lists blockers that must be resolved first")
	}
	if plan.Outcome == outcomeAlreadyArchived {
		return plan, nil
	}

	additions, err := s.archiveAdditions(plan, liveSnapshot)
	if err != nil {
		return Plan{}, err
	}
	if len(additions) == 0 {
		// Nothing is about to leave the workspace.
		return plan, nil
	}
	// History is committed before anything leaves the live workspace. If this
	// write fails, every object remains online unchanged.
	result, err := s.archives.CommitArchive(additions)
	if err != nil {
		return Plan{}, err
	}

	err = s.store.Update(nil, func(snapshot *domain.Snapshot) error {
		for _, item := range plan.Items {
			switch item.Kind {
			case archive.KindFormula:
				formula, ok := snapshot.Formulas[item.ID]
				if !ok {
					continue // already absent: deterministic repeat
				}
				if formula.Version != item.Version {
					return domain.NewError(domain.CodeRevisionConflict,
						"the live formula changed after the archive preview")
				}
				if formula.State != domain.FormulaRetired {
					return domain.NewError(domain.CodeArchiveNotRetired,
						"only a retired formula can leave the workspace")
				}
				delete(snapshot.Formulas, item.ID)
				delete(snapshot.NameIndex, domain.NameKey(formula.Name))
			case archive.KindVessel:
				vessel, ok := snapshot.Vessels[item.ID]
				if !ok {
					continue
				}
				if vessel.Version != item.Version {
					return domain.NewError(domain.CodeRevisionConflict,
						"the live vessel changed after the archive preview")
				}
				if vessel.State != domain.VesselRetired {
					return domain.NewError(domain.CodeArchiveNotRetired,
						"only a retired vessel can leave the workspace")
				}
				delete(snapshot.Vessels, item.ID)
				delete(snapshot.VesselIndex, vessel.Code)
			case archive.KindRun:
				run, ok := snapshot.Runs[item.ID]
				if !ok {
					continue
				}
				if run.Version != item.Version {
					return domain.NewError(domain.CodeRevisionConflict,
						"the live run changed after the archive preview")
				}
				if run.State != domain.RunCompleted && run.State != domain.RunAborted {
					return domain.NewError(domain.CodeArchiveBlocked,
						"only a completed or aborted run can leave the workspace")
				}
				delete(snapshot.Runs, item.ID)
				delete(snapshot.RunIndex, run.Code)
			}
		}
		return nil
	})
	if err != nil {
		// History already holds the copies, but every live object is intact.
		// A repeat confirmation removes them deterministically.
		return Plan{}, err
	}

	refreshed, _, _, err := s.buildArchivePlan(selection)
	if err != nil {
		return Plan{}, err
	}
	if result.GroupID != "" {
		refreshed.GroupID = result.GroupID
	} else if plan.GroupID != "" {
		refreshed.GroupID = plan.GroupID
	}
	return refreshed, nil
}

func (s *Service) archiveAdditions(plan Plan, live domain.Snapshot) ([]archive.CommitEntry, error) {
	stamp := s.clock.Now()
	additions := make([]archive.CommitEntry, 0, len(plan.Items))
	for _, item := range plan.Items {
		entry := archive.Entry{Kind: item.Kind, ID: item.ID, ArchivedAt: stamp}
		switch item.Kind {
		case archive.KindFormula:
			formula, exists := live.Formulas[item.ID]
			if !exists {
				continue // identical history copy already endorses its absence
			}
			copy := formula.Clone()
			entry.Version, entry.Formula = copy.Version, &copy
		case archive.KindVessel:
			vessel, exists := live.Vessels[item.ID]
			if !exists {
				continue
			}
			if vessel.ActiveRunID != nil {
				runID := *vessel.ActiveRunID
				vessel.ActiveRunID = &runID
			}
			entry.Version, entry.Vessel = vessel.Version, &vessel
		case archive.KindRun:
			run, exists := live.Runs[item.ID]
			if !exists {
				continue
			}
			copy := run.Clone()
			entry.Version, entry.Run = copy.Version, &copy
		}
		additions = append(additions, archive.CommitEntry{Entry: entry})
	}
	return additions, nil
}
