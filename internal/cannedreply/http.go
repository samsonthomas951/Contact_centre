package cannedreply

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the CRUD endpoints. Mount under /v1/canned-replies.
type API struct{ Repo *Repo }

// Routes returns the chi router. Any authed role can list/create;
// edit/delete is owner-or-admin (enforced in the repo).
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", a.list)
	r.Post("/", a.create)
	r.Patch("/{id}", a.update)
	r.Delete("/{id}", a.delete)
	return r
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := a.Repo.ListVisible(r.Context(), id.TenantID, id.AgentID)
	if err != nil {
		slog.ErrorContext(r.Context(), "cannedreply: list", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"replies": rows})
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Shortcut string  `json:"shortcut"`
		Title    string  `json:"title"`
		Body     string  `json:"body"`
		Channel  *string `json:"channel,omitempty"`
		// scope = "personal" (default) or "tenant" (admin-only).
		Scope string `json:"scope,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	owner := &id.AgentID
	if body.Scope == "tenant" {
		if !id.HasRole(auth.RoleAdmin) {
			writeErr(w, http.StatusForbidden, "only admins can create tenant-shared replies")
			return
		}
		owner = nil
	}
	rep, err := a.Repo.Create(r.Context(), CreateParams{
		TenantID: id.TenantID, OwnerAgentID: owner,
		Shortcut: body.Shortcut, Title: body.Title, Body: body.Body, Channel: body.Channel,
	})
	switch {
	case errors.Is(err, ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "cannedreply: create", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "create failed")
	default:
		writeJSON(w, http.StatusCreated, rep)
	}
}

func (a *API) update(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	repID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		Title    *string `json:"title,omitempty"`
		Body     *string `json:"body,omitempty"`
		Shortcut *string `json:"shortcut,omitempty"`
		Channel  *string `json:"channel,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	rep, err := a.Repo.Update(r.Context(), UpdateParams{
		TenantID: id.TenantID, ID: repID,
		ActingAgent: id.AgentID, ActingAdmin: id.HasRole(auth.RoleAdmin),
		Title: body.Title, Body: body.Body, Shortcut: body.Shortcut, Channel: body.Channel,
	})
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		writeErr(w, http.StatusForbidden, err.Error())
	case errors.Is(err, ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "cannedreply: update", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "update failed")
	default:
		writeJSON(w, http.StatusOK, rep)
	}
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	repID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	err = a.Repo.Delete(r.Context(), id.TenantID, repID, id.AgentID, id.HasRole(auth.RoleAdmin))
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case errors.Is(err, ErrForbidden):
		writeErr(w, http.StatusForbidden, err.Error())
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "delete failed")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
