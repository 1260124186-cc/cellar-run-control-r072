package service

import (
	"reflect"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/archive"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// archiveResolution is the internal working set behind an archive preview.
type archiveResolution struct {
	formulaIDs map[string]struct{}
	vesselIDs  map[string]struct{}
	runIDs     map[string]struct{}
	requested  map[string]struct{}
	items      []PlanItemView
	blockers   []PlanBlocker
}

func newArchiveResolution(selection ArchiveSelection) *archiveResolution {
	resolution := &archiveResolution{
		formulaIDs: map[string]struct{}{},
		vesselIDs:  map[string]struct{}{},
		runIDs:     map[string]struct{}{},
		requested:  map[string]struct{}{},
	}
	for _, id := range selection.FormulaIDs {
		if id != "" {
			resolution.formulaIDs[id] = struct{}{}
			resolution.requested[archive.EntryKey(archive.KindFormula, id)] = struct{}{}
		}
	}
	for _, id := range selection.VesselIDs {
		if id != "" {
			resolution.vesselIDs[id] = struct{}{}
			resolution.requested[archive.EntryKey(archive.KindVessel, id)] = struct{}{}
		}
	}
	return resolution
}

func (r *archiveResolution) blocker(code, kind, id, detail string) {
	r.blockers = append(r.blockers, PlanBlocker{Code: code, Kind: kind, ID: id, Detail: detail})
}

func collectClosureRuns(runs map[string]domain.FermentationRun, resolution *archiveResolution) {
	for _, run := range runs {
		_, formulaSelected := resolution.formulaIDs[run.FormulaID]
		vesselSelected := false
		if run.VesselID != nil {
			_, vesselSelected = resolution.vesselIDs[*run.VesselID]
		}
		if formulaSelected || vesselSelected {
			resolution.runIDs[run.ID] = struct{}{}
		}
	}
}

func (s *Service) buildArchivePlan(selection ArchiveSelection) (Plan, domain.Snapshot, archive.Snapshot, error) {
	if len(selection.FormulaIDs) == 0 && len(selection.VesselIDs) == 0 {
		return Plan{}, domain.Snapshot{}, archive.Snapshot{},
			domain.FieldError(domain.CodeInvalidInput, "selection",
				"at least one retired formula or vessel must be selected")
	}

	var live domain.Snapshot
	if err := s.store.View(func(snapshot domain.Snapshot) error {
		live = snapshot
		return nil
	}); err != nil {
		return Plan{}, domain.Snapshot{}, archive.Snapshot{}, err
	}
	var history archive.Snapshot
	if err := s.archives.View(func(snapshot archive.Snapshot) error {
		history = snapshot
		return nil
	}); err != nil {
		return Plan{}, domain.Snapshot{}, archive.Snapshot{}, err
	}

	resolution := newArchiveResolution(selection)

	// Closure: every terminal run attached to a selected formula or reserved by
	// a selected vessel travels together, with all of its observations. A run
	// can currently live on either side of the boundary (for example after a
	// previous interrupted archive was completed), so both stores are scanned.
	collectClosureRuns(live.Runs, resolution)
	for _, entry := range history.Entries {
		if entry.Kind == archive.KindRun {
			collectClosureRuns(map[string]domain.FermentationRun{entry.ID: *entry.Run},
				resolution)
		}
	}

	s.resolveArchiveFormulas(live, history, resolution)
	s.resolveArchiveVessels(live, history, resolution)
	s.resolveArchiveRuns(live, history, resolution)

	plan := Plan{
		Mode:            modeArchive,
		Items:           sortedItems(resolution.items),
		Blockers:        resolution.blockers,
		LiveRevision:    live.Revision,
		ArchiveRevision: history.Revision,
	}
	plan.Executable = len(plan.Blockers) == 0
	switch {
	case !plan.Executable:
		plan.Outcome = outcomeBlocked
	case allItemsLocated(plan.Items, locationArchive):
		plan.Outcome = outcomeAlreadyArchived
		plan.GroupID = groupContaining(history, resolution.requested)
	default:
		plan.Outcome = outcomeReady
	}
	plan.Token = planToken(modeArchive, live.Revision, history.Revision, selection, plan.Items)
	return plan, live, history, nil
}

func (s *Service) resolveArchiveFormulas(
	live domain.Snapshot,
	history archive.Snapshot,
	resolution *archiveResolution,
) {
	for id := range resolution.formulaIDs {
		formula, liveOK := live.Formulas[id]
		entry, archivedOK := history.Entries[archive.EntryKey(archive.KindFormula, id)]
		if !liveOK {
			if !archivedOK {
				resolution.blocker(string(domain.CodeFormulaNotFound),
					archive.KindFormula, id, "formula exists neither in the workspace nor in the archive")
				continue
			}
			resolution.items = append(resolution.items, formulaItemFromArchive(entry.Formula, locationArchive))
			continue
		}
		item := formulaItemFromLive(&formula)
		if archivedOK && !formulaEqual(*entry.Formula, formula) {
			resolution.blocker(string(domain.CodeRestoreConflict), archive.KindFormula, id,
				"the archive already stores a different version of this formula")
			continue
		}
		if formula.State != domain.FormulaRetired {
			resolution.blocker(string(domain.CodeArchiveNotRetired), archive.KindFormula, id,
				"only a retired formula can be archived")
		}
		item.RemoveIndexes = append(item.RemoveIndexes, domain.NameKey(formula.Name))
		for _, run := range live.Runs {
			if run.FormulaID == id {
				item.References = append(item.References, ReferenceView{
					Kind: archive.KindRun, ID: run.ID, Code: run.Code, Role: roleRun})
			}
		}
		resolution.items = append(resolution.items, item)
	}
}

func (s *Service) resolveArchiveVessels(
	live domain.Snapshot,
	history archive.Snapshot,
	resolution *archiveResolution,
) {
	for id := range resolution.vesselIDs {
		vessel, liveOK := live.Vessels[id]
		entry, archivedOK := history.Entries[archive.EntryKey(archive.KindVessel, id)]
		if !liveOK {
			if !archivedOK {
				resolution.blocker(string(domain.CodeVesselNotFound),
					archive.KindVessel, id, "vessel exists neither in the workspace nor in the archive")
				continue
			}
			resolution.items = append(resolution.items, vesselItemFromArchive(entry.Vessel, locationArchive))
			continue
		}
		item := vesselItemFromLive(&vessel)
		if archivedOK && !vesselEqual(entry.Vessel, &vessel) {
			resolution.blocker(string(domain.CodeRestoreConflict), archive.KindVessel, id,
				"the archive already stores a different version of this vessel")
			continue
		}
		if vessel.State != domain.VesselRetired {
			resolution.blocker(string(domain.CodeArchiveNotRetired), archive.KindVessel, id,
				"only a retired vessel can be archived")
		}
		if vessel.ActiveRunID != nil {
			run, runOK := live.Runs[*vessel.ActiveRunID]
			detail := "retired vessel still names an active run; finish cleaning before archiving"
			if runOK {
				item.References = append(item.References, ReferenceView{
					Kind: archive.KindRun, ID: run.ID, Code: run.Code, Role: roleActiveRun})
				if run.State != domain.RunCompleted && run.State != domain.RunAborted {
					detail = "vessel is attached to a run that is still open"
				}
			}
			resolution.blocker(string(domain.CodeArchiveReference), archive.KindVessel, id, detail)
		}
		item.RemoveIndexes = append(item.RemoveIndexes, vessel.Code)
		for _, run := range live.Runs {
			if run.VesselID != nil && *run.VesselID == id {
				item.References = appendReference(item.References, ReferenceView{
					Kind: archive.KindRun, ID: run.ID, Code: run.Code, Role: roleRun})
			}
		}
		resolution.items = append(resolution.items, item)
	}
}

func (s *Service) resolveArchiveRuns(
	live domain.Snapshot,
	history archive.Snapshot,
	resolution *archiveResolution,
) {
	for id := range resolution.runIDs {
		run, liveOK := live.Runs[id]
		entry, archivedOK := history.Entries[archive.EntryKey(archive.KindRun, id)]
		if !liveOK {
			if !archivedOK {
				resolution.blocker(string(domain.CodeRunNotFound),
					archive.KindRun, id, "associated run exists neither in the workspace nor in the archive")
				continue
			}
			resolution.items = append(resolution.items, runItemFromArchive(entry.Run, locationArchive))
			continue
		}
		item := runItemFromLive(&run)
		if archivedOK && !reflect.DeepEqual(entry.Run, &run) {
			resolution.blocker(string(domain.CodeRestoreConflict), archive.KindRun, id,
				"the archive already stores a different version of this run")
			continue
		}
		if run.State != domain.RunCompleted && run.State != domain.RunAborted {
			resolution.blocker(string(domain.CodeArchiveBlocked), archive.KindRun, id,
				"only a completed or aborted run can be archived")
		}
		// Defensive referential integrity: a live run's formula is always
		// live; a vessel that has gone missing from the snapshot is corruption.
		if _, ok := live.Formulas[run.FormulaID]; !ok {
			resolution.blocker(string(domain.CodeArchiveReference), archive.KindRun, id,
				"run references a formula that is missing from the workspace")
		} else {
			formula := live.Formulas[run.FormulaID]
			item.References = append(item.References, ReferenceView{
				Kind: archive.KindFormula, ID: formula.ID, Name: formula.Name, Role: roleFormula})
		}
		if run.VesselID != nil {
			vessel, ok := live.Vessels[*run.VesselID]
			if !ok {
				resolution.blocker(string(domain.CodeArchiveReference), archive.KindRun, id,
					"run references a vessel that is missing from the workspace")
			} else {
				item.References = append(item.References, ReferenceView{
					Kind: archive.KindVessel, ID: vessel.ID, Code: vessel.Code, Role: roleVessel})
			}
		}
		item.RemoveIndexes = append(item.RemoveIndexes, run.Code)
		resolution.items = append(resolution.items, item)
	}
}

func appendReference(existing []ReferenceView, addition ReferenceView) []ReferenceView {
	for _, ref := range existing {
		if ref.Kind == addition.Kind && ref.ID == addition.ID {
			return existing
		}
	}
	return append(existing, addition)
}

func allItemsLocated(items []PlanItemView, location string) bool {
	if len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Location != location {
			return false
		}
	}
	return true
}

func groupContaining(history archive.Snapshot, requested map[string]struct{}) string {
	for i := len(history.Groups) - 1; i >= 0; i-- {
		group := history.Groups[i]
		matches := 0
		for _, key := range group.EntryKeys {
			if _, ok := requested[key]; ok {
				matches++
			}
		}
		if matches == len(requested) {
			return group.ID
		}
	}
	return ""
}
