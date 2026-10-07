package service

import (
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/archive"
	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// restoreResolution is the working set behind a restore preview. Explicit runs
// pull their archived referents into the same batch.
type restoreResolution struct {
	formulaIDs map[string]struct{}
	vesselIDs  map[string]struct{}
	runIDs     map[string]struct{}
	items      []PlanItemView
	blockers   []PlanBlocker
}

func newRestoreResolution() *restoreResolution {
	return &restoreResolution{
		formulaIDs: map[string]struct{}{},
		vesselIDs:  map[string]struct{}{},
		runIDs:     map[string]struct{}{},
	}
}

func (r *restoreResolution) blocker(code, kind, id, detail string) {
	r.blockers = append(r.blockers, PlanBlocker{Code: code, Kind: kind, ID: id, Detail: detail})
}

// PreviewRestore checks current names, codes, ids, and run references against
// the live workspace and produces the executable restore preview.
func (s *Service) PreviewRestore(selection RestoreSelection) (Plan, error) {
	if s.archives == nil {
		return Plan{}, domain.NewError(domain.CodeArchiveNotConfigured,
			"the historical archive repository is not configured")
	}
	return s.buildRestorePlan(selection)
}

func (s *Service) buildRestorePlan(selection RestoreSelection) (Plan, error) {
	if selection.GroupID == "" && len(selection.FormulaIDs) == 0 &&
		len(selection.VesselIDs) == 0 && len(selection.RunIDs) == 0 {
		return Plan{}, domain.FieldError(domain.CodeInvalidInput, "selection",
			"a group id or at least one archived object must be selected")
	}

	var live domain.Snapshot
	if err := s.store.View(func(snapshot domain.Snapshot) error {
		live = snapshot
		return nil
	}); err != nil {
		return Plan{}, err
	}
	var history archive.Snapshot
	if err := s.archives.View(func(snapshot archive.Snapshot) error {
		history = snapshot
		return nil
	}); err != nil {
		return Plan{}, err
	}

	resolution := newRestoreResolution()
	for _, id := range selection.FormulaIDs {
		if id != "" {
			resolution.formulaIDs[id] = struct{}{}
		}
	}
	for _, id := range selection.VesselIDs {
		if id != "" {
			resolution.vesselIDs[id] = struct{}{}
		}
	}
	for _, id := range selection.RunIDs {
		if id != "" {
			resolution.runIDs[id] = struct{}{}
		}
	}
	if selection.GroupID != "" {
		group, ok := findGroup(history, selection.GroupID)
		if !ok {
			return Plan{}, domain.NewError(domain.CodeArchiveEntryNotFound,
				"archive group was not found: "+selection.GroupID)
		}
		for _, key := range group.EntryKeys {
			kind, id, err := archive.SplitKey(key)
			if err != nil {
				return Plan{}, domain.NewError(domain.CodeStorage, err.Error())
			}
			switch kind {
			case archive.KindFormula:
				resolution.formulaIDs[id] = struct{}{}
			case archive.KindVessel:
				resolution.vesselIDs[id] = struct{}{}
			case archive.KindRun:
				resolution.runIDs[id] = struct{}{}
			}
		}
	}

	// A restored run needs its formula and vessel. Referents still in history
	// join the batch automatically; referents already live are reused.
	s.expandRunReferents(history, resolution)

	s.resolveRestoreFormulas(live, history, resolution)
	s.resolveRestoreVessels(live, history, resolution)
	s.resolveRestoreRuns(live, history, resolution)

	plan := Plan{
		Mode:            modeRestore,
		Items:           sortedItems(resolution.items),
		Blockers:        resolution.blockers,
		LiveRevision:    live.Revision,
		ArchiveRevision: history.Revision,
		GroupID:         selection.GroupID,
	}
	plan.Executable = len(plan.Blockers) == 0
	switch {
	case !plan.Executable:
		plan.Outcome = outcomeBlocked
	case len(plan.Items) > 0 && allItemsLocated(plan.Items, locationLive):
		plan.Outcome = outcomeAlreadyRestored
	default:
		plan.Outcome = outcomeReady
	}
	plan.Token = planToken(modeRestore, live.Revision, history.Revision, selection, plan.Items)
	return plan, nil
}

func (s *Service) expandRunReferents(history archive.Snapshot, resolution *restoreResolution) {
	for id := range resolution.runIDs {
		entry, ok := history.Entries[archive.EntryKey(archive.KindRun, id)]
		if !ok {
			continue // reported as a blocker during run resolution
		}
		run := entry.Run
		resolution.formulaIDs[run.FormulaID] = struct{}{}
		if run.VesselID != nil {
			resolution.vesselIDs[*run.VesselID] = struct{}{}
		}
	}
}

func (s *Service) resolveRestoreFormulas(
	live domain.Snapshot,
	history archive.Snapshot,
	resolution *restoreResolution,
) {
	for id := range resolution.formulaIDs {
		entry, ok := history.Entries[archive.EntryKey(archive.KindFormula, id)]
		if !ok {
			resolution.blocker(string(domain.CodeArchiveEntryNotFound), archive.KindFormula, id,
				"formula is not present in the archive")
			continue
		}
		archived := entry.Formula
		item := formulaItemFromArchive(archived, locationArchive)
		if current, exists := live.Formulas[id]; exists {
			if formulaEqual(current, *archived) {
				item.Location = locationLive
			} else {
				item.Location = locationLiveConflict
				resolution.blocker(string(domain.CodeRestoreConflict), archive.KindFormula, id,
					"a different live formula already occupies this id")
			}
		}
		nameKey := domain.NameKey(archived.Name)
		if occupantID, exists := live.NameIndex[nameKey]; exists && occupantID != id {
			occupant := live.Formulas[occupantID]
			item.References = append(item.References, ReferenceView{
				Kind: archive.KindFormula, ID: occupantID, Name: occupant.Name,
				Role: roleFormulaNameIndex, Location: locationLiveConflict})
			resolution.blocker(string(domain.CodeFormulaNameConflict), archive.KindFormula, id,
				"normalized name is already used by live formula "+occupantID)
		}
		resolution.items = append(resolution.items, item)
	}
}

func (s *Service) resolveRestoreVessels(
	live domain.Snapshot,
	history archive.Snapshot,
	resolution *restoreResolution,
) {
	for id := range resolution.vesselIDs {
		entry, ok := history.Entries[archive.EntryKey(archive.KindVessel, id)]
		if !ok {
			resolution.blocker(string(domain.CodeArchiveEntryNotFound), archive.KindVessel, id,
				"vessel is not present in the archive")
			continue
		}
		archived := entry.Vessel
		item := vesselItemFromArchive(archived, locationArchive)
		if current, exists := live.Vessels[id]; exists {
			if vesselEqual(&current, archived) {
				item.Location = locationLive
			} else {
				item.Location = locationLiveConflict
				resolution.blocker(string(domain.CodeRestoreConflict), archive.KindVessel, id,
					"a different live vessel already occupies this id")
			}
		}
		if occupantID, exists := live.VesselIndex[archived.Code]; exists && occupantID != id {
			item.References = append(item.References, ReferenceView{
				Kind: archive.KindVessel, ID: occupantID, Code: archived.Code,
				Role: roleVesselCodeIndex, Location: locationLiveConflict})
			resolution.blocker(string(domain.CodeVesselConflict), archive.KindVessel, id,
				"vessel code is already used by live vessel "+occupantID)
		}
		resolution.items = append(resolution.items, item)
	}
}

func (s *Service) resolveRestoreRuns(
	live domain.Snapshot,
	history archive.Snapshot,
	resolution *restoreResolution,
) {
	for id := range resolution.runIDs {
		entry, ok := history.Entries[archive.EntryKey(archive.KindRun, id)]
		if !ok {
			resolution.blocker(string(domain.CodeArchiveEntryNotFound), archive.KindRun, id,
				"run is not present in the archive")
			continue
		}
		archived := entry.Run
		item := runItemFromArchive(archived, locationArchive)
		if current, exists := live.Runs[id]; exists {
			if reflectDeepEqualRun(&current, archived) {
				item.Location = locationLive
			} else {
				item.Location = locationLiveConflict
				resolution.blocker(string(domain.CodeRestoreConflict), archive.KindRun, id,
					"a different live run already occupies this id")
			}
		}
		if occupantID, exists := live.RunIndex[archived.Code]; exists && occupantID != id {
			item.References = append(item.References, ReferenceView{
				Kind: archive.KindRun, ID: occupantID, Code: archived.Code,
				Role: roleRunCodeIndex, Location: locationLiveConflict})
			resolution.blocker(string(domain.CodeRunConflict), archive.KindRun, id,
				"run code is already used by live run "+occupantID)
		}

		// Referential closure: the formula must end up resolvable, from the
		// batch itself or from the current workspace.
		if _, inBatch := resolution.formulaIDs[archived.FormulaID]; inBatch {
			if formulaEntry, ok := history.Entries[archive.EntryKey(archive.KindFormula, archived.FormulaID)]; ok {
				item.References = append(item.References, ReferenceView{
					Kind: archive.KindFormula, ID: archived.FormulaID,
					Name: formulaEntry.Formula.Name, Role: roleFormula, Location: locationArchive})
			}
		} else if liveFormula, ok := live.Formulas[archived.FormulaID]; ok {
			item.References = append(item.References, ReferenceView{
				Kind: archive.KindFormula, ID: archived.FormulaID,
				Name: liveFormula.Name, Role: roleFormula, Location: locationLive})
		} else {
			resolution.blocker(string(domain.CodeRestoreBlocked), archive.KindRun, id,
				"referenced formula is neither in the restore batch nor in the workspace")
		}
		if archived.VesselID != nil {
			vesselID := *archived.VesselID
			if _, inBatch := resolution.vesselIDs[vesselID]; inBatch {
				if vesselEntry, ok := history.Entries[archive.EntryKey(archive.KindVessel, vesselID)]; ok {
					item.References = append(item.References, ReferenceView{
						Kind: archive.KindVessel, ID: vesselID,
						Code: vesselEntry.Vessel.Code, Role: roleVessel, Location: locationArchive})
				}
			} else if liveVessel, ok := live.Vessels[vesselID]; ok {
				item.References = append(item.References, ReferenceView{
					Kind: archive.KindVessel, ID: vesselID,
					Code: liveVessel.Code, Role: roleVessel, Location: locationLive})
			} else {
				resolution.blocker(string(domain.CodeRestoreBlocked), archive.KindRun, id,
					"referenced vessel is neither in the restore batch nor in the workspace")
			}
		}
		resolution.items = append(resolution.items, item)
	}
}

func findGroup(history archive.Snapshot, groupID string) (archive.Group, bool) {
	for _, group := range history.Groups {
		if group.ID == groupID {
			return group, true
		}
	}
	return archive.Group{}, false
}
