package facebook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Sender wraps the Meta Graph API outbound path. One Sender is shared
// across the whole gateway/connector binary; per-Page state (token +
// BUC headers) lives in PageStore implementations.
//
// The Graph API endpoint is:
//
//   POST https://graph.facebook.com/<version>/<page_id>/messages
//      ?access_token=<page_token>
//
// Body: { recipient: {id: <psid>}, message: {text: "..."} }.
//
// We deliberately don't pin a v22+ version constant -- Meta deprecates
// versions yearly; the operator sets it via env so a hot fix doesn't
// need a release.
type Sender struct {
	HTTP   *http.Client
	Pages  PageStore
	Cost   CostObserver
	GraphHost   string  // default "https://graph.facebook.com"
	GraphVersion string // default "v22.0"
}

// PageStore decrypts the Page access token + records the inevitable
// BUC header observations from each response. The implementation
// against fb_pages + Vault lives outside this file (envelope DEK
// fetch + AES-GCM); for the dev path the connector binary may inject
// a plaintext-pass-through.
type PageStore interface {
	// Token returns the plaintext Page access token for the page.
	// Errors propagate -- a bad token surfaces 401 from Meta which
	// the outbound queue translates to "rotate".
	Token(ctx context.Context, pageID string) (string, error)
	// ObserveBUC parses the X-Business-Use-Case-Usage header (each
	// response Meta sends carries this, per §4.1 of the technical
	// plan; we monitor it and throttle when any percentage hits 80).
	ObserveBUC(ctx context.Context, pageID, header string)
}

// CostObserver lets the supervisor dashboard render outbound counters.
// Optional -- nil is a no-op.
type CostObserver interface {
	RecordOutbound(pageID string, ok bool)
}

// NewSender builds a Sender with sane defaults. Caller passes the
// PageStore + optional CostObserver.
func NewSender(pages PageStore, cost CostObserver) *Sender {
	return &Sender{
		HTTP: &http.Client{
			Timeout: 15 * time.Second,
			Transport: &http.Transport{
				MaxIdleConnsPerHost:   8,
				MaxConnsPerHost:       16,
				IdleConnTimeout:       60 * time.Second,
				ResponseHeaderTimeout: 15 * time.Second,
			},
		},
		Pages:        pages,
		Cost:         cost,
		GraphHost:    "https://graph.facebook.com",
		GraphVersion: "v22.0",
	}
}

// SendText sends a plain-text Messenger reply. Returns the platform
// message id Meta echoes back (`mid.*`) so the caller can correlate
// the outbound row in messages.platform_message_id.
func (s *Sender) SendText(ctx context.Context, pageID, recipientPSID, body string) (string, error) {
	if pageID == "" || recipientPSID == "" {
		return "", errors.New("fb: page_id and recipient required")
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("fb: empty body")
	}
	tok, err := s.Pages.Token(ctx, pageID)
	if err != nil {
		return "", fmt.Errorf("fb: token lookup: %w", err)
	}

	payload := struct {
		Recipient struct {
			ID string `json:"id"`
		} `json:"recipient"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
		// MessagingType "RESPONSE" is appropriate within the 24-hour
		// customer-window. Outside the window Messenger requires
		// MESSAGE_TAG which the next iteration adds.
		MessagingType string `json:"messaging_type"`
	}{}
	payload.Recipient.ID = recipientPSID
	payload.Message.Text = body
	payload.MessagingType = "RESPONSE"

	enc, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s/%s/messages?access_token=%s",
		strings.TrimRight(s.GraphHost, "/"), s.GraphVersion, pageID, tok)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(enc))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		s.recordCost(pageID, false)
		return "", fmt.Errorf("fb: graph request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	if h := resp.Header.Get("X-Business-Use-Case-Usage"); h != "" && s.Pages != nil {
		s.Pages.ObserveBUC(ctx, pageID, h)
	}

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.recordCost(pageID, false)
		return "", graphError(resp.StatusCode, bodyBytes)
	}

	var ok struct {
		MessageID   string `json:"message_id"`
		RecipientID string `json:"recipient_id"`
	}
	if err := json.Unmarshal(bodyBytes, &ok); err != nil {
		s.recordCost(pageID, false)
		return "", fmt.Errorf("fb: graph reply: %w", err)
	}
	s.recordCost(pageID, true)
	return ok.MessageID, nil
}

// graphError returns a typed error so the outbound queue can decide
// retry vs hard-fail. Meta's error envelope:
//
//   {"error":{"message":"...","type":"OAuthException","code":190, ...}}
//
// We surface the code so the caller can switch on permanent (190 =
// invalid token, 100 = invalid param) vs transient (1, 2, 4, 17, 32 =
// rate-limit family).
func graphError(status int, body []byte) error {
	var env struct {
		Error struct {
			Message      string `json:"message"`
			Type         string `json:"type"`
			Code         int    `json:"code"`
			ErrorSubcode int    `json:"error_subcode"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &env)
	return &Error{
		Status:  status,
		Code:    env.Error.Code,
		Subcode: env.Error.ErrorSubcode,
		Type:    env.Error.Type,
		Message: env.Error.Message,
	}
}

// Error is the typed failure mode every outbound caller sees.
type Error struct {
	Status         int
	Code, Subcode  int
	Type, Message  string
}

// Error implements error.
func (e *Error) Error() string {
	return fmt.Sprintf("fb: graph %d code=%d subcode=%d type=%s: %s",
		e.Status, e.Code, e.Subcode, e.Type, e.Message)
}

// IsTransient reports whether the outbound queue should retry. Per
// §17 of the plan: codes 4, 17, 32, 613 are the rate-limit family;
// 5xx and timeout (no code, transport error -> not classified here)
// are also transient.
func (e *Error) IsTransient() bool {
	switch e.Code {
	case 4, 17, 32, 613:
		return true
	}
	if e.Status >= 500 {
		return true
	}
	return false
}

func (s *Sender) recordCost(pageID string, ok bool) {
	if s.Cost == nil {
		return
	}
	s.Cost.RecordOutbound(pageID, ok)
}
