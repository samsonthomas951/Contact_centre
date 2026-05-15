package csat

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

// PublicAPI is the surface the customer browser hits via the survey
// link. No auth -- the token in the URL path is the access control.
// Mount under /csat (no /v1 prefix); reachable from the public
// internet behind Traefik + the rate limiter.
type PublicAPI struct{ Repo *Repo }

// Routes returns the chi router. Mount with the rate-limit middleware
// already applied -- the public collector is high-leverage for an
// attacker (each rejected attempt is cheap, but a flood would create
// noise in the analytics rollups).
func (a *PublicAPI) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/{token}", a.preview)
	r.Post("/{token}", a.submit)
	return r
}

// preview returns the survey metadata the JS client renders before
// the score selector.
func (a *PublicAPI) preview(w http.ResponseWriter, r *http.Request) {
	s, err := a.Repo.FindByToken(r.Context(), chi.URLParam(r, "token"))
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrExpired):
		http.Error(w, "link expired", http.StatusGone)
	case errors.Is(err, ErrAlreadyResponded):
		// Don't 4xx -- show the score the customer already gave so
		// the page doesn't look broken. Score in the JSON; UI renders
		// "thanks, you rated us X".
		writeJSON(w, http.StatusOK, s)
	case err != nil:
		slog.WarnContext(r.Context(), "csat: preview", slog.String("err", err.Error()))
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	default:
		writeJSON(w, http.StatusOK, s)
	}
}

// submit accepts {score:1..5, comment?:string}. Comment is capped at
// 4 KB. Body bigger than that is rejected via http.MaxBytesReader.
func (a *PublicAPI) submit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var body struct {
		Score   int16  `json:"score"`
		Comment string `json:"comment,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.Comment != "" {
		body.Comment = strings.TrimSpace(body.Comment)
		if len(body.Comment) > 4000 {
			body.Comment = body.Comment[:4000]
		}
	}
	s, err := a.Repo.Submit(r.Context(), SubmitParams{
		Token: chi.URLParam(r, "token"), Score: body.Score, Comment: body.Comment,
	})
	switch {
	case errors.Is(err, ErrNotFound):
		http.Error(w, "not found", http.StatusNotFound)
	case errors.Is(err, ErrExpired):
		http.Error(w, "link expired", http.StatusGone)
	case errors.Is(err, ErrAlreadyResponded):
		// Idempotent: returning 200 lets the customer's browser back-
		// button without surfacing a "you already voted" error.
		writeJSON(w, http.StatusOK, s)
	case err != nil && strings.HasPrefix(err.Error(), "csat:"):
		http.Error(w, err.Error(), http.StatusBadRequest)
	case err != nil:
		slog.ErrorContext(r.Context(), "csat: submit", slog.String("err", err.Error()))
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
	default:
		writeJSON(w, http.StatusOK, s)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
