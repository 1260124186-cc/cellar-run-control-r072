package httpapi

import (
	"net/http"

	"github.com/1260124186-cc/solo-0016-cellar-run-control/internal/service"
)

type archiveRequest struct {
	ObjectIDs []string `json:"object_ids"`
}

type restoreRequest struct {
	EntryIDs []string `json:"entry_ids"`
}

func registerArchiveRoutes(mux *http.ServeMux, app *service.Service) {
	mux.HandleFunc("POST /v1/archive/preview", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[archiveRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		plan, err := app.PlanArchive(service.ArchiveInput{ObjectIDs: body.ObjectIDs})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, plan)
	})

	mux.HandleFunc("POST /v1/archive/confirm", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[archiveRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		result, err := app.ConfirmArchive(service.ArchiveInput{ObjectIDs: body.ObjectIDs})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	})

	mux.HandleFunc("GET /v1/archive/entries", func(response http.ResponseWriter, _ *http.Request) {
		entries, err := app.ListArchiveEntries()
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, map[string]any{"entries": entries})
	})

	mux.HandleFunc("GET /v1/archive/entries/{id}", func(response http.ResponseWriter, request *http.Request) {
		entry, err := app.GetArchiveEntry(request.PathValue("id"))
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, entry)
	})

	mux.HandleFunc("POST /v1/archive/restore/preview", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[restoreRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		plan, err := app.PlanRestore(service.RestoreInput{EntryIDs: body.EntryIDs})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, plan)
	})

	mux.HandleFunc("POST /v1/archive/restore/confirm", func(response http.ResponseWriter, request *http.Request) {
		body, err := decodeJSON[restoreRequest](response, request)
		if !handleDecode(response, err) {
			return
		}
		result, err := app.ConfirmRestore(service.RestoreInput{EntryIDs: body.EntryIDs})
		if err != nil {
			writeDomainError(response, err)
			return
		}
		writeJSON(response, http.StatusOK, result)
	})
}
