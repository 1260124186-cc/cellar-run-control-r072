package httpapi

import (
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type archiveSelectionRequest struct {
	FormulaIDs []string `json:"formula_ids"`
	VesselIDs  []string `json:"vessel_ids"`
}

type restoreSelectionRequest struct {
	GroupID    string   `json:"group_id"`
	FormulaIDs []string `json:"formula_ids"`
	VesselIDs  []string `json:"vessel_ids"`
	RunIDs     []string `json:"run_ids"`
}

type archiveConfirmRequest struct {
	Selection archiveSelectionRequest `json:"selection"`
	Token     string                  `json:"token"`
}

type restoreConfirmRequest struct {
	Selection restoreSelectionRequest `json:"selection"`
	Token     string                  `json:"token"`
}

func registerArchiveRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("POST /v1/archive/preview", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[archiveSelectionRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		plan, err := app.PreviewArchive(service.ArchiveSelection{
			FormulaIDs: body.FormulaIDs,
			VesselIDs:  body.VesselIDs,
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, plan)
	})

	mux.HandleFunc("POST /v1/archive/confirm", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[archiveConfirmRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		plan, err := app.ConfirmArchive(service.ArchiveSelection{
			FormulaIDs: body.Selection.FormulaIDs,
			VesselIDs:  body.Selection.VesselIDs,
		}, body.Token)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, plan)
	})

	mux.HandleFunc("GET /v1/archive", func(response http.ResponseWriter, _ *http.Request) {
		listing, err := app.ListArchive()
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, listing)
	})

	mux.HandleFunc("POST /v1/archive/restore/preview", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[restoreSelectionRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		plan, err := app.PreviewRestore(service.RestoreSelection{
			GroupID:    body.GroupID,
			FormulaIDs: body.FormulaIDs,
			VesselIDs:  body.VesselIDs,
			RunIDs:     body.RunIDs,
		})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, plan)
	})

	mux.HandleFunc("POST /v1/archive/restore/confirm", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[restoreConfirmRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		plan, err := app.ConfirmRestore(service.RestoreSelection{
			GroupID:    body.Selection.GroupID,
			FormulaIDs: body.Selection.FormulaIDs,
			VesselIDs:  body.Selection.VesselIDs,
			RunIDs:     body.Selection.RunIDs,
		}, body.Token)
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, plan)
	})
}
