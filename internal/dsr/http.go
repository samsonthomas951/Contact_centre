package dsr

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the DSR endpoints. Mount under /v1/dsr.
type API struct{ Repo *Repo }

// Routes returns a chi router. Open + read are gated to dpo/auditor/
// admin; the close (Resolve) endpoint requires dpo/admin so an auditor
// can't materially change state.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleDPO, auth.RoleAuditor, auth.RoleAdmin))

	r.Get("/", a.list)
	r.Post("/", a.open)
	r.Get("/{id}", a.get)
	r.Post("/{id}/assign", a.assign)
	r.Post("/{id}/resolve", a.resolve)
	r.Post("/{id}/actions", a.action)
	return r
}

func (a *API) open(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		CustomerID         *uuid.UUID `json:"customer_id,omitempty"`
		SubjectEmail       string     `json:"subject_email,omitempty"`
		SubjectPhone       string     `json:"subject_phone,omitempty"`
		SubjectExternalRef string     `json:"subject_external_ref,omitempty"`
		Kind               Kind       `json:"kind"`
		Reason             string     `json:"reason,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req, err := a.Repo.OpenRequest(r.Context(), OpenParams{
		TenantID: id.TenantID, CustomerID: body.CustomerID,
		SubjectEmail: body.SubjectEmail, SubjectPhone: body.SubjectPhone,
		SubjectExternalRef: body.SubjectExternalRef,
		Kind:               body.Kind, Reason: body.Reason,
	})
	switch {
	case errors.Is(err, ErrSubjectRequired):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil && strings.HasPrefix(err.Error(), "dsr:"):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "dsr: open", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "open failed")
	default:
		writeJSON(w, http.StatusCreated, req)
	}
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	state := State(r.URL.Query().Get("state"))
	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n <= 500 {
			limit = n
		}
	}
	rows, err := a.Repo.List(r.Context(), id.TenantID, state, limit)
	if err != nil {
		slog.ErrorContext(r.Context(), "dsr: list", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requests": rows})
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	req, err := a.Repo.Get(r.Context(), id.TenantID, rid)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, req)
}

func (a *API) assign(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct{ AgentID uuid.UUID `json:"agent_id"` }
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.AgentID == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "agent_id required")
		return
	}
	req, err := a.Repo.Assign(r.Context(), id.TenantID, rid, body.AgentID)
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "assign failed")
	default:
		writeJSON(w, http.StatusOK, req)
	}
}

func (a *API) resolve(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	// Only DPO / admin may close.
	if !id.HasRole(auth.RoleDPO, auth.RoleAdmin) {
		writeErr(w, http.StatusForbidden, "forbidden")
		return
	}
	rid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		To             State  `json:"to"`
		ResolutionNote string `json:"resolution_note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.To == StateRejected && body.ResolutionNote == "" {
		writeErr(w, http.StatusBadRequest, "resolution_note required when rejecting")
		return
	}
	req, err := a.Repo.Resolve(r.Context(), ResolveParams{
		TenantID: id.TenantID, ID: rid, To: body.To,
		ResolutionNote: body.ResolutionNote,
	})
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case err != nil && strings.HasPrefix(err.Error(), "dsr:"):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "dsr: resolve", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "resolve failed")
	default:
		writeJSON(w, http.StatusOK, req)
	}
}

func (a *API) action(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Kind string `json:"kind"`
		Note string `json:"note,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Kind == "" {
		writeErr(w, http.StatusBadRequest, "kind required")
		return
	}
	agentID := id.AgentID
	if err := a.Repo.LogAction(r.Context(), rid, &agentID, body.Kind, body.Note); err != nil {
		writeErr(w, http.StatusInternalServerError, "action log failed")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
