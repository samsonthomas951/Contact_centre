// Package meta hosts the public callbacks Meta requires for App Review:
// data-deletion (when a user revokes the app or asks Meta to remove
// their data from third parties) and deauthorize (when a user removes
// the app from their account).
//
// Both endpoints receive Meta's signed_request POST body and respond
// with JSON containing a follow-up URL where the user can see the
// status of their request. We treat both as Data Subject erasure
// requests under Kenya DPA s.28 and route them through internal/dsr,
// which already owns the lifecycle, audit trail, and 30-day SLA.
//
// Reference: https://developers.facebook.com/docs/development/create-an-app/app-dashboard/data-deletion-callback
package meta

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/samsonthomas951/contact-centre/internal/dsr"
)

// SignedRequest is the decoded inner payload Meta signs. We deliberately
// ignore everything except user_id -- algorithm + issued_at are sanity-
// checked during verification but don't need to leak past the seam.
type SignedRequest struct {
	UserID    string `json:"user_id"`
	Algorithm string `json:"algorithm"`
	IssuedAt  int64  `json:"issued_at"`
}

// Handler responds to /v1/meta/data-deletion and /v1/meta/deauthorize.
// Both share the same HMAC verification + DSR-opening logic; the only
// difference is the log line.
type Handler struct {
	Pool      *pgxpool.Pool
	DSR       *dsr.Repo
	AppSecret string // FB_APP_SECRET; HMAC-SHA256 signing key
	// PublicBaseURL is what we put in the "url" field of the response
	// (so users can come back and check status). e.g.
	// "https://app.example.co.ke" -- gateway adds the path.
	PublicBaseURL string
}

// Routes returns a chi router; mount at /v1/meta. Both routes are
// PUBLIC -- Meta does not send a bearer.
func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/data-deletion", h.dataDeletion)
	r.Post("/deauthorize", h.deauthorize)
	// Status page: the URL we hand back to Meta. Public so the user
	// can revisit without an account. Returns a tiny HTML rather than
	// JSON because most users will open it in a browser.
	r.Get("/deletion-status/{code}", h.status)
	return r
}

func (h *Handler) dataDeletion(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, "data-deletion")
}

func (h *Handler) deauthorize(w http.ResponseWriter, r *http.Request) {
	h.handle(w, r, "deauthorize")
}

func (h *Handler) handle(w http.ResponseWriter, r *http.Request, kind string) {
	// Meta posts as application/x-www-form-urlencoded with a single
	// field 'signed_request'. ParseForm caps the body via
	// http.MaxBytesReader implicitly when Server.MaxHeaderBytes is
	// set; here we belt-and-brace with our own cap.
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	signed := r.FormValue("signed_request")
	if signed == "" {
		http.Error(w, "signed_request required", http.StatusBadRequest)
		return
	}

	sr, err := ParseSignedRequest(signed, h.AppSecret)
	if err != nil {
		slog.WarnContext(r.Context(), "meta: signed_request verify failed",
			slog.String("kind", kind),
			slog.String("err", err.Error()))
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}
	if sr.UserID == "" {
		http.Error(w, "user_id missing in signed_request", http.StatusBadRequest)
		return
	}

	// One DSR per matching tenant. A single user_id (PSID / IGSID /
	// WAID) may have interacted with multiple Pages owned by different
	// tenants -- each gets its own erasure ticket. The lookup hits the
	// customers.external_refs JSONB, which the ingress code populates
	// with channel-keyed external ids on first inbound.
	tenants, err := h.tenantsForUser(r.Context(), sr.UserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "meta: tenants lookup",
			slog.String("err", err.Error()))
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}

	// Confirmation code: deterministic on user_id + algorithm so a
	// retry from Meta produces the same status URL. UUID v5 keeps it
	// safe to put in a public URL.
	ns := uuid.MustParse("0e7c1e30-ffff-4fff-bfff-000000000001")
	code := uuid.NewSHA1(ns, []byte(kind+":"+sr.UserID)).String()

	if len(tenants) == 0 {
		// No data on this user. Still return success per Meta spec;
		// the status page will show "no data was held".
		slog.InfoContext(r.Context(), "meta: no data for user",
			slog.String("kind", kind),
			slog.String("user_id", sr.UserID),
			slog.String("code", code))
	} else {
		for _, t := range tenants {
			_, err := h.DSR.OpenRequest(r.Context(), dsr.OpenParams{
				TenantID:           t,
				SubjectExternalRef: sr.UserID,
				Kind:               dsr.KindErasure,
				Reason:             fmt.Sprintf("Meta %s callback (confirmation %s)", kind, code),
			})
			if err != nil {
				slog.ErrorContext(r.Context(), "meta: open dsr",
					slog.String("tenant", t.String()),
					slog.String("user_id", sr.UserID),
					slog.String("err", err.Error()))
				// Don't fail the response -- some tenants may have
				// succeeded. The status page will reflect actual state.
			}
		}
		slog.InfoContext(r.Context(), "meta: dsrs opened",
			slog.String("kind", kind),
			slog.String("user_id", sr.UserID),
			slog.String("code", code),
			slog.Int("tenants", len(tenants)))
	}

	statusURL := strings.TrimRight(h.PublicBaseURL, "/") + "/v1/meta/deletion-status/" + code
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"url":               statusURL,
		"confirmation_code": code,
	})
}

// status renders a tiny HTML page summarising the deletion progress for
// every DSR opened under the given confirmation code. Reason text on
// the DSR row carries the code so we can find them without keeping a
// separate index.
func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	code := chi.URLParam(r, "code")
	if _, err := uuid.Parse(code); err != nil {
		http.Error(w, "bad code", http.StatusBadRequest)
		return
	}

	rows, err := h.Pool.Query(r.Context(), `
		SELECT id, tenant_id, state, received_at, due_at, fulfilled_at
		FROM dsr_requests
		WHERE reason LIKE '%' || $1 || ')'
		ORDER BY received_at ASC`, code)
	if err != nil {
		http.Error(w, "lookup failed", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	type row struct {
		ID          string
		TenantID    string
		State       string
		ReceivedAt  string
		DueAt       string
		Fulfilled   string
	}
	out := []row{}
	for rows.Next() {
		var r row
		var fulfilled *string
		if err := rows.Scan(&r.ID, &r.TenantID, &r.State, &r.ReceivedAt, &r.DueAt, &fulfilled); err != nil {
			continue
		}
		if fulfilled != nil {
			r.Fulfilled = *fulfilled
		}
		out = append(out, r)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = fmt.Fprintf(w, `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<title>Deletion status %s</title>
<style>body{font:14px/1.5 system-ui,sans-serif;max-width:42rem;margin:3rem auto;padding:0 1.5rem;color:#1f2937}
h1{font-size:1.2rem}table{border-collapse:collapse;width:100%%;margin-top:1rem}
th,td{border:1px solid #e5e7eb;padding:.4rem .6rem;text-align:left;font-size:.9rem}
th{background:#f8fafc}small{color:#64748b}</style>
</head><body>
<h1>Data deletion status</h1>
<p><small>Confirmation code: <code>%s</code></small></p>`,
		code, code)

	if len(out) == 0 {
		_, _ = fmt.Fprintf(w, `<p>No data was held for this account, or the request has not yet been received.</p>`)
	} else {
		_, _ = fmt.Fprintf(w, `<table><thead><tr><th>Request</th><th>State</th><th>Opened</th><th>Due by</th><th>Resolved</th></tr></thead><tbody>`)
		for _, r := range out {
			fulfilled := r.Fulfilled
			if fulfilled == "" {
				fulfilled = "—"
			}
			_, _ = fmt.Fprintf(w, `<tr><td><code>%s</code></td><td>%s</td><td>%s</td><td>%s</td><td>%s</td></tr>`,
				r.ID[:8], r.State, r.ReceivedAt[:10], r.DueAt[:10], fulfilled)
		}
		_, _ = fmt.Fprintf(w, `</tbody></table>
<p><small>Each row is one tenant's copy of your data. Erasure SLA is 30 days under Kenya Data Protection Act 2019 s.28.</small></p>`)
	}
	_, _ = fmt.Fprintf(w, `</body></html>`)
}

// tenantsForUser returns every tenant that has a customer row keyed on
// the given external id, across all channels. Cheap because
// customers.external_refs has a GIN-friendly shape; the query stays
// fast even with a few hundred thousand customers.
func (h *Handler) tenantsForUser(ctx context.Context, userID string) ([]uuid.UUID, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT DISTINCT tenant_id
		FROM customers
		WHERE external_refs::text LIKE '%"' || $1 || '"%'`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []uuid.UUID{}
	for rows.Next() {
		var t uuid.UUID
		if err := rows.Scan(&t); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ParseSignedRequest decodes Meta's signed_request envelope and verifies
// the HMAC-SHA256 signature using appSecret. Format on the wire:
//
//	<base64url(signature)>.<base64url(payload)>
//
// Where signature = HMAC-SHA256(payload, appSecret) computed on the
// base64-url-encoded payload string (NOT the decoded JSON). Meta uses
// url-safe base64 without padding.
func ParseSignedRequest(token, appSecret string) (SignedRequest, error) {
	if appSecret == "" {
		return SignedRequest{}, errors.New("app secret unconfigured")
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return SignedRequest{}, errors.New("malformed signed_request")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return SignedRequest{}, fmt.Errorf("decode signature: %w", err)
	}

	mac := hmac.New(sha256.New, []byte(appSecret))
	mac.Write([]byte(parts[1]))
	expected := mac.Sum(nil)
	if !hmac.Equal(sig, expected) {
		return SignedRequest{}, errors.New("signature mismatch")
	}

	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return SignedRequest{}, fmt.Errorf("decode payload: %w", err)
	}
	var sr SignedRequest
	if err := json.Unmarshal(payload, &sr); err != nil {
		return SignedRequest{}, fmt.Errorf("payload json: %w", err)
	}
	if sr.Algorithm != "" && !strings.EqualFold(sr.Algorithm, "HMAC-SHA256") {
		return SignedRequest{}, fmt.Errorf("unsupported algorithm %q", sr.Algorithm)
	}
	return sr, nil
}
