package supervisor

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the supervisor dashboard's read-only endpoints. Mount
// behind auth.RequireRole(supervisor, admin, auditor, dpo).
type API struct{ Repo *Repo }

// Routes returns a chi router; mount at /v1/supervisor.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	// Apply the role gate inside Routes so callers can't accidentally
	// mount a more permissive prefix and bypass it.
	r.Use(auth.RequireRole(auth.RoleSupervisor, auth.RoleAdmin, auth.RoleAuditor, auth.RoleDPO))

	r.Get("/queue", a.queueDepth)
	r.Get("/agents", a.agents)
	r.Get("/at-risk", a.atRisk)
	return r
}

func (a *API) queueDepth(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := a.Repo.QueueDepth(r.Context(), id.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "supervisor: queue depth", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": rows})
}

func (a *API) agents(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	rows, err := a.Repo.AgentPresence(r.Context(), id.TenantID)
	if err != nil {
		slog.ErrorContext(r.Context(), "supervisor: agents", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"agents": rows})
}

func (a *API) atRisk(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	within := 30 * time.Minute
	if w := r.URL.Query().Get("within_minutes"); w != "" {
		if n, err := strconv.Atoi(w); err == nil && n > 0 && n <= 24*60 {
			within = time.Duration(n) * time.Minute
		}
	}
	limit := 50
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	rows, err := a.Repo.AtRiskTickets(r.Context(), id.TenantID, within, limit)
	if err != nil {
		slog.ErrorContext(r.Context(), "supervisor: at risk", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tickets": rows, "within_minutes": int(within.Minutes())})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
