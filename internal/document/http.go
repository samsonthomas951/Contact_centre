package document

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the document service's HTTP endpoints.
type API struct{ Svc *Service }

// Routes returns a chi router pre-configured with the doc endpoints.
// Mount under /v1/documents in the gateway.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/", a.upload)
	r.Get("/{id}/download", a.download)
	return r
}

func (a *API) upload(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	// Multipart upload: one "file" part required, optional "ticket_id"
	// form field for linkage. 25 MiB cap matches MaxUploadBytes.
	if err := r.ParseMultipartForm(MaxUploadBytes); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid multipart form")
		return
	}
	file, fh, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "file part missing")
		return
	}
	defer file.Close()

	var ticketIDPtr *uuid.UUID
	if t := r.FormValue("ticket_id"); t != "" {
		tid, err := uuid.Parse(t)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "invalid ticket_id")
			return
		}
		ticketIDPtr = &tid
	}

	agentID := id.AgentID
	d, err := a.Svc.Upload(r.Context(), UploadParams{
		TenantID:         id.TenantID,
		TicketID:         ticketIDPtr,
		UploaderAgentID:  &agentID,
		Filename:         fh.Filename,
		ContentTypeClaim: fh.Header.Get("Content-Type"),
		Body:             file,
	})
	switch {
	case errors.Is(err, ErrTooLarge):
		writeErr(w, http.StatusRequestEntityTooLarge, err.Error())
	case errors.Is(err, ErrContentTypeNotAllowed),
		errors.Is(err, ErrContentTypeMismatch):
		writeErr(w, http.StatusUnsupportedMediaType, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "document: upload", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "upload failed")
	default:
		writeJSON(w, http.StatusCreated, d)
	}
}

func (a *API) download(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	docID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	url, err := a.Svc.PresignDownload(r.Context(), id.TenantID, docID, id.AgentID)
	if err != nil {
		// Don't leak whether the doc exists — same body for not-found and
		// not-clean. Operator triages via audit + metric.
		slog.WarnContext(r.Context(), "document: presign refused",
			slog.String("err", err.Error()),
			slog.String("doc_id", docID.String()))
		writeErr(w, http.StatusNotFound, "not available")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"url": url})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
