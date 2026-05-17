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
	Widgets *WidgetRepo
}

// Routes returns a chi router behind admin RBAC.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleAdmin))

	r.Get("/tenant", a.getMyTenant)
	r.Patch("/tenant/retention", a.updateRetention)
	r.Post("/agents", a.invite)
	r.Delete("/agents/{id}", a.deactivate)
	if a.Widgets != nil {
		r.Get("/widgets", a.listWidgets)
		r.Post("/widgets", a.registerWidget)
		r.Patch("/widgets/{id}", a.updateWidget)
		r.Delete("/widgets/{id}", a.deleteWidget)
	}
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

func (a *API) listWidgets(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := a.Widgets.ListWidgets(r.Context(), id.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "onboarding: list widgets", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"widgets": rows})
}

func (a *API) registerWidget(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	var body struct {
		Origin         string `json:"origin"`
		DisplayName    string `json:"display_name"`
		WelcomeMessage string `json:"welcome_message,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	site, err := a.Widgets.Register(r.Context(), RegisterParams{
		TenantID:       id.TenantID,
		Origin:         body.Origin,
		DisplayName:    body.DisplayName,
		WelcomeMessage: body.WelcomeMessage,
	})
	switch {
	case errors.Is(err, ErrInvalidOrigin):
		writeErr(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrOriginExists):
		writeErr(w, http.StatusConflict, err.Error())
	case err != nil:
		slog.ErrorContext(r.Context(), "onboarding: register widget", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "register failed")
	default:
		writeJSON(w, http.StatusCreated, site)
	}
}

func (a *API) updateWidget(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	widgetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	var body struct {
		DisplayName    *string `json:"display_name,omitempty"`
		WelcomeMessage *string `json:"welcome_message,omitempty"`
		Active         *bool   `json:"active,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	site, err := a.Widgets.Update(r.Context(), UpdateParams{
		TenantID: id.TenantID, ID: widgetID,
		DisplayName: body.DisplayName, WelcomeMessage: body.WelcomeMessage,
		Active: body.Active,
	})
	if err != nil {
		slog.WarnContext(r.Context(), "onboarding: update widget", slog.String("err", err.Error()))
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, site)
}

func (a *API) deleteWidget(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	widgetID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	if err := a.Widgets.Delete(r.Context(), id.TenantID, widgetID); err != nil {
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
