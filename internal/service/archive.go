package service

import (
	"fmt"
	"sort"
	"strings"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

// Archive item statuses reported by archive previews.
const (
	archiveStatusReady     = "ready"
	archiveStatusArchived  = "already_archived"
	archiveStatusBlocked   = "blocked"
	archiveStatusNotFound  = "not_found"
	restoreStatusReady     = "ready"
	restoreStatusRestored  = "already_restored"
	restoreStatusConflict  = "conflict"
	referenceIncluded      = "included"
	referenceSeparateEntry = "separate_entry"
	referenceWorkspace     = "workspace"
	referenceHistory       = "history"
)

type ArchiveInput struct {
	ObjectIDs []string
}

type RestoreInput struct {
	EntryIDs []string
}

// ArchiveReference describes how one archived object relates to another
// object and where that related object will live after the archive confirm.
type ArchiveReference struct {
	Kind        domain.ObjectKind `json:"kind"`
	ID          string            `json:"id"`
	Label       string            `json:"label"`
	Disposition string            `json:"disposition"`
}

// ArchiveObjectSummary is one object inside a planned or stored entry.
type ArchiveObjectSummary struct {
	Kind             domain.ObjectKind  `json:"kind"`
	ID               string             `json:"id"`
	Label            string             `json:"label"`
	Version          int64              `json:"version"`
	ObservationCount int                `json:"observation_count,omitempty"`
	References       []ArchiveReference `json:"references,omitempty"`
}

// ArchivePlanItem reports the decision for one requested object identifier.
type ArchivePlanItem struct {
	ObjectID string            `json:"object_id"`
	Kind     domain.ObjectKind `json:"kind,omitempty"`
	Status   string            `json:"status"`
	EntryID  string            `json:"entry_id,omitempty"`
	Detail   string            `json:"detail,omitempty"`
	code     domain.Code
}

// ArchivePlanEntry is the entry that a confirm would create or reuse.
type ArchivePlanEntry struct {
	EntryID  string                 `json:"entry_id"`
	Existing bool                   `json:"existing"`
	Objects  []ArchiveObjectSummary `json:"objects"`
}

// ArchivePlan is the preview of an archive request: which versions,
// observations, and references would move to the history area.
type ArchivePlan struct {
	Ready   bool               `json:"ready"`
	Items   []ArchivePlanItem  `json:"items"`
	Entries []ArchivePlanEntry `json:"entries"`
}

// ArchiveResult is the outcome of a confirmed archive request.
type ArchiveResult struct {
	Plan    ArchivePlan           `json:"plan"`
	Entries []domain.ArchiveEntry `json:"entries"`
}

// RestoreConflict explains why one archived object cannot return to the
// workspace right now.
type RestoreConflict struct {
	Code   domain.Code `json:"code"`
	Detail string      `json:"detail"`
}

// RestorePlanItem reports the decision for one object inside the requested
// archive entries.
type RestorePlanItem struct {
	EntryID   string            `json:"entry_id"`
	Kind      domain.ObjectKind `json:"kind"`
	ObjectID  string            `json:"object_id"`
	Label     string            `json:"label"`
	Version   int64             `json:"version"`
	Status    string            `json:"status"`
	Conflicts []RestoreConflict `json:"conflicts,omitempty"`
}

// RestorePlan is the preview of a restore request after checking live names,
// codes, and run references.
type RestorePlan struct {
	Ready bool              `json:"ready"`
	Items []RestorePlanItem `json:"items"`
}

// RestoreResult is the outcome of a confirmed restore request.
type RestoreResult struct {
	Plan            RestorePlan              `json:"plan"`
	Formulas        []domain.Formula         `json:"formulas"`
	Vessels         []domain.Vessel          `json:"vessels"`
	Runs            []domain.FermentationRun `json:"runs"`
	RemovedEntryIDs []string                 `json:"removed_entry_ids"`
}

// ArchiveEntrySummary describes one stored entry without its full payload.
type ArchiveEntrySummary struct {
	EntryID   string                 `json:"entry_id"`
	CreatedAt string                 `json:"created_at"`
	Objects   []ArchiveObjectSummary `json:"objects"`
}

// entrySpec accumulates the objects that will form one archive entry.
type entrySpec struct {
	id       string
	formulas []domain.Formula
	vessels  []domain.Vessel
	runs     []domain.FermentationRun
}

func (s *Service) PlanArchive(input ArchiveInput) (ArchivePlan, error) {
	plan, _, err := s.buildArchivePlan(input.ObjectIDs)
	return plan, err
}

func (s *Service) ConfirmArchive(input ArchiveInput) (ArchiveResult, error) {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	plan, specs, err := s.buildArchivePlan(input.ObjectIDs)
	if err != nil {
		return ArchiveResult{}, err
	}
	if !plan.Ready {
		return ArchiveResult{}, firstArchiveBlocker(plan)
	}

	now := s.clock.Now()
	entries := make([]domain.ArchiveEntry, 0, len(specs))
	for _, spec := range specs {
		entry := domain.NewArchiveEntry(spec.id, now)
		for _, formula := range spec.formulas {
			entry.Formulas[formula.ID] = formula.Clone()
		}
		for _, vessel := range spec.vessels {
			entry.Vessels[vessel.ID] = vessel.Clone()
		}
		for _, run := range spec.runs {
			entry.Runs[run.ID] = run.Clone()
		}
		entries = append(entries, entry)
	}

	// Copy to the history area first. If this fails the workspace is
	// untouched; if the workspace removal below fails, the history copy is
	// already durable and a retry converges on the same entry identifiers.
	if len(entries) > 0 {
		if err := s.archive.Put(entries); err != nil {
			return ArchiveResult{}, err
		}
	}

	if len(specs) > 0 {
		err := s.store.Execute(func(snapshot *domain.Snapshot) error {
			for _, spec := range specs {
				for _, formula := range spec.formulas {
					if err := removeArchivedFormula(snapshot, formula); err != nil {
						return err
					}
				}
				for _, vessel := range spec.vessels {
					if err := removeArchivedVessel(snapshot, vessel); err != nil {
						return err
					}
				}
				for _, run := range spec.runs {
					if err := removeArchivedRun(snapshot, run); err != nil {
						return err
					}
				}
			}
			return nil
		})
		if err != nil {
			return ArchiveResult{}, err
		}
	}

	stored, err := s.collectEntries(plan.entryIDs())
	if err != nil {
		return ArchiveResult{}, err
	}
	return ArchiveResult{Plan: plan, Entries: stored}, nil
}

func (s *Service) PlanRestore(input RestoreInput) (RestorePlan, error) {
	plan, _, err := s.buildRestorePlan(input.EntryIDs)
	return plan, err
}

func (s *Service) ConfirmRestore(input RestoreInput) (RestoreResult, error) {
	s.archiveMu.Lock()
	defer s.archiveMu.Unlock()

	plan, bundle, err := s.buildRestorePlan(input.EntryIDs)
	if err != nil {
		return RestoreResult{}, err
	}
	if !plan.Ready {
		return RestoreResult{}, firstRestoreConflict(plan)
	}

	// Bring every object back in one snapshot transaction. If this fails the
	// archive entries stay untouched, so nothing is half restored.
	restoreErr := s.store.Execute(func(snapshot *domain.Snapshot) error {
		for _, formula := range bundle.formulas {
			if err := restoreFormula(snapshot, formula); err != nil {
				return err
			}
		}
		for _, vessel := range bundle.vessels {
			if err := restoreVessel(snapshot, vessel); err != nil {
				return err
			}
		}
		for _, run := range bundle.runs {
			if err := restoreRun(snapshot, run); err != nil {
				return err
			}
		}
		return restoreReferencesHold(snapshot, bundle.runs)
	})
	if restoreErr != nil {
		return RestoreResult{}, restoreErr
	}

	entryIDs := dedupePreserveOrder(input.EntryIDs)
	if err := s.archive.Remove(entryIDs); err != nil {
		return RestoreResult{}, err
	}

	result := RestoreResult{
		Plan:            plan,
		Formulas:        make([]domain.Formula, 0, len(bundle.formulas)),
		Vessels:         make([]domain.Vessel, 0, len(bundle.vessels)),
		Runs:            make([]domain.FermentationRun, 0, len(bundle.runs)),
		RemovedEntryIDs: entryIDs,
	}
	for _, formula := range bundle.formulas {
		result.Formulas = append(result.Formulas, formula.Clone())
	}
	for _, vessel := range bundle.vessels {
		result.Vessels = append(result.Vessels, vessel.Clone())
	}
	for _, run := range bundle.runs {
		result.Runs = append(result.Runs, run.Clone())
	}
	return result, nil
}

func (s *Service) ListArchiveEntries() ([]ArchiveEntrySummary, error) {
	var summaries []ArchiveEntrySummary
	err := s.archive.View(func(archive domain.Archive) error {
		summaries = make([]ArchiveEntrySummary, 0, len(archive.Entries))
		ids := make([]string, 0, len(archive.Entries))
		for id := range archive.Entries {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entry := archive.Entries[id]
			summaries = append(summaries, ArchiveEntrySummary{
				EntryID:   entry.ID,
				CreatedAt: entry.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z"),
				Objects:   summarizeEntryObjects(entry),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return summaries, nil
}

func (s *Service) GetArchiveEntry(id string) (domain.ArchiveEntry, error) {
	var found domain.ArchiveEntry
	err := s.archive.View(func(archive domain.Archive) error {
		entry, ok := archive.Entries[id]
		if !ok {
			return domain.NewError(domain.CodeArchiveEntryMissing,
				"archive entry was not found")
		}
		found = entry.Clone()
		return nil
	})
	if err != nil {
		return domain.ArchiveEntry{}, err
	}
	return found, nil
}

// buildArchivePlan validates the requested objects against the live snapshot
// and the archive, and computes the entries a confirm would persist.
func (s *Service) buildArchivePlan(objectIDs []string) (ArchivePlan, []entrySpec, error) {
	ids := dedupePreserveOrder(objectIDs)
	if len(ids) == 0 {
		return ArchivePlan{}, nil, domain.NewError(domain.CodeInvalidInput,
			"at least one object id is required")
	}
	var live domain.Snapshot
	if err := s.store.View(func(snapshot domain.Snapshot) error {
		live = snapshot
		return nil
	}); err != nil {
		return ArchivePlan{}, nil, err
	}
	var history domain.Archive
	if err := s.archive.View(func(archive domain.Archive) error {
		history = archive
		return nil
	}); err != nil {
		return ArchivePlan{}, nil, err
	}

	plan := ArchivePlan{Ready: true}
	items := make(map[string]*ArchivePlanItem, len(ids))
	var specs []entrySpec
	specByObject := make(map[string]int)
	foldedRuns := make(map[string]string)
	existingEntries := make(map[string]bool)

	for _, id := range ids {
		item := &ArchivePlanItem{ObjectID: id}
		items[id] = item
		switch {
		case hasFormula(live, id):
			item.Kind = domain.ObjectFormula
		case hasVessel(live, id):
			item.Kind = domain.ObjectVessel
		case hasRun(live, id):
			item.Kind = domain.ObjectRun
		default:
			if entryID, kind, found := history.FindObject(id); found {
				item.Kind = kind
				item.Status = archiveStatusArchived
				item.EntryID = entryID
				item.Detail = "object is already in the history area"
				existingEntries[entryID] = true
				continue
			}
			item.Status = archiveStatusNotFound
			item.Detail = "object was not found in the workspace or the history area"
			plan.Ready = false
		}
	}

	// Formulas collect their terminal runs into the same entry.
	for _, id := range ids {
		item := items[id]
		if item.Kind != domain.ObjectFormula || item.Status != "" {
			continue
		}
		formula := live.Formulas[id]
		if formula.State != domain.FormulaRetired {
			blockItem(&plan, item, domain.CodeFormulaState, fmt.Sprintf(
				"only a retired formula can be archived; current state is %q", formula.State))
			continue
		}
		runs, blockerCode, blocker := terminalRunsForFormula(live, id)
		if blocker != "" {
			blockItem(&plan, item, blockerCode, blocker)
			continue
		}
		specs = append(specs, entrySpec{formulas: []domain.Formula{formula}, runs: runs})
		specByObject[id] = len(specs) - 1
		for _, run := range runs {
			foldedRuns[run.ID] = id
			specByObject[run.ID] = len(specs) - 1
		}
		item.Status = archiveStatusReady
	}

	for _, id := range ids {
		item := items[id]
		if item.Kind != domain.ObjectVessel || item.Status != "" {
			continue
		}
		vessel := live.Vessels[id]
		if vessel.State != domain.VesselRetired {
			blockItem(&plan, item, domain.CodeVesselState, fmt.Sprintf(
				"only a retired vessel can be archived; current state is %q", vessel.State))
			continue
		}
		specs = append(specs, entrySpec{vessels: []domain.Vessel{vessel}})
		specByObject[id] = len(specs) - 1
		item.Status = archiveStatusReady
	}

	for _, id := range ids {
		item := items[id]
		if item.Kind != domain.ObjectRun || item.Status != "" {
			continue
		}
		if formulaID, folded := foldedRuns[id]; folded {
			item.Status = archiveStatusReady
			item.Detail = fmt.Sprintf("run is archived together with formula %s", formulaID)
			continue
		}
		run := live.Runs[id]
		if run.State != domain.RunCompleted && run.State != domain.RunAborted {
			blockItem(&plan, item, domain.CodeRunState, fmt.Sprintf(
				"only a completed or aborted run can be archived; current state is %q", run.State))
			continue
		}
		if blocker := vesselLinkBlocker(live, run); blocker != "" {
			blockItem(&plan, item, domain.CodeVesselState, blocker)
			continue
		}
		specs = append(specs, entrySpec{runs: []domain.FermentationRun{run}})
		specByObject[id] = len(specs) - 1
		item.Status = archiveStatusReady
	}

	plan.Entries = make([]ArchivePlanEntry, 0, len(specs)+len(existingEntries))
	for index := range specs {
		spec := &specs[index]
		spec.id = domain.ArchiveEntryID(spec.formulas, spec.vessels, spec.runs)
		_, existing := history.Entries[spec.id]
		plan.Entries = append(plan.Entries, ArchivePlanEntry{
			EntryID:  spec.id,
			Existing: existing,
			Objects:  summarizeSpec(*spec, live, history, specs, index),
		})
		for objectID, specIndex := range specByObject {
			if specIndex == index {
				if item, ok := items[objectID]; ok {
					item.EntryID = spec.id
				}
			}
		}
	}
	for entryID := range existingEntries {
		entry := history.Entries[entryID]
		plan.Entries = append(plan.Entries, ArchivePlanEntry{
			EntryID:  entryID,
			Existing: true,
			Objects:  summarizeEntryObjects(entry),
		})
	}
	sort.Slice(plan.Entries, func(i, j int) bool {
		return plan.Entries[i].EntryID < plan.Entries[j].EntryID
	})

	plan.Items = make([]ArchivePlanItem, 0, len(ids))
	for _, id := range ids {
		plan.Items = append(plan.Items, *items[id])
	}
	return plan, specs, nil
}

// buildRestorePlan checks the requested entries against live names, codes,
// and run references, and merges the objects a confirm would restore.
func (s *Service) buildRestorePlan(entryIDs []string) (RestorePlan, entrySpec, error) {
	ids := dedupePreserveOrder(entryIDs)
	if len(ids) == 0 {
		return RestorePlan{}, entrySpec{}, domain.NewError(domain.CodeInvalidInput,
			"at least one archive entry id is required")
	}
	var history domain.Archive
	if err := s.archive.View(func(archive domain.Archive) error {
		history = archive
		return nil
	}); err != nil {
		return RestorePlan{}, entrySpec{}, err
	}
	var live domain.Snapshot
	if err := s.store.View(func(snapshot domain.Snapshot) error {
		live = snapshot
		return nil
	}); err != nil {
		return RestorePlan{}, entrySpec{}, err
	}

	entries := make([]domain.ArchiveEntry, 0, len(ids))
	for _, id := range ids {
		entry, ok := history.Entries[id]
		if !ok {
			return RestorePlan{}, entrySpec{}, domain.NewError(domain.CodeArchiveEntryMissing,
				fmt.Sprintf("archive entry %s was not found", id))
		}
		entries = append(entries, entry)
	}

	bundle, err := mergeEntries(entries)
	if err != nil {
		return RestorePlan{}, entrySpec{}, err
	}

	plan := RestorePlan{Ready: true}
	for _, entry := range entries {
		for _, formula := range sortedFormulas(entry.Formulas) {
			plan.Items = append(plan.Items, checkFormulaRestore(live, entry.ID, formula))
		}
		for _, vessel := range sortedVessels(entry.Vessels) {
			plan.Items = append(plan.Items, checkVesselRestore(live, entry.ID, vessel))
		}
		for _, run := range sortedRuns(entry.Runs) {
			plan.Items = append(plan.Items, checkRunRestore(live, bundle, entry.ID, run))
		}
	}
	for _, item := range plan.Items {
		if item.Status == restoreStatusConflict {
			plan.Ready = false
			break
		}
	}
	return plan, bundle, nil
}

func mergeEntries(entries []domain.ArchiveEntry) (entrySpec, error) {
	bundle := entrySpec{}
	seen := make(map[string]bool)
	for _, entry := range entries {
		for _, formula := range sortedFormulas(entry.Formulas) {
			if seen[formula.ID] {
				return entrySpec{}, domain.NewError(domain.CodeArchiveConflict,
					fmt.Sprintf("object %s appears in more than one requested entry", formula.ID))
			}
			seen[formula.ID] = true
			bundle.formulas = append(bundle.formulas, formula.Clone())
		}
		for _, vessel := range sortedVessels(entry.Vessels) {
			if seen[vessel.ID] {
				return entrySpec{}, domain.NewError(domain.CodeArchiveConflict,
					fmt.Sprintf("object %s appears in more than one requested entry", vessel.ID))
			}
			seen[vessel.ID] = true
			bundle.vessels = append(bundle.vessels, vessel.Clone())
		}
		for _, run := range sortedRuns(entry.Runs) {
			if seen[run.ID] {
				return entrySpec{}, domain.NewError(domain.CodeArchiveConflict,
					fmt.Sprintf("object %s appears in more than one requested entry", run.ID))
			}
			seen[run.ID] = true
			bundle.runs = append(bundle.runs, run.Clone())
		}
	}
	return bundle, nil
}

func checkFormulaRestore(
	live domain.Snapshot,
	entryID string,
	formula domain.Formula,
) RestorePlanItem {
	item := RestorePlanItem{
		EntryID:  entryID,
		Kind:     domain.ObjectFormula,
		ObjectID: formula.ID,
		Label:    formula.Name,
		Version:  formula.Version,
		Status:   restoreStatusReady,
	}
	if owner, taken := live.NameIndex[domain.NameKey(formula.Name)]; taken && owner != formula.ID {
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code: domain.CodeFormulaNameConflict,
			Detail: fmt.Sprintf(
				"a live formula already uses the name %q", formula.Name),
		})
	}
	if current, exists := live.Formulas[formula.ID]; exists {
		if domain.FormulasEqual(current, formula) {
			item.Status = restoreStatusRestored
			return item
		}
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code:   domain.CodeArchiveConflict,
			Detail: "a different live formula already uses this identifier",
		})
	}
	return markConflicts(item)
}

func checkVesselRestore(
	live domain.Snapshot,
	entryID string,
	vessel domain.Vessel,
) RestorePlanItem {
	item := RestorePlanItem{
		EntryID:  entryID,
		Kind:     domain.ObjectVessel,
		ObjectID: vessel.ID,
		Label:    vessel.Code,
		Version:  vessel.Version,
		Status:   restoreStatusReady,
	}
	if owner, taken := live.VesselIndex[vessel.Code]; taken && owner != vessel.ID {
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code: domain.CodeVesselConflict,
			Detail: fmt.Sprintf(
				"a live vessel already uses the code %q", vessel.Code),
		})
	}
	if current, exists := live.Vessels[vessel.ID]; exists {
		if domain.VesselsEqual(current, vessel) {
			item.Status = restoreStatusRestored
			return item
		}
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code:   domain.CodeArchiveConflict,
			Detail: "a different live vessel already uses this identifier",
		})
	}
	return markConflicts(item)
}

func checkRunRestore(
	live domain.Snapshot,
	bundle entrySpec,
	entryID string,
	run domain.FermentationRun,
) RestorePlanItem {
	item := RestorePlanItem{
		EntryID:  entryID,
		Kind:     domain.ObjectRun,
		ObjectID: run.ID,
		Label:    run.Code,
		Version:  run.Version,
		Status:   restoreStatusReady,
	}
	if owner, taken := live.RunIndex[run.Code]; taken && owner != run.ID {
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code: domain.CodeRunConflict,
			Detail: fmt.Sprintf(
				"a live run already uses the code %q", run.Code),
		})
	}
	if current, exists := live.Runs[run.ID]; exists {
		if domain.RunsEqual(current, run) {
			item.Status = restoreStatusRestored
			return item
		}
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code:   domain.CodeArchiveConflict,
			Detail: "a different live run already uses this identifier",
		})
	}
	if _, ok := live.Formulas[run.FormulaID]; !ok && !bundle.hasFormula(run.FormulaID) {
		item.Conflicts = append(item.Conflicts, RestoreConflict{
			Code: domain.CodeArchiveReference,
			Detail: fmt.Sprintf(
				"run %s references formula %s, which is neither live nor part of this restore",
				run.Code, run.FormulaID),
		})
	}
	if run.VesselID != nil {
		if _, ok := live.Vessels[*run.VesselID]; !ok && !bundle.hasVessel(*run.VesselID) {
			item.Conflicts = append(item.Conflicts, RestoreConflict{
				Code: domain.CodeArchiveReference,
				Detail: fmt.Sprintf(
					"run %s references vessel %s, which is neither live nor part of this restore",
					run.Code, *run.VesselID),
			})
		}
	}
	return markConflicts(item)
}

func markConflicts(item RestorePlanItem) RestorePlanItem {
	if len(item.Conflicts) > 0 {
		item.Status = restoreStatusConflict
	}
	return item
}

func (s *Service) collectEntries(entryIDs []string) ([]domain.ArchiveEntry, error) {
	entries := make([]domain.ArchiveEntry, 0, len(entryIDs))
	err := s.archive.View(func(archive domain.Archive) error {
		for _, id := range entryIDs {
			entry, ok := archive.Entries[id]
			if !ok {
				return domain.NewError(domain.CodeStorage,
					fmt.Sprintf("archived entry %s is missing after confirm", id))
			}
			entries = append(entries, entry.Clone())
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func (p ArchivePlan) entryIDs() []string {
	ids := make([]string, 0, len(p.Entries))
	for _, entry := range p.Entries {
		ids = append(ids, entry.EntryID)
	}
	return ids
}

func firstArchiveBlocker(plan ArchivePlan) error {
	for _, item := range plan.Items {
		if item.Status == archiveStatusNotFound {
			return domain.NewError(domain.CodeInvalidInput,
				fmt.Sprintf("object %s: %s", item.ObjectID, item.Detail))
		}
		if item.Status == archiveStatusBlocked {
			code := item.code
			if code == "" {
				code = domain.CodeArchiveConflict
			}
			return domain.NewError(code, item.Detail)
		}
	}
	return domain.NewError(domain.CodeArchiveConflict, "archive request is blocked")
}

func firstRestoreConflict(plan RestorePlan) error {
	for _, item := range plan.Items {
		if item.Status != restoreStatusConflict || len(item.Conflicts) == 0 {
			continue
		}
		conflict := item.Conflicts[0]
		return domain.NewError(conflict.Code, conflict.Detail)
	}
	return domain.NewError(domain.CodeArchiveConflict, "restore request has conflicts")
}

func removeArchivedFormula(snapshot *domain.Snapshot, formula domain.Formula) error {
	current, ok := snapshot.Formulas[formula.ID]
	if !ok {
		// A concurrent confirm already moved this object; the archive holds it.
		return nil
	}
	if current.Version != formula.Version || current.State != domain.FormulaRetired {
		return domain.NewError(domain.CodeArchiveConflict,
			fmt.Sprintf("formula %s changed while archiving", formula.ID))
	}
	delete(snapshot.Formulas, formula.ID)
	delete(snapshot.NameIndex, domain.NameKey(current.Name))
	return nil
}

func removeArchivedVessel(snapshot *domain.Snapshot, vessel domain.Vessel) error {
	current, ok := snapshot.Vessels[vessel.ID]
	if !ok {
		return nil
	}
	if current.Version != vessel.Version || current.State != domain.VesselRetired {
		return domain.NewError(domain.CodeArchiveConflict,
			fmt.Sprintf("vessel %s changed while archiving", vessel.ID))
	}
	delete(snapshot.Vessels, vessel.ID)
	delete(snapshot.VesselIndex, current.Code)
	return nil
}

func removeArchivedRun(snapshot *domain.Snapshot, run domain.FermentationRun) error {
	current, ok := snapshot.Runs[run.ID]
	if !ok {
		return nil
	}
	if current.Version != run.Version ||
		(current.State != domain.RunCompleted && current.State != domain.RunAborted) {
		return domain.NewError(domain.CodeArchiveConflict,
			fmt.Sprintf("run %s changed while archiving", run.ID))
	}
	if blocker := vesselLinkBlocker(*snapshot, current); blocker != "" {
		return domain.NewError(domain.CodeVesselState, blocker)
	}
	delete(snapshot.Runs, run.ID)
	delete(snapshot.RunIndex, current.Code)
	return nil
}

func restoreFormula(snapshot *domain.Snapshot, formula domain.Formula) error {
	key := domain.NameKey(formula.Name)
	if owner, taken := snapshot.NameIndex[key]; taken && owner != formula.ID {
		return domain.NewError(domain.CodeFormulaNameConflict,
			fmt.Sprintf("a live formula already uses the name %q", formula.Name))
	}
	if current, exists := snapshot.Formulas[formula.ID]; exists {
		if domain.FormulasEqual(current, formula) {
			return nil
		}
		return domain.NewError(domain.CodeArchiveConflict,
			fmt.Sprintf("a different live formula already uses identifier %s", formula.ID))
	}
	snapshot.Formulas[formula.ID] = formula.Clone()
	snapshot.NameIndex[key] = formula.ID
	return nil
}

func restoreVessel(snapshot *domain.Snapshot, vessel domain.Vessel) error {
	if owner, taken := snapshot.VesselIndex[vessel.Code]; taken && owner != vessel.ID {
		return domain.NewError(domain.CodeVesselConflict,
			fmt.Sprintf("a live vessel already uses the code %q", vessel.Code))
	}
	if current, exists := snapshot.Vessels[vessel.ID]; exists {
		if domain.VesselsEqual(current, vessel) {
			return nil
		}
		return domain.NewError(domain.CodeArchiveConflict,
			fmt.Sprintf("a different live vessel already uses identifier %s", vessel.ID))
	}
	snapshot.Vessels[vessel.ID] = vessel.Clone()
	snapshot.VesselIndex[vessel.Code] = vessel.ID
	return nil
}

func restoreRun(snapshot *domain.Snapshot, run domain.FermentationRun) error {
	if owner, taken := snapshot.RunIndex[run.Code]; taken && owner != run.ID {
		return domain.NewError(domain.CodeRunConflict,
			fmt.Sprintf("a live run already uses the code %q", run.Code))
	}
	if current, exists := snapshot.Runs[run.ID]; exists {
		if domain.RunsEqual(current, run) {
			return nil
		}
		return domain.NewError(domain.CodeArchiveConflict,
			fmt.Sprintf("a different live run already uses identifier %s", run.ID))
	}
	snapshot.Runs[run.ID] = run.Clone()
	snapshot.RunIndex[run.Code] = run.ID
	return nil
}

func restoreReferencesHold(snapshot *domain.Snapshot, runs []domain.FermentationRun) error {
	for _, run := range runs {
		if _, ok := snapshot.Formulas[run.FormulaID]; !ok {
			return domain.NewError(domain.CodeArchiveReference,
				fmt.Sprintf("run %s references formula %s, which is not live after restore",
					run.Code, run.FormulaID))
		}
		if run.VesselID != nil {
			if _, ok := snapshot.Vessels[*run.VesselID]; !ok {
				return domain.NewError(domain.CodeArchiveReference,
					fmt.Sprintf("run %s references vessel %s, which is not live after restore",
						run.Code, *run.VesselID))
			}
		}
	}
	return nil
}

func terminalRunsForFormula(
	snapshot domain.Snapshot,
	formulaID string,
) ([]domain.FermentationRun, domain.Code, string) {
	runs := make([]domain.FermentationRun, 0)
	for _, run := range snapshot.Runs {
		if run.FormulaID != formulaID {
			continue
		}
		if run.State != domain.RunCompleted && run.State != domain.RunAborted {
			return nil, domain.CodeFormulaInUse,
				fmt.Sprintf("run %s still uses this formula", run.Code)
		}
		if blocker := vesselLinkBlocker(snapshot, run); blocker != "" {
			return nil, domain.CodeVesselState, blocker
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool { return runs[i].ID < runs[j].ID })
	return runs, "", ""
}

func vesselLinkBlocker(snapshot domain.Snapshot, run domain.FermentationRun) string {
	if run.VesselID == nil {
		return ""
	}
	vessel, ok := snapshot.Vessels[*run.VesselID]
	if !ok || vessel.ActiveRunID == nil || *vessel.ActiveRunID != run.ID {
		return ""
	}
	return fmt.Sprintf(
		"vessel %s is still linked to run %s; finish cleaning before archiving",
		vessel.Code, run.Code)
}

func summarizeSpec(
	spec entrySpec,
	live domain.Snapshot,
	history domain.Archive,
	specs []entrySpec,
	ownIndex int,
) []ArchiveObjectSummary {
	summaries := make([]ArchiveObjectSummary, 0,
		len(spec.formulas)+len(spec.vessels)+len(spec.runs))
	for _, formula := range spec.formulas {
		summaries = append(summaries, ArchiveObjectSummary{
			Kind:    domain.ObjectFormula,
			ID:      formula.ID,
			Label:   formula.Name,
			Version: formula.Version,
		})
	}
	for _, vessel := range spec.vessels {
		summaries = append(summaries, ArchiveObjectSummary{
			Kind:       domain.ObjectVessel,
			ID:         vessel.ID,
			Label:      vessel.Code,
			Version:    vessel.Version,
			References: vesselReferences(live, vessel),
		})
	}
	for _, run := range spec.runs {
		summaries = append(summaries, ArchiveObjectSummary{
			Kind:             domain.ObjectRun,
			ID:               run.ID,
			Label:            run.Code,
			Version:          run.Version,
			ObservationCount: len(run.Observations),
			References:       runReferences(spec, live, history, specs, ownIndex, run),
		})
	}
	return summaries
}

func summarizeEntryObjects(entry domain.ArchiveEntry) []ArchiveObjectSummary {
	summaries := make([]ArchiveObjectSummary, 0,
		len(entry.Formulas)+len(entry.Vessels)+len(entry.Runs))
	for _, formula := range sortedFormulas(entry.Formulas) {
		summaries = append(summaries, ArchiveObjectSummary{
			Kind:    domain.ObjectFormula,
			ID:      formula.ID,
			Label:   formula.Name,
			Version: formula.Version,
		})
	}
	for _, vessel := range sortedVessels(entry.Vessels) {
		summaries = append(summaries, ArchiveObjectSummary{
			Kind:    domain.ObjectVessel,
			ID:      vessel.ID,
			Label:   vessel.Code,
			Version: vessel.Version,
		})
	}
	for _, run := range sortedRuns(entry.Runs) {
		summaries = append(summaries, ArchiveObjectSummary{
			Kind:             domain.ObjectRun,
			ID:               run.ID,
			Label:            run.Code,
			Version:          run.Version,
			ObservationCount: len(run.Observations),
		})
	}
	return summaries
}

// runReferences reports where the objects a run points at will live after the
// archive confirm: inside the same entry, in another entry of this batch,
// still in the workspace, or already in the history area.
func runReferences(
	spec entrySpec,
	live domain.Snapshot,
	history domain.Archive,
	specs []entrySpec,
	ownIndex int,
	run domain.FermentationRun,
) []ArchiveReference {
	references := make([]ArchiveReference, 0, 2)
	references = append(references, ArchiveReference{
		Kind:        domain.ObjectFormula,
		ID:          run.FormulaID,
		Label:       formulaLabel(live, history, run.FormulaID),
		Disposition: referenceDisposition(spec.hasFormula(run.FormulaID), false, live, history, specs, ownIndex, run.FormulaID),
	})
	if run.VesselID != nil {
		references = append(references, ArchiveReference{
			Kind:        domain.ObjectVessel,
			ID:          *run.VesselID,
			Label:       vesselLabel(live, history, *run.VesselID),
			Disposition: referenceDisposition(spec.hasVessel(*run.VesselID), true, live, history, specs, ownIndex, *run.VesselID),
		})
	}
	return references
}

func referenceDisposition(
	inOwnEntry bool,
	isVessel bool,
	live domain.Snapshot,
	history domain.Archive,
	specs []entrySpec,
	ownIndex int,
	id string,
) string {
	if inOwnEntry {
		return referenceIncluded
	}
	for index, spec := range specs {
		if index == ownIndex {
			continue
		}
		if spec.hasFormula(id) || spec.hasVessel(id) || spec.hasRun(id) {
			return referenceSeparateEntry
		}
	}
	if isVessel {
		if _, ok := live.Vessels[id]; ok {
			return referenceWorkspace
		}
	} else {
		if _, ok := live.Formulas[id]; ok {
			return referenceWorkspace
		}
	}
	return referenceHistory
}

// vesselReferences lists live runs that keep pointing at an archived vessel;
// they stay in the workspace when the vessel moves to the history area.
func vesselReferences(live domain.Snapshot, vessel domain.Vessel) []ArchiveReference {
	references := make([]ArchiveReference, 0)
	for _, run := range sortedRuns(live.Runs) {
		if run.VesselID == nil || *run.VesselID != vessel.ID {
			continue
		}
		references = append(references, ArchiveReference{
			Kind:        domain.ObjectRun,
			ID:          run.ID,
			Label:       run.Code,
			Disposition: referenceWorkspace,
		})
	}
	return references
}

func formulaLabel(live domain.Snapshot, history domain.Archive, id string) string {
	if formula, ok := live.Formulas[id]; ok {
		return formula.Name
	}
	for _, entry := range history.Entries {
		if formula, ok := entry.Formulas[id]; ok {
			return formula.Name
		}
	}
	return id
}

func vesselLabel(live domain.Snapshot, history domain.Archive, id string) string {
	if vessel, ok := live.Vessels[id]; ok {
		return vessel.Code
	}
	for _, entry := range history.Entries {
		if vessel, ok := entry.Vessels[id]; ok {
			return vessel.Code
		}
	}
	return id
}

func (s entrySpec) hasFormula(id string) bool {
	for _, formula := range s.formulas {
		if formula.ID == id {
			return true
		}
	}
	return false
}

func (s entrySpec) hasVessel(id string) bool {
	for _, vessel := range s.vessels {
		if vessel.ID == id {
			return true
		}
	}
	return false
}

func (s entrySpec) hasRun(id string) bool {
	for _, run := range s.runs {
		if run.ID == id {
			return true
		}
	}
	return false
}

func hasFormula(snapshot domain.Snapshot, id string) bool {
	_, ok := snapshot.Formulas[id]
	return ok
}

func hasVessel(snapshot domain.Snapshot, id string) bool {
	_, ok := snapshot.Vessels[id]
	return ok
}

func hasRun(snapshot domain.Snapshot, id string) bool {
	_, ok := snapshot.Runs[id]
	return ok
}

func blockItem(plan *ArchivePlan, item *ArchivePlanItem, code domain.Code, detail string) {
	item.Status = archiveStatusBlocked
	item.Detail = detail
	item.code = code
	plan.Ready = false
}

func dedupePreserveOrder(ids []string) []string {
	seen := make(map[string]bool, len(ids))
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		trimmed := strings.TrimSpace(id)
		if trimmed == "" || seen[trimmed] {
			continue
		}
		seen[trimmed] = true
		result = append(result, trimmed)
	}
	return result
}

func sortedFormulas(formulas map[string]domain.Formula) []domain.Formula {
	result := make([]domain.Formula, 0, len(formulas))
	for _, formula := range formulas {
		result = append(result, formula)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedVessels(vessels map[string]domain.Vessel) []domain.Vessel {
	result := make([]domain.Vessel, 0, len(vessels))
	for _, vessel := range vessels {
		result = append(result, vessel)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func sortedRuns(runs map[string]domain.FermentationRun) []domain.FermentationRun {
	result := make([]domain.FermentationRun, 0, len(runs))
	for _, run := range runs {
		result = append(result, run)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}
