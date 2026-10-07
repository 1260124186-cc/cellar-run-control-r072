package workflowcheck

import (
	"fmt"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

type archiveReferenceView struct {
	Kind        string `json:"kind"`
	ID          string `json:"id"`
	Disposition string `json:"disposition"`
}

type archiveObjectView struct {
	Kind             string                 `json:"kind"`
	ID               string                 `json:"id"`
	Version          int64                  `json:"version"`
	ObservationCount int                    `json:"observation_count"`
	References       []archiveReferenceView `json:"references"`
}

type archivePlanEntryView struct {
	EntryID string              `json:"entry_id"`
	Objects []archiveObjectView `json:"objects"`
}

type archivePlanView struct {
	Ready bool `json:"ready"`
	Items []struct {
		ObjectID string `json:"object_id"`
		Kind     string `json:"kind"`
		Status   string `json:"status"`
		EntryID  string `json:"entry_id"`
	} `json:"items"`
	Entries []archivePlanEntryView `json:"entries"`
}

type archiveResultView struct {
	Entries []domain.ArchiveEntry `json:"entries"`
}

type archiveEntriesView struct {
	Entries []struct {
		EntryID string `json:"entry_id"`
	} `json:"entries"`
}

type restorePlanView struct {
	Ready bool `json:"ready"`
	Items []struct {
		Kind      string `json:"kind"`
		Status    string `json:"status"`
		Conflicts []struct {
			Code string `json:"code"`
		} `json:"conflicts"`
	} `json:"items"`
}

type restoreResultView struct {
	Formulas []domain.Formula         `json:"formulas"`
	Vessels  []domain.Vessel          `json:"vessels"`
	Runs     []domain.FermentationRun `json:"runs"`
}

func archiveRestore(api *client) error {
	formulaID, vesselID, runID, err := ensureRetiredHistory(api)
	if err != nil {
		return err
	}

	preview, err := api.call("POST", "/v1/archive/preview",
		map[string]any{"object_ids": []string{formulaID, vesselID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(preview, 200, "preview archive"); err != nil {
		return err
	}
	var plan archivePlanView
	if err := preview.decode(&plan); err != nil {
		return err
	}
	if !plan.Ready || len(plan.Entries) != 2 {
		return errInvalidWorkflow("archive preview was not ready with two entries")
	}
	formulaEntryID, vesselEntryID := "", ""
	for _, entry := range plan.Entries {
		if len(entry.Objects) == 2 {
			formulaEntryID = entry.EntryID
			if err := requireArchivedRunShape(entry.Objects, runID, vesselID); err != nil {
				return err
			}
		} else {
			vesselEntryID = entry.EntryID
		}
	}
	if formulaEntryID == "" || vesselEntryID == "" {
		return errInvalidWorkflow("archive preview did not split formula and vessel entries")
	}

	confirmed, err := api.call("POST", "/v1/archive/confirm",
		map[string]any{"object_ids": []string{formulaID, vesselID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(confirmed, 200, "confirm archive"); err != nil {
		return err
	}
	var archived archiveResultView
	if err := confirmed.decode(&archived); err != nil {
		return err
	}
	if len(archived.Entries) != 2 {
		return errInvalidWorkflow("archive confirm did not store two entries")
	}
	for _, gone := range []struct{ kind, id string }{
		{"formulas", formulaID}, {"vessels", vesselID}, {"runs", runID},
	} {
		missing, err := api.call("GET", "/v1/"+gone.kind+"/"+gone.id, nil, nil)
		if err != nil {
			return err
		}
		if err := requireStatus(missing, 404, "archived object left the workspace"); err != nil {
			return err
		}
	}

	repeat, err := api.call("POST", "/v1/archive/confirm",
		map[string]any{"object_ids": []string{formulaID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(repeat, 200, "repeat archive is idempotent"); err != nil {
		return err
	}
	var repeated archiveResultView
	if err := repeat.decode(&repeated); err != nil {
		return err
	}
	if len(repeated.Entries) != 1 || repeated.Entries[0].ID != formulaEntryID {
		return errInvalidWorkflow("repeat archive did not return the same entry")
	}

	entryResponse, err := api.call("GET", "/v1/archive/entries/"+formulaEntryID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(entryResponse, 200, "read archive entry"); err != nil {
		return err
	}
	var stored domain.ArchiveEntry
	if err := entryResponse.decode(&stored); err != nil {
		return err
	}
	storedRun, ok := stored.Runs[runID]
	if !ok || len(storedRun.Observations) != 3 || storedRun.Metrics == nil {
		return errInvalidWorkflow("archive entry lost run observations or metrics")
	}

	blockedPreview, err := api.call("POST", "/v1/archive/restore/preview",
		map[string]any{"entry_ids": []string{formulaEntryID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(blockedPreview, 200, "preview partial restore"); err != nil {
		return err
	}
	var blocked restorePlanView
	if err := blockedPreview.decode(&blocked); err != nil {
		return err
	}
	if blocked.Ready || !hasRestoreConflict(blocked, string(domain.CodeArchiveReference)) {
		return errInvalidWorkflow("restore preview did not flag the missing vessel reference")
	}
	blockedConfirm, err := api.call("POST", "/v1/archive/restore/confirm",
		map[string]any{"entry_ids": []string{formulaEntryID}}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(blockedConfirm, 409, string(domain.CodeArchiveReference),
		"confirm restore with dangling reference"); err != nil {
		return err
	}

	restored, err := api.call("POST", "/v1/archive/restore/confirm",
		map[string]any{"entry_ids": []string{formulaEntryID, vesselEntryID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restored, 200, "confirm restore"); err != nil {
		return err
	}
	var restoredResult restoreResultView
	if err := restored.decode(&restoredResult); err != nil {
		return err
	}
	if len(restoredResult.Formulas) != 1 || len(restoredResult.Vessels) != 1 ||
		len(restoredResult.Runs) != 1 ||
		len(restoredResult.Runs[0].Observations) != 3 {
		return errInvalidWorkflow("restore did not bring back every archived object")
	}
	for _, back := range []struct{ kind, id string }{
		{"formulas", formulaID}, {"vessels", vesselID}, {"runs", runID},
	} {
		alive, err := api.call("GET", "/v1/"+back.kind+"/"+back.id, nil, nil)
		if err != nil {
			return err
		}
		if err := requireStatus(alive, 200, "restored object is live"); err != nil {
			return err
		}
	}

	entriesAfter, err := api.call("GET", "/v1/archive/entries", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(entriesAfter, 200, "list archive entries"); err != nil {
		return err
	}
	var remaining archiveEntriesView
	if err := entriesAfter.decode(&remaining); err != nil {
		return err
	}
	if len(remaining.Entries) != 0 {
		return errInvalidWorkflow("restored entries were not removed from the archive")
	}

	repeatedRestore, err := api.call("POST", "/v1/archive/restore/confirm",
		map[string]any{"entry_ids": []string{formulaEntryID}}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(repeatedRestore, 404, string(domain.CodeArchiveEntryMissing),
		"repeat restore is deterministically missing"); err != nil {
		return err
	}

	rearchived, err := api.call("POST", "/v1/archive/confirm",
		map[string]any{"object_ids": []string{formulaID, vesselID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(rearchived, 200, "archive again after restore"); err != nil {
		return err
	}
	var rearchivedResult archiveResultView
	if err := rearchived.decode(&rearchivedResult); err != nil {
		return err
	}
	if !sameEntryIDs(rearchivedResult.Entries, formulaEntryID, vesselEntryID) {
		return errInvalidWorkflow("re-archiving did not reproduce the same entry identifiers")
	}

	conflicting, err := api.call("POST", "/v1/formulas",
		createFormulaPayload("Archive Candidate"), nil)
	if err != nil {
		return err
	}
	if err := requireStatus(conflicting, 201, "create conflicting formula"); err != nil {
		return err
	}
	namePreview, err := api.call("POST", "/v1/archive/restore/preview",
		map[string]any{"entry_ids": []string{formulaEntryID}}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(namePreview, 200, "preview restore with name conflict"); err != nil {
		return err
	}
	var namePlan restorePlanView
	if err := namePreview.decode(&namePlan); err != nil {
		return err
	}
	if namePlan.Ready || !hasRestoreConflict(namePlan, string(domain.CodeFormulaNameConflict)) {
		return errInvalidWorkflow("restore preview did not flag the formula name conflict")
	}
	nameConfirm, err := api.call("POST", "/v1/archive/restore/confirm",
		map[string]any{"entry_ids": []string{formulaEntryID}}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(nameConfirm, 409, string(domain.CodeFormulaNameConflict),
		"confirm restore with name conflict"); err != nil {
		return err
	}
	return nil
}

// ensureRetiredHistory completes one full run lifecycle and retires the
// formula and vessel so all three become eligible for archiving.
func ensureRetiredHistory(api *client) (string, string, string, error) {
	formula, err := ensureApprovedFormula(api, "Archive Candidate")
	if err != nil {
		return "", "", "", err
	}
	vessel, err := createCheckVessel(api, "VSL-909", 260)
	if err != nil {
		return "", "", "", err
	}
	started, err := createStartedRun(api, formula.ID, vessel.ID, "RUN-ARCHIVE")
	if err != nil {
		return "", "", "", err
	}

	base := time.Now().UTC().Add(10 * time.Second)
	version := started.Run.Version
	for sequence := 1; sequence <= 3; sequence++ {
		response, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      sequence,
				"gravity":       1.050 - float64(sequence)*0.01,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, sequence),
			}, ptrInt64(version))
		if err != nil {
			return "", "", "", err
		}
		if err := requireStatus(response, 200, "append archive observation"); err != nil {
			return "", "", "", err
		}
		var observed runView
		if err := response.decode(&observed); err != nil {
			return "", "", "", err
		}
		version = observed.Version
	}

	conditioning, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/conditioning",
		nil, ptrInt64(version))
	if err != nil {
		return "", "", "", err
	}
	if err := requireStatus(conditioning, 200, "condition archive run"); err != nil {
		return "", "", "", err
	}
	var conditioningRun runView
	if err := conditioning.decode(&conditioningRun); err != nil {
		return "", "", "", err
	}

	completed, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(conditioningRun.Version))
	if err != nil {
		return "", "", "", err
	}
	if err := requireStatus(completed, 200, "complete archive run"); err != nil {
		return "", "", "", err
	}
	var pair pairView
	if err := completed.decode(&pair); err != nil {
		return "", "", "", err
	}

	cleaned, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/finish-cleaning", nil, nil)
	if err != nil {
		return "", "", "", err
	}
	if err := requireStatus(cleaned, 200, "finish cleaning archive vessel"); err != nil {
		return "", "", "", err
	}

	retireFormula, err := api.call("POST", "/v1/formulas/"+formula.ID+"/retire", nil, nil)
	if err != nil {
		return "", "", "", err
	}
	if err := requireStatus(retireFormula, 200, "retire archive formula"); err != nil {
		return "", "", "", err
	}
	retireVessel, err := api.call("POST", "/v1/vessels/"+vessel.ID+"/retire", nil, nil)
	if err != nil {
		return "", "", "", err
	}
	if err := requireStatus(retireVessel, 200, "retire archive vessel"); err != nil {
		return "", "", "", err
	}
	return formula.ID, vessel.ID, pair.Run.ID, nil
}

func requireArchivedRunShape(objects []archiveObjectView, runID, vesselID string) error {
	var runObject *archiveObjectView
	for index := range objects {
		if objects[index].Kind == "run" {
			runObject = &objects[index]
		}
	}
	if runObject == nil || runObject.ID != runID || runObject.ObservationCount != 3 {
		return errInvalidWorkflow("archive preview did not list the run with its observations")
	}
	dispositions := map[string]string{}
	for _, reference := range runObject.References {
		dispositions[reference.Kind] = reference.Disposition
		if reference.Kind == "vessel" && reference.ID != vesselID {
			return errInvalidWorkflow("archive preview listed an unexpected vessel reference")
		}
	}
	if dispositions["formula"] != "included" || dispositions["vessel"] != "separate_entry" {
		return fmt.Errorf("archive preview listed wrong reference dispositions: %v", dispositions)
	}
	return nil
}

func sameEntryIDs(entries []domain.ArchiveEntry, expected ...string) bool {
	if len(entries) != len(expected) {
		return false
	}
	remaining := map[string]int{}
	for _, id := range expected {
		remaining[id]++
	}
	for _, entry := range entries {
		remaining[entry.ID]--
	}
	for _, count := range remaining {
		if count != 0 {
			return false
		}
	}
	return true
}

func hasRestoreConflict(plan restorePlanView, code string) bool {
	for _, item := range plan.Items {
		for _, conflict := range item.Conflicts {
			if conflict.Code == code {
				return true
			}
		}
	}
	return false
}
