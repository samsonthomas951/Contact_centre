package onboarding

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the onboarding endpoints. Mount under /v1/onboarding.
type API struct {
	Tenants *TenantRepo
	Agents  *AgentRepo
}

// Routes returns a chi router behind admin RBAC.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleAdmin))

	r.Get("/tenant", a.getMyTenant)
	r.Patch("/tenant/retention", a.updateRetention)
	r.Post("/agents", a.invite)
	r.Delete("/agents/{id}", a.deactivate)
	return r
}

func (a *API) getMyTenant(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	t, err := a.Tenants.Get(r.Context(), id.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "onboarding: get tenant", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) updateRetention(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		MessagesDays  *int `json:"messages_days,omitempty"`
		DocumentsDays *int `json:"documents_days,omitempty"`
		AuditDays     *int `json:"audit_days,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	// Sanity bands so a fat-finger can't set retention to one day.
	for _, n := range []*int{body.MessagesDays, body.DocumentsDays, body.AuditDays} {
		if n != nil && (*n < 7 || *n > 3650) {
			writeErr(w, http.StatusBadRequest, "retention days must be between 7 and 3650")
			return
		}
	}
	t, err := a.Tenants.UpdateRetention(r.Context(), UpdateRetentionParams{
		TenantID:      id.TenantID,
		MessagesDays:  body.MessagesDays,
		DocumentsDays: body.DocumentsDays,
		AuditDays:     body.AuditDays,
	})
	if err != nil {
		slog.ErrorContext(r.Context(), "onboarding: update retention", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "update failed")
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *API) invite(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		AgentID       uuid.UUID `json:"agent_id"`
		Email         string    `json:"email"`
		DisplayName   string    `json:"display_name"`
		Role          string    `json:"role"`
		MaxConcurrent int16     `json:"max_concurrent"`
		Skills        []string  `json:"skills,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	agent, err := a.Agents.Invite(r.Context(), InviteParams{
		TenantID:      id.TenantID,
		AgentID:       body.AgentID,
		Email:         body.Email,
		DisplayName:   body.DisplayName,
		Role:          body.Role,
		MaxConcurrent: body.MaxConcurrent,
		Skills:        body.Skills,
	})
	switch {
	case errors.Is(err, ErrInvalidRole):
		writeErr(w, http.StatusBadRequest, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "onboarding: invite", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "invite failed")
	default:
		writeJSON(w, http.StatusCreated, agent)
	}
}

func (a *API) deactivate(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	agentID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.Agents.Deactivate(r.Context(), id.TenantID, agentID); err != nil {
		slog.WarnContext(r.Context(), "onboarding: deactivate",
			slog.String("err", err.Error()),
			slog.String("agent_id", agentID.String()))
		writeErr(w, http.StatusNotFound, "not found")
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
