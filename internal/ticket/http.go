package ticket

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the ticket service's HTTP endpoints onto a chi router.
type API struct{ Repo *Repo }

// Routes returns a chi router pre-configured with the ticket endpoints.
// Mount it under whichever prefix the gateway uses (e.g. /v1/tickets).
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/", a.list)
	r.Post("/", a.create)
	r.Get("/{id}", a.get)
	r.Patch("/{id}/state", a.patchState)
	r.Get("/{id}/messages", a.listMessages)
	r.Post("/{id}/messages", a.appendMessage)
	return r
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	var body struct {
		ConversationID uuid.UUID `json:"conversation_id"`
		Priority       int16     `json:"priority"`
		RequiredSkills []string  `json:"required_skills"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.ConversationID == uuid.Nil {
		writeErr(w, http.StatusBadRequest, "conversation_id required")
		return
	}

	t, err := a.Repo.CreateTicket(r.Context(), CreateTicketParams{
		TenantID:       id.TenantID,
		ConversationID: body.ConversationID,
		Priority:       body.Priority,
		RequiredSkills: body.RequiredSkills,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "ticket: create", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "create failed")
		return
	}
	writeJSON(w, http.StatusCreated, t)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	t, err := a.Repo.GetTicket(r.Context(), id.TenantID, tid)
	if errors.Is(err, ErrNotFound) {
		writeErr(w, http.StatusNotFound, "not found")
		return
	}
	if err != nil {
		slog.ErrorContext(r.Context(), "ticket: get", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	// Phase 0 stub: a fuller list/search lands with the routing engine.
	writeJSON(w, http.StatusOK, map[string]any{"tickets": []Ticket{}})
}

func (a *API) patchState(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		To State `json:"to"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	t, err := a.Repo.ChangeState(r.Context(), id.TenantID, tid, body.To)
	switch {
	case errors.Is(err, ErrNotFound):
		writeErr(w, http.StatusNotFound, "not found")
	case err != nil:
		// Both validation ("invalid state") and transition errors are
		// caller-facing 4xx, so we surface the message. Internal pgx
		// errors don't quote "ticket:" so they fall through to 500.
		if isClientError(err) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		slog.ErrorContext(r.Context(), "ticket: state change", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "state change failed")
	default:
		writeJSON(w, http.StatusOK, t)
	}
}

func (a *API) appendMessage(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}

	var body struct {
		Direction   Direction   `json:"direction"`
		Body        string      `json:"body"`
		Attachments []uuid.UUID `json:"attachments"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if body.Direction == "" {
		body.Direction = DirectionOut
	}
	if body.Direction == DirectionIn {
		writeErr(w, http.StatusBadRequest, "inbound messages must come via a connector")
		return
	}

	agentID := id.AgentID
	m, err := a.Repo.AppendMessage(r.Context(), AppendMessageParams{
		TenantID:    id.TenantID,
		TicketID:    tid,
		Direction:   body.Direction,
		AgentID:     &agentID,
		Body:        body.Body,
		Attachments: body.Attachments,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "ticket: append message", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "send failed")
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

func (a *API) listMessages(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	tid, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	msgs, err := a.Repo.ListMessages(r.Context(), id.TenantID, tid, 50)
	if err != nil {
		slog.ErrorContext(r.Context(), "ticket: list messages", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": msgs})
}

// isClientError flags errors raised by Validate / Transition (which
// always carry the "ticket:" prefix) as caller-facing.
func isClientError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return len(msg) >= 8 && msg[:8] == "ticket: "
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
