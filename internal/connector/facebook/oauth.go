package facebook

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OAuthConfig holds the Meta App credentials + the public redirect
// URI Meta calls back. AppSecret is also the value the webhook handler
// uses for X-Hub-Signature-256 verification, so the demo wires both
// from one env var.
type OAuthConfig struct {
	AppID       string `env:"FB_APP_ID"`
	AppSecret   string `env:"FB_APP_SECRET"`
	RedirectURI string `env:"FB_REDIRECT_URI"`
	// GraphHost + GraphVersion mirror the values on Sender so the
	// OAuth + outbound paths stay coupled to the same Meta API
	// generation.
	GraphHost    string `env:"FB_GRAPH_HOST"    default:"https://graph.facebook.com"`
	GraphVersion string `env:"FB_GRAPH_VERSION" default:"v22.0"`
}

// Scopes is the permission set the brand admin grants when connecting
// a Page. Per §4.1 of the technical plan: Page DMs (`pages_messaging`),
// the metadata to subscribe to webhooks (`pages_manage_metadata`),
// the Page list (`pages_show_list`), inbound user content
// (`pages_read_user_content`), engagement reads (`pages_read_engagement`).
var Scopes = []string{
	"pages_show_list",
	"pages_messaging",
	"pages_manage_metadata",
	"pages_read_user_content",
	"pages_read_engagement",
}

// OAuthHandler hosts /v1/connect/fb (start) and /v1/connect/fb/callback.
type OAuthHandler struct {
	Cfg          OAuthConfig
	Pool         *pgxpool.Pool
	Crypter      Crypter
	WebhookFields []string // defaults to the Phase-0 subscription fields
	HTTP         *http.Client
}

// Crypter is the seam for token encryption. Production wraps a Vault
// transit secret; the demo binary registers a static-key AES-GCM
// implementation (see DemoCrypter below) so brand admins can connect
// without a Vault deployment.
type Crypter interface {
	Encrypt(ctx context.Context, plaintext []byte) (ciphertext []byte, dekID string, err error)
	Decrypt(ctx context.Context, ciphertext []byte, dekID string) ([]byte, error)
}

// NewOAuthHandler constructs one with sane defaults.
func NewOAuthHandler(cfg OAuthConfig, pool *pgxpool.Pool, crypter Crypter) *OAuthHandler {
	return &OAuthHandler{
		Cfg:     cfg,
		Pool:    pool,
		Crypter: crypter,
		WebhookFields: []string{
			"messages", "messaging_postbacks", "message_reads",
			"message_deliveries", "feed", "mention",
		},
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost: 4,
				IdleConnTimeout:     60 * time.Second,
			},
		},
	}
}

// Routes returns the chi-compatible handlers. /v1/connect/fb starts
// the OAuth dance; /v1/connect/fb/callback is what Meta posts back to.
func (h *OAuthHandler) Start(w http.ResponseWriter, r *http.Request) {
	if h.Cfg.AppID == "" || h.Cfg.RedirectURI == "" {
		http.Error(w, "fb oauth not configured (FB_APP_ID + FB_REDIRECT_URI required)",
			http.StatusServiceUnavailable)
		return
	}
	// CSRF state. Embed the tenant ID so the callback knows which
	// tenant's fb_pages to write into. In production the tenant id
	// comes from the agent's session cookie + a server-side state
	// store; for the demo we trust the tenant header / cookie.
	tenantID := r.URL.Query().Get("tenant")
	if tenantID == "" {
		tenantID = r.Header.Get("X-Demo-Tenant")
	}
	if _, err := uuid.Parse(tenantID); err != nil {
		http.Error(w, "tenant id required", http.StatusBadRequest)
		return
	}
	state := newState(tenantID)

	q := url.Values{}
	q.Set("client_id", h.Cfg.AppID)
	q.Set("redirect_uri", h.Cfg.RedirectURI)
	q.Set("state", state)
	q.Set("scope", strings.Join(Scopes, ","))
	q.Set("response_type", "code")

	authURL := fmt.Sprintf("%s/%s/dialog/oauth?%s",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		// dialog/oauth lives under www.facebook.com, not graph.
		// We rewrite the host below; the version path stays.
		h.Cfg.GraphVersion, q.Encode())
	authURL = strings.Replace(authURL, "graph.facebook.com",
		"www.facebook.com", 1)

	http.Redirect(w, r, authURL, http.StatusFound)
}

// Callback handles Meta's redirect after the brand admin consents.
// Steps:
//  1. Parse `code` + `state`.
//  2. Exchange code -> short-lived user token.
//  3. Exchange short-lived -> long-lived user token (60 days).
//  4. GET /me/accounts -> list of Pages with their never-expiring
//     Page access tokens.
//  5. For each Page: subscribe to webhook fields, upsert into fb_pages.
func (h *OAuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	code := r.URL.Query().Get("code")
	state := r.URL.Query().Get("state")
	if code == "" || state == "" {
		http.Error(w, "missing code or state", http.StatusBadRequest)
		return
	}
	tenantID, err := tenantFromState(state)
	if err != nil {
		http.Error(w, "bad state", http.StatusBadRequest)
		return
	}

	ctx := r.Context()

	// Step 2: code -> short-lived user token.
	shortTok, err := h.exchangeCode(ctx, code)
	if err != nil {
		slog.ErrorContext(ctx, "fb oauth: exchange code", slog.String("err", err.Error()))
		http.Error(w, "exchange failed", http.StatusBadGateway)
		return
	}

	// Step 3: short-lived -> long-lived (60 day).
	longTok, err := h.extendToken(ctx, shortTok)
	if err != nil {
		slog.ErrorContext(ctx, "fb oauth: extend token", slog.String("err", err.Error()))
		http.Error(w, "extend failed", http.StatusBadGateway)
		return
	}

	// Step 4: list Pages.
	pages, err := h.listPages(ctx, longTok)
	if err != nil {
		slog.ErrorContext(ctx, "fb oauth: list pages", slog.String("err", err.Error()))
		http.Error(w, "list pages failed", http.StatusBadGateway)
		return
	}
	if len(pages) == 0 {
		http.Error(w, "no Pages associated with this Facebook account; create or claim one first", http.StatusBadRequest)
		return
	}

	// Step 5: subscribe + persist each Page.
	connected := []string{}
	for _, p := range pages {
		if err := h.subscribePage(ctx, p.ID, p.Token); err != nil {
			slog.WarnContext(ctx, "fb oauth: subscribe",
				slog.String("page_id", p.ID),
				slog.String("err", err.Error()))
			continue
		}
		if err := h.upsertPage(ctx, tenantID, p); err != nil {
			slog.WarnContext(ctx, "fb oauth: upsert page",
				slog.String("page_id", p.ID),
				slog.String("err", err.Error()))
			continue
		}
		connected = append(connected, p.Name)
	}

	// Friendly text response so the brand admin sees what landed.
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = fmt.Fprintf(w, `<!doctype html><html><body style="font-family:system-ui;padding:2rem;max-width:40rem;margin:auto">
<h1>Facebook connected</h1>
<p>Subscribed %d Page(s) to your contact-centre tenant:</p>
<ul>%s</ul>
<p>Inbound DMs and Page comments will now appear in the agent inbox.</p>
<p><a href="/agent/inbox">Back to the inbox</a></p>
</body></html>`,
		len(connected), liItems(connected))
}

// pageInfo is the subset of /me/accounts we use.
type pageInfo struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Token string `json:"access_token"`
}

// ConnectedPage is the JSON shape returned by ListConnected for the
// agent UI's Channels admin page. The encrypted access token never
// appears here.
type ConnectedPage struct {
	PageID            string    `json:"page_id"`
	PageName          string    `json:"page_name"`
	WebhookSubscribed bool      `json:"webhook_subscribed"`
	CreatedAt         time.Time `json:"created_at"`
}

// ListConnected returns the Pages this tenant has connected. Caller
// (the gateway handler) extracts tenant_id from auth.Identity.
func (h *OAuthHandler) ListConnected(ctx context.Context, tenantID uuid.UUID) ([]ConnectedPage, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT page_id, page_name, webhook_subscribed, created_at
		FROM fb_pages
		WHERE tenant_id = $1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectedPage{}
	for rows.Next() {
		var p ConnectedPage
		if err := rows.Scan(&p.PageID, &p.PageName, &p.WebhookSubscribed, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (h *OAuthHandler) exchangeCode(ctx context.Context, code string) (string, error) {
	q := url.Values{}
	q.Set("client_id", h.Cfg.AppID)
	q.Set("client_secret", h.Cfg.AppSecret)
	q.Set("redirect_uri", h.Cfg.RedirectURI)
	q.Set("code", code)
	u := fmt.Sprintf("%s/%s/oauth/access_token?%s",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, q.Encode())
	var resp struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", errors.New("fb oauth: empty access_token in response")
	}
	return resp.AccessToken, nil
}

func (h *OAuthHandler) extendToken(ctx context.Context, shortLived string) (string, error) {
	q := url.Values{}
	q.Set("grant_type", "fb_exchange_token")
	q.Set("client_id", h.Cfg.AppID)
	q.Set("client_secret", h.Cfg.AppSecret)
	q.Set("fb_exchange_token", shortLived)
	u := fmt.Sprintf("%s/%s/oauth/access_token?%s",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, q.Encode())
	var resp struct {
		AccessToken string `json:"access_token"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		return "", err
	}
	if resp.AccessToken == "" {
		return "", errors.New("fb oauth: empty long-lived token")
	}
	return resp.AccessToken, nil
}

func (h *OAuthHandler) listPages(ctx context.Context, userToken string) ([]pageInfo, error) {
	u := fmt.Sprintf("%s/%s/me/accounts?access_token=%s&fields=id,name,access_token",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, url.QueryEscape(userToken))
	var resp struct {
		Data []pageInfo `json:"data"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// subscribePage tells Meta to start delivering webhook events for the
// listed fields to our app's webhook URL (configured in the Meta App
// console, NOT per-Page). This is the load-bearing call that turns
// "we have a Page token" into "we receive DMs".
func (h *OAuthHandler) subscribePage(ctx context.Context, pageID, pageToken string) error {
	q := url.Values{}
	q.Set("access_token", pageToken)
	q.Set("subscribed_fields", strings.Join(h.WebhookFields, ","))
	u := fmt.Sprintf("%s/%s/%s/subscribed_apps?%s",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, pageID, q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return err
	}
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("subscribe %s: HTTP %d: %s", pageID, resp.StatusCode, body)
	}
	return nil
}

func (h *OAuthHandler) upsertPage(ctx context.Context, tenantID uuid.UUID, p pageInfo) error {
	ct, dekID, err := h.Crypter.Encrypt(ctx, []byte(p.Token))
	if err != nil {
		return fmt.Errorf("encrypt page token: %w", err)
	}
	_, err = h.Pool.Exec(ctx, `
		INSERT INTO fb_pages
		  (tenant_id, page_id, page_name, access_token_ct, access_token_dek_id,
		   webhook_subscribed)
		VALUES ($1, $2, $3, $4, $5, TRUE)
		ON CONFLICT (page_id) DO UPDATE
		  SET tenant_id            = EXCLUDED.tenant_id,
		      page_name            = EXCLUDED.page_name,
		      access_token_ct      = EXCLUDED.access_token_ct,
		      access_token_dek_id  = EXCLUDED.access_token_dek_id,
		      webhook_subscribed   = TRUE`,
		tenantID, p.ID, p.Name, ct, dekID)
	return err
}

// getJSON does a GET, decodes JSON, surfaces any non-2xx as an error
// containing Meta's error envelope.
func (h *OAuthHandler) getJSON(ctx context.Context, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	resp, err := h.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, body)
	}
	return json.Unmarshal(body, into)
}

// state encoding: base64url(tenant_id ":" 8-byte-random). Cheap CSRF
// + tenant routing in one value. Production stores state server-side;
// for the demo this is enough -- the worst an attacker can do with a
// guessed state is connect a Page to a tenant they already control.
func newState(tenantID string) string {
	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	raw := append([]byte(tenantID+":"), nonce[:]...)
	return base64.RawURLEncoding.EncodeToString(raw)
}

func tenantFromState(s string) (uuid.UUID, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return uuid.Nil, err
	}
	parts := strings.SplitN(string(raw), ":", 2)
	if len(parts) != 2 {
		return uuid.Nil, errors.New("bad state shape")
	}
	return uuid.Parse(parts[0])
}

func liItems(names []string) string {
	if len(names) == 0 {
		return "<li>(none)</li>"
	}
	var b strings.Builder
	for _, n := range names {
		b.WriteString("<li>")
		b.WriteString(htmlEscape(n))
		b.WriteString("</li>")
	}
	return b.String()
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")
	return r.Replace(s)
}

// DemoCrypter is the local-dev / demo Crypter that AES-GCMs token
// bytes against a single static key derived from FB_DEMO_KEY. Strictly
// for the demo path -- production wires a Vault transit Crypter.
type DemoCrypter struct {
	key []byte // 32 bytes
}

// NewDemoCrypter derives a 32-byte key from the supplied secret via
// SHA-256 so any-length input works. Empty secret returns an error so
// the operator can't accidentally ship an empty key.
func NewDemoCrypter(secret string) (*DemoCrypter, error) {
	if secret == "" {
		return nil, errors.New("fb demo crypter: secret required")
	}
	sum := sha256.Sum256([]byte(secret))
	return &DemoCrypter{key: sum[:]}, nil
}

// Encrypt prepends a fresh 12-byte nonce to the GCM ciphertext.
func (c *DemoCrypter) Encrypt(_ context.Context, plaintext []byte) ([]byte, string, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, "", err
	}
	ct := gcm.Seal(nonce, nonce, plaintext, nil)
	return ct, "demo-static", nil
}

// Decrypt strips the nonce + verifies the GCM tag.
func (c *DemoCrypter) Decrypt(_ context.Context, ciphertext []byte, _ string) ([]byte, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) < gcm.NonceSize() {
		return nil, errors.New("ciphertext too short")
	}
	nonce, sealed := ciphertext[:gcm.NonceSize()], ciphertext[gcm.NonceSize():]
	return gcm.Open(nil, nonce, sealed, nil)
}
