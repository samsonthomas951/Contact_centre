package analytics

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the analytics endpoints. Mount under /v1/analytics.
type API struct{ M *Materialiser }

// Routes returns a chi router behind the RBAC gate.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Use(auth.RequireRole(auth.RoleSupervisor, auth.RoleAdmin, auth.RoleAuditor, auth.RoleDPO))
	r.Get("/tickets-daily", a.ticketsDaily)
	r.Get("/agents-daily", a.agentsDaily)
	return r
}

func (a *API) ticketsDaily(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	from, to, perr := parseRange(r)
	if perr != "" {
		writeErr(w, http.StatusBadRequest, perr)
		return
	}
	rows, err := a.M.TicketsDaily(r.Context(), id.TenantID, from, to)
	if err != nil {
		slog.ErrorContext(r.Context(), "analytics: tickets daily", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "rows": rows})
}

func (a *API) agentsDaily(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		writeErr(w, http.StatusUnauthorized, "unauthorized")
		return
	}
	from, to, perr := parseRange(r)
	if perr != "" {
		writeErr(w, http.StatusBadRequest, perr)
		return
	}
	rows, err := a.M.AgentsDaily(r.Context(), id.TenantID, from, to)
	if err != nil {
		slog.ErrorContext(r.Context(), "analytics: agents daily", slog.String("err", err.Error()))
		writeErr(w, http.StatusInternalServerError, "lookup failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"from": from, "to": to, "rows": rows})
}

// parseRange reads from / to query params; defaults to the last 30
// days when both are absent. Range capped at 366 days so a casual
// query can't pull a multi-year sweep.
func parseRange(r *http.Request) (time.Time, time.Time, string) {
	const layout = "2006-01-02"
	q := r.URL.Query()
	now := time.Now().UTC().Truncate(24 * time.Hour)
	to := now.AddDate(0, 0, -1) // up to yesterday (today not materialised yet)
	from := to.AddDate(0, 0, -30)
	if s := q.Get("from"); s != "" {
		d, err := time.Parse(layout, s)
		if err != nil {
			return time.Time{}, time.Time{}, "invalid from (want YYYY-MM-DD)"
		}
		from = d
	}
	if s := q.Get("to"); s != "" {
		d, err := time.Parse(layout, s)
		if err != nil {
			return time.Time{}, time.Time{}, "invalid to (want YYYY-MM-DD)"
		}
		to = d
	}
	if to.Before(from) {
		return time.Time{}, time.Time{}, "to must be >= from"
	}
	if to.Sub(from) > 366*24*time.Hour {
		return time.Time{}, time.Time{}, "range > 366 days; narrow the window"
	}
	return from, to, ""
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
