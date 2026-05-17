package whatsapp

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

// Sender wraps the WhatsApp Cloud API outbound path. Endpoint:
//
//   POST https://graph.facebook.com/<version>/<phone_number_id>/messages
//
// With Bearer auth (not the access_token query param FB Messenger uses):
//
//   Authorization: Bearer <system_user_token>
//
// Body (text reply, customer is in the 24h window):
//
//   {
//     "messaging_product": "whatsapp",
//     "recipient_type":    "individual",
//     "to":                "<E.164 no +>",
//     "type":              "text",
//     "text":              { "body": "..." }
//   }
//
// Outside the 24h customer window WA requires a template — the
// outbound queue's "no_template" error surfaces that to the agent UI.
type Sender struct {
	HTTP         *http.Client
	Numbers      NumberStore
	GraphHost    string
	GraphVersion string
}

// NumberStore returns the system-user access token for a given
// phone_number_id (the Meta-assigned routing id, not the display
// number). Production wires PGNumberStore (this package).
type NumberStore interface {
	Token(ctx context.Context, phoneNumberID string) (string, error)
}

// NewSender builds a Sender with sane defaults.
func NewSender(numbers NumberStore) *Sender {
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
		Numbers:      numbers,
		GraphHost:    "https://graph.facebook.com",
		GraphVersion: "v22.0",
	}
}

// SendText ships a plain-text message to the customer's WA number.
// recipientWAID is the customer's number in E.164 *without* the
// leading "+" (per Meta's spec). Returns the platform message id.
func (s *Sender) SendText(ctx context.Context, phoneNumberID, recipientWAID, body string) (string, error) {
	if phoneNumberID == "" || recipientWAID == "" {
		return "", errors.New("wa: phone_number_id and recipient required")
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("wa: empty body")
	}
	tok, err := s.Numbers.Token(ctx, phoneNumberID)
	if err != nil {
		return "", fmt.Errorf("wa: token lookup: %w", err)
	}

	payload := struct {
		MessagingProduct string `json:"messaging_product"`
		RecipientType    string `json:"recipient_type"`
		To               string `json:"to"`
		Type             string `json:"type"`
		Text             struct {
			Body       string `json:"body"`
			PreviewURL bool   `json:"preview_url"`
		} `json:"text"`
	}{
		MessagingProduct: "whatsapp",
		RecipientType:    "individual",
		To:               strings.TrimPrefix(recipientWAID, "+"),
		Type:             "text",
	}
	payload.Text.Body = body
	payload.Text.PreviewURL = false

	enc, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s/%s/messages",
		strings.TrimRight(s.GraphHost, "/"), s.GraphVersion, phoneNumberID)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(enc))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("wa: graph request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", graphError(resp.StatusCode, bodyBytes)
	}

	// WA response shape:
	//   {"messaging_product":"whatsapp","contacts":[...],"messages":[{"id":"wamid.XXX"}]}
	var ok struct {
		Messages []struct {
			ID string `json:"id"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(bodyBytes, &ok); err != nil || len(ok.Messages) == 0 {
		return "", fmt.Errorf("wa: graph reply: %s", string(bodyBytes))
	}
	return ok.Messages[0].ID, nil
}

// Error mirrors the Graph error shape with WA-specific transient
// classification.
type Error struct {
	Status        int
	Code, Subcode int
	Type, Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("wa: graph %d code=%d subcode=%d type=%s: %s",
		e.Status, e.Code, e.Subcode, e.Type, e.Message)
}

// IsTransient -- WA shares the Graph rate-limit family plus 130472
// (user's number experiencing issues). 131056 (pair rate limit) is
// also transient.
func (e *Error) IsTransient() bool {
	switch e.Code {
	case 4, 17, 32, 613, 130472, 131056:
		return true
	}
	return e.Status >= 500
}

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
