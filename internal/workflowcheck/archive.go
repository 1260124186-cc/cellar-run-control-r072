package workflowcheck

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/domain"
)

type planView struct {
	Mode       string `json:"mode"`
	Executable bool   `json:"executable"`
	Outcome    string `json:"outcome"`
	Token      string `json:"token"`
	GroupID    string `json:"group_id"`
	Items      []struct {
		Kind             string `json:"kind"`
		ID               string `json:"id"`
		Code             string `json:"code"`
		Name             string `json:"name"`
		State            string `json:"state"`
		Version          int64  `json:"version"`
		Location         string `json:"location"`
		ObservationCount int    `json:"observation_count"`
	} `json:"items"`
	Blockers []struct {
		Code string `json:"code"`
		Kind string `json:"kind"`
		ID   string `json:"id"`
	} `json:"blockers"`
}

type archiveListingView struct {
	Revision   int `json:"revision"`
	EventCount int `json:"event_count"`
	Entries    []struct {
		Kind             string `json:"kind"`
		ID               string `json:"id"`
		ObservationCount int    `json:"observation_count"`
	} `json:"entries"`
	Groups []struct {
		ID        string   `json:"id"`
		EntryKeys []string `json:"entry_keys"`
	} `json:"groups"`
}

func archiveRestore(h checkHarness) error {
	api := h.api

	// --- Setup: an approved control formula that must never move -----------
	control, err := ensureApprovedFormula(api, "Control Keeper")
	if err != nil {
		return err
	}

	// Retired standalone formula.
	standalone, err := ensureApprovedFormula(api, "Standalone Stout")
	if err != nil {
		return err
	}
	if err := retireFormula(api, standalone.ID, 3); err != nil {
		return err
	}

	// Retired vessel with no run history.
	bareVessel, err := createCheckVessel(api, "VSL-ARC-A", 500)
	if err != nil {
		return err
	}
	if err := retireVessel(api, bareVessel.ID, bareVessel.Version); err != nil {
		return err
	}

	// Formula + vessel with a fully completed run and three observations.
	batchFormula, err := ensureApprovedFormula(api, "Batch Reserve Ale")
	if err != nil {
		return err
	}
	batchVessel, err := createCheckVessel(api, "VSL-ARC-B", 900)
	if err != nil {
		return err
	}
	started, err := createStartedRun(api, batchFormula.ID, batchVessel.ID, "RUN-ARC-001")
	if err != nil {
		return err
	}
	base := time.Now().UTC().Add(10 * time.Second)
	points := []float64{1.060, 1.030, 1.012}
	runVersion := started.Run.Version
	for index, gravity := range points {
		response, callErr := api.call("POST", "/v1/runs/"+started.Run.ID+"/observations",
			map[string]any{
				"sequence":      index + 1,
				"gravity":       gravity,
				"temperature_c": 20.0,
				"observed_at":   verifiedAt(base, index+1),
			}, ptrInt64(runVersion))
		if callErr != nil {
			return callErr
		}
		if err := requireStatus(response, 200, "append archive setup observation"); err != nil {
			return err
		}
		if err := response.decode(&started.Run); err != nil {
			return err
		}
		runVersion = started.Run.Version
	}
	response, err := api.call("POST", "/v1/runs/"+started.Run.ID+"/conditioning",
		nil, ptrInt64(runVersion))
	if err != nil {
		return err
	}
	if err := requireStatus(response, 200, "condition archive setup run"); err != nil {
		return err
	}
	if err := response.decode(&started.Run); err != nil {
		return err
	}
	response, err = api.call("POST", "/v1/runs/"+started.Run.ID+"/complete",
		nil, ptrInt64(started.Run.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(response, 200, "complete archive setup run"); err != nil {
		return err
	}
	var completed pairView
	if err := response.decode(&completed); err != nil {
		return err
	}
	// Finish cleaning so the vessel can retire, then retire both parents.
	response, err = api.call("POST", "/v1/vessels/"+batchVessel.ID+"/finish-cleaning",
		nil, ptrInt64(completed.Vessel.Version))
	if err != nil {
		return err
	}
	if err := requireStatus(response, 200, "finish archive setup cleaning"); err != nil {
		return err
	}
	if err := retireVessel(api, batchVessel.ID, completed.Vessel.Version+1); err != nil {
		return err
	}
	if err := retireFormula(api, batchFormula.ID, 3); err != nil {
		return err
	}

	// --- A non-retired formula must be rejected ----------------------------
	badPreview, err := api.call("POST", "/v1/archive/preview", map[string]any{
		"formula_ids": []string{control.ID},
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(badPreview, 200, "preview non-retired formula"); err != nil {
		return err
	}
	var badPlan planView
	if err := badPreview.decode(&badPlan); err != nil {
		return err
	}
	if badPlan.Executable || !planHasBlocker(badPlan, string(domain.CodeArchiveNotRetired)) {
		return errInvalidWorkflow("archive preview must flag a non-retired formula")
	}
	badConfirm, err := api.call("POST", "/v1/archive/confirm", map[string]any{
		"selection": map[string]any{"formula_ids": []string{control.ID}},
		"token":     badPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(badConfirm, 409, string(domain.CodeArchiveBlocked),
		"confirm a blocked archive"); err != nil {
		return err
	}
	if err := requireFormulaLive(api, control.ID, domain.FormulaApproved); err != nil {
		return err
	}

	// --- Preview the full closure and assert its contents ------------------
	previewResponse, err := api.call("POST", "/v1/archive/preview", map[string]any{
		"formula_ids": []string{standalone.ID, batchFormula.ID},
		"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(previewResponse, 200, "archive preview"); err != nil {
		return err
	}
	var plan planView
	if err := previewResponse.decode(&plan); err != nil {
		return err
	}
	if !plan.Executable || plan.Outcome != "ready" {
		return errInvalidWorkflow("archive preview should be executable and ready")
	}
	if !planContains(plan, "run", started.Run.ID) {
		return errInvalidWorkflow("preview did not include the closure run")
	}
	if runItem := findPlanItem(plan, "run", started.Run.ID); runItem == nil ||
		runItem.ObservationCount != 3 {
		return errInvalidWorkflow("preview did not show all three observations travelling")
	}
	if len(plan.Items) != 5 {
		return fmt.Errorf("expected 5 items in closure (2 formulas, 2 vessels, 1 run), got %d",
			len(plan.Items))
	}

	// --- History write failure must leave every online object in place -----
	if err := breakArchiveFile(h.archiveDir); err != nil {
		return err
	}
	failedConfirm, err := api.call("POST", "/v1/archive/confirm", map[string]any{
		"selection": map[string]any{
			"formula_ids": []string{standalone.ID, batchFormula.ID},
			"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
		},
		"token": plan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(failedConfirm, 500, string(domain.CodeStorage),
		"archive with a broken history file"); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(h.archiveDir, "archive.json")); err != nil {
		return fmt.Errorf("remove archive fault: %w", err)
	}
	if err := requireFormulaLive(api, standalone.ID, domain.FormulaRetired); err != nil {
		return err
	}
	if err := requireFormulaLive(api, batchFormula.ID, domain.FormulaRetired); err != nil {
		return err
	}
	if err := requireVesselLive(api, bareVessel.ID, domain.VesselRetired); err != nil {
		return err
	}
	if err := requireVesselLive(api, batchVessel.ID, domain.VesselRetired); err != nil {
		return err
	}
	if err := requireRunLive(api, started.Run.ID, domain.RunCompleted); err != nil {
		return err
	}

	// --- Confirm for real and verify the workspace emptied -----------------
	confirmResponse, err := api.call("POST", "/v1/archive/confirm", map[string]any{
		"selection": map[string]any{
			"formula_ids": []string{standalone.ID, batchFormula.ID},
			"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
		},
		"token": plan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(confirmResponse, 200, "confirm archive"); err != nil {
		return err
	}
	var archivedPlan planView
	if err := confirmResponse.decode(&archivedPlan); err != nil {
		return err
	}
	if archivedPlan.Outcome != "already_archived" || archivedPlan.GroupID == "" {
		return errInvalidWorkflow("post-archive preview should report an archive group")
	}
	groupID := archivedPlan.GroupID
	for _, ref := range []struct{ kind, id string }{
		{"formula", standalone.ID},
		{"formula", batchFormula.ID},
		{"vessel", bareVessel.ID},
		{"vessel", batchVessel.ID},
		{"run", started.Run.ID},
	} {
		if err := requireArchived(api, ref.kind, ref.id); err != nil {
			return err
		}
	}
	listingResponse, err := api.call("GET", "/v1/archive", nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(listingResponse, 200, "list archive"); err != nil {
		return err
	}
	var listing archiveListingView
	if err := listingResponse.decode(&listing); err != nil {
		return err
	}
	if len(listing.Entries) != 5 || len(listing.Groups) != 1 {
		return fmt.Errorf("archive ledger expected 5 entries and 1 group, got %d/%d",
			len(listing.Entries), len(listing.Groups))
	}
	if err := requireFormulaLive(api, control.ID, domain.FormulaApproved); err != nil {
		return err
	}
	// Its name index is still usable: creating a name twin of the archived
	// formula is allowed and later proves the restore conflict check.
	nameTwin, err := createDraftNamed(api, "Standalone Stout", "Twin Style")
	if err != nil {
		return err
	}
	// --- Repeating the same archive is a deterministic no-op ---------------
	repeatPreview, err := api.call("POST", "/v1/archive/preview", map[string]any{
		"formula_ids": []string{standalone.ID, batchFormula.ID},
		"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
	}, nil)
	if err != nil {
		return err
	}
	var repeatPlan planView
	if err := repeatPreview.decode(&repeatPlan); err != nil {
		return err
	}
	if !repeatPlan.Executable || repeatPlan.Outcome != "already_archived" {
		return errInvalidWorkflow("repeat archive preview must be already_archived")
	}
	repeatConfirm, err := api.call("POST", "/v1/archive/confirm", map[string]any{
		"selection": map[string]any{
			"formula_ids": []string{standalone.ID, batchFormula.ID},
			"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
		},
		"token": repeatPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(repeatConfirm, 200, "repeat archive confirm"); err != nil {
		return err
	}
	var repeatListing archiveListingView
	again, err := api.call("GET", "/v1/archive", nil, nil)
	if err != nil {
		return err
	}
	if err := again.decode(&repeatListing); err != nil {
		return err
	}
	if len(repeatListing.Entries) != 5 || len(repeatListing.Groups) != 1 {
		return errInvalidWorkflow("idempotent archive changed entries or groups")
	}
	if repeatListing.EventCount != listing.EventCount {
		return errInvalidWorkflow("a true no-op archive must not record a new event")
	}
	if repeatListing.Revision != listing.Revision {
		return errInvalidWorkflow("a true no-op archive must not advance the archive revision")
	}

	// --- Restore the whole group atomically, observations included ---------
	restorePreview, err := api.call("POST", "/v1/archive/restore/preview", map[string]any{
		"group_id": groupID,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restorePreview, 200, "restore preview"); err != nil {
		return err
	}
	var restorePlan planView
	if err := restorePreview.decode(&restorePlan); err != nil {
		return err
	}
	if restorePlan.Executable {
		return errInvalidWorkflow("restore must conflict with the live name twin")
	}
	if !planHasBlocker(restorePlan, string(domain.CodeFormulaNameConflict)) {
		return errInvalidWorkflow("restore preview must report the name conflict")
	}

	// Retiring does not free a normalized name, so clearing the conflict means
	// retiring the twin and moving it to history as well.
	if err := purgeLiveFormula(api, nameTwin.ID); err != nil {
		return err
	}
	twinPreview, err := api.call("POST", "/v1/archive/preview", map[string]any{
		"formula_ids": []string{nameTwin.ID},
	}, nil)
	if err != nil {
		return err
	}
	var twinPlan planView
	if err := twinPreview.decode(&twinPlan); err != nil {
		return err
	}
	twinConfirm, err := api.call("POST", "/v1/archive/confirm", map[string]any{
		"selection": map[string]any{"formula_ids": []string{nameTwin.ID}},
		"token":     twinPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(twinConfirm, 200, "archive name twin to free the name"); err != nil {
		return err
	}
	staleConfirm, err := api.call("POST", "/v1/archive/restore/confirm", map[string]any{
		"selection": map[string]any{"group_id": groupID},
		"token":     restorePlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(staleConfirm, 412, string(domain.CodePlanTokenInvalid),
		"restore with a stale preview token"); err != nil {
		return err
	}

	freshPreview, err := api.call("POST", "/v1/archive/restore/preview", map[string]any{
		"group_id": groupID,
	}, nil)
	if err != nil {
		return err
	}
	var freshPlan planView
	if err := freshPreview.decode(&freshPlan); err != nil {
		return err
	}
	if !freshPlan.Executable || freshPlan.Outcome != "ready" {
		return errInvalidWorkflow("fresh restore preview should be executable")
	}
	restoreConfirm, err := api.call("POST", "/v1/archive/restore/confirm", map[string]any{
		"selection": map[string]any{"group_id": groupID},
		"token":     freshPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(restoreConfirm, 200, "confirm restore"); err != nil {
		return err
	}
	var restoredPlan planView
	if err := restoreConfirm.decode(&restoredPlan); err != nil {
		return err
	}
	if restoredPlan.Outcome != "already_restored" {
		return errInvalidWorkflow("post-restore preview should report already_restored")
	}
	if err := requireFormulaLive(api, batchFormula.ID, domain.FormulaRetired); err != nil {
		return err
	}
	if err := requireVesselLive(api, batchVessel.ID, domain.VesselRetired); err != nil {
		return err
	}
	runRead, err := api.call("GET", "/v1/runs/"+started.Run.ID, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(runRead, 200, "read restored run"); err != nil {
		return err
	}
	var restoredRun runView
	if err := runRead.decode(&restoredRun); err != nil {
		return err
	}
	if len(restoredRun.Observations) != 3 || restoredRun.Metrics == nil {
		return errInvalidWorkflow("restored run lost observations or metrics")
	}
	if restoredRun.Metrics.ObservationCount != 3 {
		return errInvalidWorkflow("restored metrics lost the observation count")
	}

	// --- A failed live write during restore cannot return half a group ------
	// Re-archive the whole bundle (closure recaptures the run); identical
	// history copies already exist, yet moving live objects requires a fresh
	// durable endorsement event. No new entries or groups are produced.
	beforeRearchive, err := api.call("GET", "/v1/archive", nil, nil)
	if err != nil {
		return err
	}
	var beforeListing archiveListingView
	if err := beforeRearchive.decode(&beforeListing); err != nil {
		return err
	}
	rearchive, err := api.call("POST", "/v1/archive/preview", map[string]any{
		"formula_ids": []string{standalone.ID, batchFormula.ID},
		"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
	}, nil)
	if err != nil {
		return err
	}
	var rearchivePlan planView
	if err := rearchive.decode(&rearchivePlan); err != nil {
		return err
	}
	if !planContains(rearchivePlan, "run", started.Run.ID) {
		return errInvalidWorkflow("re-archive closure must recapture the restored run")
	}
	reconfirm, err := api.call("POST", "/v1/archive/confirm", map[string]any{
		"selection": map[string]any{
			"formula_ids": []string{standalone.ID, batchFormula.ID},
			"vessel_ids":  []string{bareVessel.ID, batchVessel.ID},
		},
		"token": rearchivePlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(reconfirm, 200, "re-archive after restore"); err != nil {
		return err
	}
	afterRearchive, err := api.call("GET", "/v1/archive", nil, nil)
	if err != nil {
		return err
	}
	var afterListing archiveListingView
	if err := afterRearchive.decode(&afterListing); err != nil {
		return err
	}
	if len(afterListing.Entries) != len(beforeListing.Entries) ||
		len(afterListing.Groups) != len(beforeListing.Groups) {
		return errInvalidWorkflow("re-archiving identical copies must not add entries or groups")
	}
	if afterListing.EventCount != beforeListing.EventCount+1 {
		return errInvalidWorkflow("moving objects out again needs a new endorsement event")
	}
	if afterListing.Revision != beforeListing.Revision+1 {
		return errInvalidWorkflow("the fresh endorsement must advance the archive revision")
	}

	failingPreview, err := api.call("POST", "/v1/archive/restore/preview", map[string]any{
		"group_id": groupID,
	}, nil)
	if err != nil {
		return err
	}
	var failingPlan planView
	if err := failingPreview.decode(&failingPlan); err != nil {
		return err
	}
	if err := breakLiveStateDirectory(h.dataDir); err != nil {
		return err
	}
	failingConfirm, err := api.call("POST", "/v1/archive/restore/confirm", map[string]any{
		"selection": map[string]any{"group_id": groupID},
		"token":     failingPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireCode(failingConfirm, 500, string(domain.CodeStorage),
		"restore with a broken live snapshot target"); err != nil {
		return err
	}
	if err := repairLiveStateDirectory(h.dataDir); err != nil {
		return err
	}
	// Every object must still be absent: no half-restored group.
	if err := requireArchived(api, "formula", standalone.ID); err != nil {
		return err
	}
	if err := requireArchived(api, "formula", batchFormula.ID); err != nil {
		return err
	}
	if err := requireArchived(api, "vessel", bareVessel.ID); err != nil {
		return err
	}
	if err := requireArchived(api, "vessel", batchVessel.ID); err != nil {
		return err
	}
	if err := requireArchived(api, "run", started.Run.ID); err != nil {
		return err
	}

	// The same token succeeds on retry once the target is healthy, proving the
	// failure rolled back completely and the outcome is deterministic.
	retryConfirm, err := api.call("POST", "/v1/archive/restore/confirm", map[string]any{
		"selection": map[string]any{"group_id": groupID},
		"token":     failingPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(retryConfirm, 200, "retry restore after repair"); err != nil {
		return err
	}
	if err := requireFormulaLive(api, standalone.ID, domain.FormulaRetired); err != nil {
		return err
	}
	if err := requireRunLive(api, started.Run.ID, domain.RunCompleted); err != nil {
		return err
	}

	// Restoring the group again is a deterministic no-op.
	secondRestorePreview, err := api.call("POST", "/v1/archive/restore/preview", map[string]any{
		"group_id": groupID,
	}, nil)
	if err != nil {
		return err
	}
	var secondPlan planView
	if err := secondRestorePreview.decode(&secondPlan); err != nil {
		return err
	}
	if secondPlan.Outcome != "already_restored" || !secondPlan.Executable {
		return errInvalidWorkflow("repeat restore must be already_restored")
	}
	secondConfirm, err := api.call("POST", "/v1/archive/restore/confirm", map[string]any{
		"selection": map[string]any{"group_id": groupID},
		"token":     secondPlan.Token,
	}, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(secondConfirm, 200, "repeat restore confirm"); err != nil {
		return err
	}

	return nil
}

func retireFormula(api *client, id string, expectedVersion int64) error {
	response, err := api.call("POST", "/v1/formulas/"+id+"/retire", nil, ptrInt64(expectedVersion))
	if err != nil {
		return err
	}
	return requireStatus(response, 200, "retire formula")
}

func retireVessel(api *client, id string, expectedVersion int64) error {
	response, err := api.call("POST", "/v1/vessels/"+id+"/retire", nil, ptrInt64(expectedVersion))
	if err != nil {
		return err
	}
	return requireStatus(response, 200, "retire vessel")
}

func createDraftNamed(api *client, name, style string) (formulaView, error) {
	response, err := api.call("POST", "/v1/formulas", map[string]any{
		"name":                    name,
		"style":                   style,
		"target_original_gravity": 1.04,
		"target_final_gravity":    1.01,
		"max_fermentation_temp_c": 20.0,
		"minimum_days":            5,
		"maximum_days":            10,
	}, nil)
	if err != nil {
		return formulaView{}, err
	}
	if err := requireStatus(response, 201, "create name twin"); err != nil {
		return formulaView{}, err
	}
	var view formulaView
	return view, response.decode(&view)
}

// purgeLiveFormula removes the draft twin by retiring it out of band is not
// possible (drafts cannot retire), so the check instead moves it through steps,
// approval is unnecessary: the only public way to free a name is full
// retirement. The twin therefore gets steps, approval, and retirement.
func purgeLiveFormula(api *client, id string) error {
	steps, err := api.call("PUT", "/v1/formulas/"+id+"/steps", formulaStepPayload(), ptrInt64(1))
	if err != nil {
		return err
	}
	if err := requireStatus(steps, 200, "set twin steps"); err != nil {
		return err
	}
	approved, err := api.call("POST", "/v1/formulas/"+id+"/approve", nil, ptrInt64(2))
	if err != nil {
		return err
	}
	if err := requireStatus(approved, 200, "approve twin"); err != nil {
		return err
	}
	return retireFormula(api, id, 3)
}

func planContains(plan planView, kind, id string) bool {
	return findPlanItem(plan, kind, id) != nil
}

func findPlanItem(plan planView, kind, id string) *struct {
	Kind             string `json:"kind"`
	ID               string `json:"id"`
	Code             string `json:"code"`
	Name             string `json:"name"`
	State            string `json:"state"`
	Version          int64  `json:"version"`
	Location         string `json:"location"`
	ObservationCount int    `json:"observation_count"`
} {
	for index := range plan.Items {
		if plan.Items[index].Kind == kind && plan.Items[index].ID == id {
			return &plan.Items[index]
		}
	}
	return nil
}

func planHasBlocker(plan planView, code string) bool {
	for _, blocker := range plan.Blockers {
		if blocker.Code == code {
			return true
		}
	}
	return false
}

func requireFormulaLive(api *client, id string, state domain.FormulaState) error {
	response, err := api.call("GET", "/v1/formulas/"+id, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(response, 200, "formula remains live"); err != nil {
		return err
	}
	var view formulaView
	if err := response.decode(&view); err != nil {
		return err
	}
	if view.State != state {
		return fmt.Errorf("formula %s state = %q, want %q", id, view.State, state)
	}
	return nil
}

func requireVesselLive(api *client, id string, state domain.VesselState) error {
	response, err := api.call("GET", "/v1/vessels/"+id, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(response, 200, "vessel remains live"); err != nil {
		return err
	}
	var view vesselView
	if err := response.decode(&view); err != nil {
		return err
	}
	if view.State != state {
		return fmt.Errorf("vessel %s state = %q, want %q", id, view.State, state)
	}
	return nil
}

func requireRunLive(api *client, id string, state domain.RunState) error {
	response, err := api.call("GET", "/v1/runs/"+id, nil, nil)
	if err != nil {
		return err
	}
	if err := requireStatus(response, 200, "run remains live"); err != nil {
		return err
	}
	var view runView
	if err := response.decode(&view); err != nil {
		return err
	}
	if view.State != state {
		return fmt.Errorf("run %s state = %q, want %q", id, view.State, state)
	}
	return nil
}

func requireArchived(api *client, kind, id string) error {
	response, err := api.call("GET", "/v1/"+liveCollection(kind)+"/"+id, nil, nil)
	if err != nil {
		return err
	}
	if response.status != 404 {
		return fmt.Errorf("%s %s should have left the live workspace, got status %d",
			kind, id, response.status)
	}
	listing, err := api.call("GET", "/v1/archive", nil, nil)
	if err != nil {
		return err
	}
	var view archiveListingView
	if err := listing.decode(&view); err != nil {
		return err
	}
	for _, entry := range view.Entries {
		if entry.Kind == kind && entry.ID == id {
			return nil
		}
	}
	return fmt.Errorf("%s %s was not found in the archive ledger", kind, id)
}

func liveCollection(kind string) string {
	switch kind {
	case "formula":
		return "formulas"
	case "vessel":
		return "vessels"
	default:
		return "runs"
	}
}

// breakArchiveFile makes the archive snapshot path a directory, so the atomic
// temp-file rename fails with EISDIR. The history written earlier (none at the
// point this fault is injected) is never deleted.
func breakArchiveFile(archiveDir string) error {
	return os.MkdirAll(filepath.Join(archiveDir, "archive.json"), 0o755)
}

// breakLiveStateDirectory turns the existing state.json file into a directory,
// so the live snapshot atomic replace fails mid-restore with EISDIR.
func breakLiveStateDirectory(dataDir string) error {
	target := filepath.Join(dataDir, "state.json")
	if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.MkdirAll(target, 0o755)
}

func repairLiveStateDirectory(dataDir string) error {
	return os.RemoveAll(filepath.Join(dataDir, "state.json"))
}
