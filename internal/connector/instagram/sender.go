package instagram

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

// Sender wraps the IG Messaging API outbound path. The Graph API
// endpoint mirrors Messenger:
//
//   POST https://graph.facebook.com/<version>/<ig_user_id>/messages
//      ?access_token=<token>
//
// Body: { recipient: {id: <igsid>}, message: {text: "..."} }.
//
// For Path 1 (FB-linked IG Business accounts, the only path the OAuth
// handler currently supports) the access token is the linked Page's
// access token. The TokenStore implementation handles the lookup.
type Sender struct {
	HTTP         *http.Client
	Tokens       TokenStore
	GraphHost    string
	GraphVersion string
}

// TokenStore returns the access token to use for a given IG user id.
// Production wires PGTokenStore (this package); tests inject a fake.
type TokenStore interface {
	Token(ctx context.Context, igUserID string) (string, error)
}

// NewSender builds a Sender with sane defaults.
func NewSender(tokens TokenStore) *Sender {
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
		Tokens:       tokens,
		GraphHost:    "https://graph.facebook.com",
		GraphVersion: "v22.0",
	}
}

// SendText sends a DM reply from the given IG Business account to the
// given IG-scoped sender id (IGSID). Returns the platform message id
// Meta echoes back so the caller can correlate.
func (s *Sender) SendText(ctx context.Context, igUserID, recipientIGSID, body string) (string, error) {
	if igUserID == "" || recipientIGSID == "" {
		return "", errors.New("ig: ig_user_id and recipient required")
	}
	if strings.TrimSpace(body) == "" {
		return "", errors.New("ig: empty body")
	}
	tok, err := s.Tokens.Token(ctx, igUserID)
	if err != nil {
		return "", fmt.Errorf("ig: token lookup: %w", err)
	}

	payload := struct {
		Recipient struct {
			ID string `json:"id"`
		} `json:"recipient"`
		Message struct {
			Text string `json:"text"`
		} `json:"message"`
	}{}
	payload.Recipient.ID = recipientIGSID
	payload.Message.Text = body

	enc, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	url := fmt.Sprintf("%s/%s/%s/messages?access_token=%s",
		strings.TrimRight(s.GraphHost, "/"), s.GraphVersion, igUserID, tok)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(enc))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := s.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("ig: graph request: %w", err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", graphError(resp.StatusCode, bodyBytes)
	}

	var ok struct {
		MessageID   string `json:"message_id"`
		RecipientID string `json:"recipient_id"`
	}
	if err := json.Unmarshal(bodyBytes, &ok); err != nil {
		return "", fmt.Errorf("ig: graph reply: %w", err)
	}
	return ok.MessageID, nil
}

// Error is the typed failure for IG outbound. Same shape as
// facebook.Error so callers can switch on Code / Status uniformly.
type Error struct {
	Status        int
	Code, Subcode int
	Type, Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("ig: graph %d code=%d subcode=%d type=%s: %s",
		e.Status, e.Code, e.Subcode, e.Type, e.Message)
}

// IsTransient mirrors facebook.Error's classification -- IG uses the
// same Graph API error codes (4 / 17 / 32 / 613 = rate-limit family).
func (e *Error) IsTransient() bool {
	switch e.Code {
	case 4, 17, 32, 613:
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
