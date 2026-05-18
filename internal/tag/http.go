package tag

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the tag CRUD endpoints (mount at /v1/tags) plus the
// per-ticket attach/detach routes (mount at /v1/tickets/{id}/tags
// via the gateway router so the route lives next to the ticket
// itself).
type API struct{ Repo *Repo }

// Routes returns the /v1/tags chi tree.
//
//	GET    /            list tags
//	POST   /            create tag    (admin)
//	DELETE /{id}        delete tag    (admin)
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", a.list)
	r.Group(func(r chi.Router) {
		r.Use(auth.RequireRole(auth.RoleAdmin))
		r.Post("/", a.create)
		r.Delete("/{id}", a.delete)
	})
	return r
}

// TicketRoutes returns the per-ticket sub-tree to be mounted under
// /v1/tickets/{id}/tags. Any authed agent can attach/detach.
//
//	GET    /            list tags on this ticket
//	POST   /            attach   body: {"tag_id":"…"}
//	DELETE /{tag_id}    detach
func (a *API) TicketRoutes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", a.listForTicket)
	r.Post("/", a.attach)
	r.Delete("/{tag_id}", a.detach)
	return r
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tags, err := a.Repo.List(r.Context(), id.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "tag: list", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Slug  string  `json:"slug"`
		Name  string  `json:"name"`
		Color *string `json:"color,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	t, err := a.Repo.Create(r.Context(), CreateParams{
		TenantID: id.TenantID, Slug: body.Slug, Name: body.Name, Color: body.Color,
	})
	switch {
	case errors.Is(err, ErrInvalid):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "tag: create", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "create failed")
	default:
		writeJSON(w, http.StatusCreated, t)
	}
}

func (a *API) delete(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tagID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	switch err := a.Repo.Delete(r.Context(), id.TenantID, tagID); {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case err != nil:
		writeErr(w, http.StatusInternalServerError, "delete failed")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) listForTicket(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	tags, err := a.Repo.ForTicket(r.Context(), id.TenantID, tid)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tags": tags})
}

func (a *API) attach(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	var body struct {
		TagID uuid.UUID `json:"tag_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.TagID == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "tag_id required")
		return
	}
	switch err := a.Repo.Attach(r.Context(), id.TenantID, tid, body.TagID); {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "ticket or tag not found")
	case err != nil:
		slog.ErrorContext(r.Context(), "tag: attach", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "attach failed")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (a *API) detach(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid ticket id")
		return
	}
	tagID, err := uuid.Parse(chi.URLParam(r, "tag_id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid tag id")
		return
	}
	if err := a.Repo.Detach(r.Context(), id.TenantID, tid, tagID); err != nil {
		writeErr(w, http.StatusInternalServerError, "detach failed")
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
