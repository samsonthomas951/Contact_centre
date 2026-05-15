package facebook

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"

	"github.com/google/uuid"
)

// IG (Path 1, FB-linked) and WA discovery extensions to the FB OAuth
// callback. After the brand admin grants the union of FB + IG + WA
// scopes in one consent screen, we enumerate everything they own and
// register each asset with the right tenant.
//
// One OAuth, three channels -- matches how Meta wants Business apps to
// surface during onboarding (Embedded Signup is the more elaborate
// variant; this path uses the regular Login dialog with the WhatsApp
// permissions added).

// igInfo is the subset of the IG Business Account graph we use.
type igInfo struct {
	IGUserID string
	Username string
}

// discoverIGForPage hits GET /{page_id}?fields=instagram_business_account{id,username}
// and returns the linked IG Business account if one exists. Pages
// without an IG link return ok=false (not an error).
func (h *OAuthHandler) discoverIGForPage(ctx context.Context, p pageInfo) (igInfo, bool) {
	q := url.Values{}
	q.Set("fields", "instagram_business_account{id,username}")
	q.Set("access_token", p.Token)
	u := fmt.Sprintf("%s/%s/%s?%s",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, p.ID, q.Encode())

	var resp struct {
		InstagramBusinessAccount *struct {
			ID       string `json:"id"`
			Username string `json:"username"`
		} `json:"instagram_business_account"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		slog.WarnContext(ctx, "fb oauth: discover ig",
			slog.String("page_id", p.ID),
			slog.String("err", err.Error()))
		return igInfo{}, false
	}
	if resp.InstagramBusinessAccount == nil {
		return igInfo{}, false
	}
	return igInfo{
		IGUserID: resp.InstagramBusinessAccount.ID,
		Username: resp.InstagramBusinessAccount.Username,
	}, true
}

// upsertIGAccount writes the Path-1 IG row. The IG webhook handler
// already exists; the resolver looks the IG user id up in this table
// when Meta delivers a webhook.
func (h *OAuthHandler) upsertIGAccount(ctx context.Context, tenantID uuid.UUID, ig igInfo, fbPageID string) error {
	_, err := h.Pool.Exec(ctx, `
		INSERT INTO ig_accounts
		  (tenant_id, ig_user_id, username, flow, fb_page_id, webhook_subscribed)
		VALUES ($1, $2, $3, 'path1', $4, TRUE)
		ON CONFLICT (ig_user_id) DO UPDATE
		  SET tenant_id          = EXCLUDED.tenant_id,
		      username           = EXCLUDED.username,
		      fb_page_id         = EXCLUDED.fb_page_id,
		      webhook_subscribed = TRUE`,
		tenantID, ig.IGUserID, ig.Username, fbPageID)
	return err
}

// businessRow is one entry in /me/businesses.
type businessRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// wabaRow is one entry in /{business_id}/owned_whatsapp_business_accounts.
type wabaRow struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// waPhoneRow is one entry in /{waba_id}/phone_numbers.
type waPhoneRow struct {
	ID                 string `json:"id"`
	DisplayPhoneNumber string `json:"display_phone_number"`
	VerifiedName       string `json:"verified_name"`
}

// discoverAndPersistWA walks Business → WABA → phone numbers,
// subscribes each WABA's webhook, and persists each phone number into
// wa_phone_numbers. Returns the human-readable list of connected
// numbers for the response page.
func (h *OAuthHandler) discoverAndPersistWA(ctx context.Context, tenantID uuid.UUID, userToken string) ([]string, error) {
	businesses, err := h.listBusinesses(ctx, userToken)
	if err != nil {
		return nil, fmt.Errorf("list businesses: %w", err)
	}
	connected := []string{}
	for _, biz := range businesses {
		wabas, err := h.listWABAs(ctx, biz.ID, userToken)
		if err != nil {
			slog.WarnContext(ctx, "fb oauth: list wabas",
				slog.String("business_id", biz.ID),
				slog.String("err", err.Error()))
			continue
		}
		for _, w := range wabas {
			// Subscribe the WABA so Meta starts delivering webhooks
			// for messages on its phone numbers.
			if err := h.subscribeWABA(ctx, w.ID, userToken); err != nil {
				slog.WarnContext(ctx, "fb oauth: subscribe waba",
					slog.String("waba_id", w.ID),
					slog.String("err", err.Error()))
				// Still persist the phone numbers; the brand can
				// re-trigger subscription later.
			}
			phones, err := h.listPhones(ctx, w.ID, userToken)
			if err != nil {
				slog.WarnContext(ctx, "fb oauth: list phones",
					slog.String("waba_id", w.ID),
					slog.String("err", err.Error()))
				continue
			}
			for _, ph := range phones {
				if err := h.upsertWAPhone(ctx, tenantID, w.ID, ph, userToken); err != nil {
					slog.WarnContext(ctx, "fb oauth: upsert wa phone",
						slog.String("phone_number_id", ph.ID),
						slog.String("err", err.Error()))
					continue
				}
				connected = append(connected, ph.DisplayPhoneNumber+" ("+ph.VerifiedName+")")
			}
		}
	}
	return connected, nil
}

func (h *OAuthHandler) listBusinesses(ctx context.Context, userToken string) ([]businessRow, error) {
	u := fmt.Sprintf("%s/%s/me/businesses?access_token=%s&fields=id,name",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, url.QueryEscape(userToken))
	var resp struct {
		Data []businessRow `json:"data"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (h *OAuthHandler) listWABAs(ctx context.Context, businessID, userToken string) ([]wabaRow, error) {
	u := fmt.Sprintf("%s/%s/%s/owned_whatsapp_business_accounts?access_token=%s&fields=id,name",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, businessID, url.QueryEscape(userToken))
	var resp struct {
		Data []wabaRow `json:"data"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

func (h *OAuthHandler) listPhones(ctx context.Context, wabaID, userToken string) ([]waPhoneRow, error) {
	u := fmt.Sprintf("%s/%s/%s/phone_numbers?access_token=%s&fields=id,display_phone_number,verified_name",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, wabaID, url.QueryEscape(userToken))
	var resp struct {
		Data []waPhoneRow `json:"data"`
	}
	if err := h.getJSON(ctx, u, &resp); err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// subscribeWABA mirrors subscribePage but for WhatsApp -- the WABA is
// the unit Meta subscribes for message delivery.
func (h *OAuthHandler) subscribeWABA(ctx context.Context, wabaID, userToken string) error {
	u := fmt.Sprintf("%s/%s/%s/subscribed_apps?access_token=%s",
		strings.TrimRight(h.Cfg.GraphHost, "/"),
		h.Cfg.GraphVersion, wabaID, url.QueryEscape(userToken))

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
		return fmt.Errorf("subscribe waba %s: HTTP %d: %s", wabaID, resp.StatusCode, body)
	}
	return nil
}

// ConnectedIG is one row returned by ListConnectedIG for the agent
// UI's Channels page.
type ConnectedIG struct {
	IGUserID  string `json:"ig_user_id"`
	Username  string `json:"username"`
	FBPageID  string `json:"fb_page_id"`
	Subscribed bool  `json:"webhook_subscribed"`
}

// ConnectedWA is one row returned by ListConnectedWA.
type ConnectedWA struct {
	PhoneNumberID      string `json:"phone_number_id"`
	WABAID             string `json:"waba_id"`
	DisplayPhoneNumber string `json:"display_phone_number"`
	Subscribed         bool   `json:"webhook_subscribed"`
}

// ListConnectedIG returns the IG accounts this tenant has connected.
func (h *OAuthHandler) ListConnectedIG(ctx context.Context, tenantID uuid.UUID) ([]ConnectedIG, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT ig_user_id, username, COALESCE(fb_page_id,''), webhook_subscribed
		FROM ig_accounts
		WHERE tenant_id = $1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectedIG{}
	for rows.Next() {
		var ig ConnectedIG
		if err := rows.Scan(&ig.IGUserID, &ig.Username, &ig.FBPageID, &ig.Subscribed); err != nil {
			return nil, err
		}
		out = append(out, ig)
	}
	return out, rows.Err()
}

// ListConnectedWA returns the WA phone numbers this tenant has connected.
func (h *OAuthHandler) ListConnectedWA(ctx context.Context, tenantID uuid.UUID) ([]ConnectedWA, error) {
	rows, err := h.Pool.Query(ctx, `
		SELECT phone_number_id, waba_id, display_phone_number, webhook_subscribed
		FROM wa_phone_numbers
		WHERE tenant_id = $1
		ORDER BY created_at DESC`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConnectedWA{}
	for rows.Next() {
		var w ConnectedWA
		if err := rows.Scan(&w.PhoneNumberID, &w.WABAID, &w.DisplayPhoneNumber, &w.Subscribed); err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, rows.Err()
}

// upsertWAPhone persists one phone number id with the user-token as
// the access token. WA permissions are scoped against the user token
// for the demo path; production typically uses a System User token
// generated against the WABA so it never expires.
func (h *OAuthHandler) upsertWAPhone(ctx context.Context, tenantID uuid.UUID, wabaID string, ph waPhoneRow, userToken string) error {
	ct, dekID, err := h.Crypter.Encrypt(ctx, []byte(userToken))
	if err != nil {
		return fmt.Errorf("encrypt wa token: %w", err)
	}
	_, err = h.Pool.Exec(ctx, `
		INSERT INTO wa_phone_numbers
		  (tenant_id, waba_id, phone_number_id, display_phone_number,
		   access_token_ct, dek_id, webhook_subscribed)
		VALUES ($1, $2, $3, $4, $5, $6, TRUE)
		ON CONFLICT (phone_number_id) DO UPDATE
		  SET tenant_id            = EXCLUDED.tenant_id,
		      waba_id              = EXCLUDED.waba_id,
		      display_phone_number = EXCLUDED.display_phone_number,
		      access_token_ct      = EXCLUDED.access_token_ct,
		      dek_id               = EXCLUDED.dek_id,
		      webhook_subscribed   = TRUE`,
		tenantID, wabaID, ph.ID, ph.DisplayPhoneNumber, ct, dekID)
	return err
}
