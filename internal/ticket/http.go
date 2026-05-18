package ticket

import (
	"encoding/json"
	"errors"
	"fmt"
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
	r.Post("/bulk", a.bulk)
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
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}

	q := r.URL.Query()
	p := ListParams{
		TenantID:       id.TenantID,
		AssignedFilter: q.Get("assigned"), // "", "mine", "unassigned", "all"
		Query:          q.Get("q"),
		Channels:       q["channel"],      // ?channel=fb&channel=email
		TagSlugs:       q["tag"],          // ?tag=billing&tag=urgent
	}
	// Always carry the caller's agent id so AssignedFilter=mine can use
	// it without an extra round-trip from the handler.
	agent := id.AgentID
	p.AssignedAgentID = &agent

	// Back-compat shim: `?mine=1` still works as a synonym for
	// `?assigned=mine`, and a missing `assigned` flag defaults to
	// "mine" for agent role so the inbox stays scoped.
	if p.AssignedFilter == "" {
		if q.Get("mine") == "1" || !id.HasRole(auth.RoleSupervisor, auth.RoleAdmin) {
			p.AssignedFilter = "mine"
		}
	}

	// `?state=open&state=pending` filters; defaults to "open work".
	if vs, ok := q["state"]; ok {
		states := make([]State, 0, len(vs))
		for _, s := range vs {
			st := State(s)
			if err := st.Validate(); err == nil {
				states = append(states, st)
			}
		}
		p.States = states
	}

	if l := q.Get("limit"); l != "" {
		var n int
		_, _ = fmt.Sscanf(l, "%d", &n)
		p.Limit = n
	}

	tickets, err := a.Repo.List(r.Context(), p)
	if err != nil {
		slog.ErrorContext(r.Context(), "ticket: list", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": tickets})
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

// bulk applies one operation across many tickets in a single request.
//
//	body: { "op": "resolve"|"close"|"reopen"|"assign"|"unassign",
//	        "ticket_ids": ["..."], "agent_id": "..." }
//
// State transitions go through ChangeState per-ticket so the same
// transition validation + realtime publishes fire as the single-
// ticket path. Assign/unassign use a single UPDATE for efficiency
// since they don't need transition validation.
//
// Returns:
//
//	{ "updated": N, "failures": [{"id":"...","reason":"..."}] }
//
// Partial success is the default: invalid transitions are collected,
// not aborted, so the agent sees what worked vs what didn't.
func (a *API) bulk(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Op        string      `json:"op"`
		TicketIDs []uuid.UUID `json:"ticket_ids"`
		AgentID   *uuid.UUID  `json:"agent_id,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if len(body.TicketIDs) == 0 {
		writeErr(w, http.StatusBadRequest, "ticket_ids required")
		return
	}
	// Cap the batch so a single request can't trigger thousands of
	// state changes. 100 is comfortably above the per-page inbox
	// limit (currently 50) and the supervisor view (200).
	if len(body.TicketIDs) > 200 {
		writeErr(w, http.StatusBadRequest, "batch too large (max 200)")
		return
	}

	switch body.Op {
	case "resolve", "close", "reopen":
		var to State
		switch body.Op {
		case "resolve":
			to = StateResolved
		case "close":
			to = StateClosed
		case "reopen":
			to = StateReopened
		}
		ok, fails, err := a.Repo.BulkState(r.Context(), id.TenantID, body.TicketIDs, to)
		if err != nil {
			slog.ErrorContext(r.Context(), "ticket: bulk state", slog.String("err", err.Error()))
			writeErr(w, http.StatusInternalServerError, "bulk failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": ok, "failures": fails})

	case "assign":
		// Reassigning others' work is a supervisor/admin act; agents
		// can self-assign — but only over tickets that are
		// unassigned or already theirs. Without that scope a plain
		// agent could lift the entire queue off a teammate by
		// self-assigning a list of their ids.
		if body.AgentID == nil || *body.AgentID == uuid.Nil {
			writeErr(w, http.StatusBadRequest, "agent_id required for assign")
			return
		}
		privileged := id.HasRole(auth.RoleSupervisor, auth.RoleAdmin)
		if *body.AgentID != id.AgentID && !privileged {
			writeErr(w, http.StatusForbidden,
				"only supervisor/admin can assign to other agents")
			return
		}
		// callerLimit is nil for privileged roles (no scope), or the
		// caller's own id for plain agents — the repo translates
		// that into "WHERE assigned_agent_id IS NULL OR = caller".
		var callerLimit *uuid.UUID
		if !privileged {
			c := id.AgentID
			callerLimit = &c
		}
		updated, err := a.Repo.BulkAssign(r.Context(), id.TenantID, body.AgentID, body.TicketIDs, callerLimit)
		if err != nil {
			slog.ErrorContext(r.Context(), "ticket: bulk assign", slog.String("err", err.Error()))
			writeErr(w, http.StatusInternalServerError, "bulk failed")
			return
		}
		// Surface skipped count so the agent sees partial success
		// when some ids fell outside their callerLimit.
		skipped := len(body.TicketIDs) - updated
		writeJSON(w, http.StatusOK, map[string]any{
			"updated": updated, "skipped": skipped, "failures": []BulkFailure{},
		})

	case "unassign":
		if !id.HasRole(auth.RoleSupervisor, auth.RoleAdmin) {
			writeErr(w, http.StatusForbidden, "only supervisor/admin can unassign")
			return
		}
		updated, err := a.Repo.BulkAssign(r.Context(), id.TenantID, nil, body.TicketIDs, nil)
		if err != nil {
			slog.ErrorContext(r.Context(), "ticket: bulk unassign", slog.String("err", err.Error()))
			writeErr(w, http.StatusInternalServerError, "bulk failed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"updated": updated, "failures": []BulkFailure{}})

	default:
		writeErr(w, http.StatusBadRequest, "unknown op (resolve/close/reopen/assign/unassign)")
	}
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
