package customer

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/samsonthomas951/contact-centre/internal/auth"
)

// API mounts the customer read endpoints. Mount under /v1/customers.
type API struct{ Repo *Repo }

// Routes returns the chi router. Any authed role can read -- the
// agent UI's sidebar needs it on every ticket page.
func (a *API) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{id}", a.get)
	// Wrapper for the agent UI: turns a ticket id into the customer
	// profile so the sidebar can fetch in one round-trip without
	// needing the ticket model to carry customer_id.
	r.Get("/by-ticket/{ticket_id}", a.getByTicket)
	return r
}

func (a *API) getByTicket(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	ticketID, err := uuid.Parse(chi.URLParam(r, "ticket_id"))
	if err != nil {
		http.Error(w, "invalid ticket_id", http.StatusBadRequest)
		return
	}
	customerID, err := a.Repo.CustomerIDForTicket(r.Context(), id.TenantID, ticketID)
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "ticket not found", http.StatusNotFound)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "customer: by-ticket", slog.String("err", err.Error()))
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	p, err := a.Repo.GetWithHistory(r.Context(), id.TenantID, customerID)
	if err != nil {
		slog.ErrorContext(r.Context(), "customer: get after by-ticket",
			slog.String("err", err.Error()))
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p)
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := auth.FromContext(r.Context())
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	customerID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	p, err := a.Repo.GetWithHistory(r.Context(), id.TenantID, customerID)
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
		return
	case err != nil:
		slog.ErrorContext(r.Context(), "customer: get", slog.String("err", err.Error()))
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p)
}
