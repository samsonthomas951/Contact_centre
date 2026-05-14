// Package voice implements the voice channel described in §14 of the
// technical plan. Phase-3 default carrier is Africa's Talking
// (Kenyan-incorporated, lowest friction for a Kenyan deployment); the
// architecture is provider-agnostic via voice_numbers.provider.
//
// Africa's Talking webhook lifecycle:
//
//   ringing     ->  notification reaches /v1/voice/at on inbound. We
//                   reply with XML that runs an IVR consent prompt
//                   (Kenya DPA s.30/s.45 -- explicit consent for
//                   recording).
//   dtmf 1 / 2  ->  same endpoint, isActive=1, dtmfDigits set. We
//                   record consent_dtmf and either bridge to the agent
//                   (1) or play a "no recording" branch (2).
//   completed   ->  isActive=0, recordingUrl set. We mirror the
//                   recording into MinIO via Doc Svc, link the
//                   resulting documents.id onto voice_calls, and
//                   transition the ticket state.
package voice

import (
	"encoding/json"
	"time"
)

// ATEvent is the Africa's Talking notification payload, decoded from
// the application/x-www-form-urlencoded body the carrier POSTs. We
// shadow url.Values into a struct so call sites stay readable.
type ATEvent struct {
	SessionID        string
	IsActive         bool   // "1" inbound start; "0" completed
	Direction        string // Inbound | Outbound
	CallerNumber     string // E.164
	DestinationNumber string
	DialNumber       string
	DTMFDigits       string
	DurationSec      int
	RecordingURL     string
	HangupCause      string
}

// FromATForm decodes the carrier's x-www-form-urlencoded body. Returns
// (event, true) when at least sessionId is present.
func FromATForm(form map[string][]string) (ATEvent, bool) {
	get := func(k string) string {
		if v, ok := form[k]; ok && len(v) > 0 {
			return v[0]
		}
		return ""
	}
	e := ATEvent{
		SessionID:         get("sessionId"),
		IsActive:          get("isActive") == "1",
		Direction:         get("direction"),
		CallerNumber:      get("callerNumber"),
		DestinationNumber: get("destinationNumber"),
		DialNumber:        get("dialNumber"),
		DTMFDigits:        get("dtmfDigits"),
		RecordingURL:      get("recordingUrl"),
		HangupCause:       get("hangupCause"),
	}
	if d := get("durationInSeconds"); d != "" {
		e.DurationSec = parseInt(d)
	}
	return e, e.SessionID != ""
}

// InboundEvent is the normalised event the connector publishes to NATS
// subject `ingress.voice.<kind>`. kind ∈ {ringing, consent, answered, ended}.
type InboundEvent struct {
	TenantID        string    `json:"tenant_id"`
	VoiceNumberID   string    `json:"voice_number_id"`
	Channel         string    `json:"channel"`        // always "voice"
	Kind            string    `json:"kind"`
	SessionID       string    `json:"session_id"`
	Caller          string    `json:"caller"`
	Callee          string    `json:"callee"`
	Direction       string    `json:"direction"`      // inbound|outbound
	ConsentDTMF     string    `json:"consent_dtmf,omitempty"`
	RecordingURL    string    `json:"recording_url,omitempty"`
	DurationSeconds int       `json:"duration_seconds,omitempty"`
	OccurredAt      time.Time `json:"occurred_at"`
}

// MarshalNATS produces the on-wire JSON.
func (e InboundEvent) MarshalNATS() ([]byte, error) { return json.Marshal(e) }

// Kind classifies an ATEvent into a normalised kind.
//
// The state machine:
//   isActive=true,  dtmfDigits=""           -> "ringing"      (run consent IVR)
//   isActive=true,  dtmfDigits="1"|"2"      -> "consent"      (route or end)
//   isActive=true,  hangupCause=""          -> "answered"     (bridged to agent)
//   isActive=false                          -> "ended"        (recording URL set)
func (e ATEvent) Kind() string {
	switch {
	case !e.IsActive:
		return "ended"
	case e.DTMFDigits == "1" || e.DTMFDigits == "2":
		return "consent"
	case e.HangupCause == "":
		// Without DTMF and still active -- either ringing or already bridged.
		// In AT's model the same callback re-fires through the call's life;
		// we treat the first occurrence (no recordingUrl yet) as "ringing"
		// and assume "answered" once an answer event arrives separately.
		return "ringing"
	default:
		return "ringing"
	}
}

// Normalize turns an ATEvent + resolved tenant into the canonical event.
func Normalize(tenantID, voiceNumberID string, e ATEvent) InboundEvent {
	return InboundEvent{
		TenantID:        tenantID,
		VoiceNumberID:   voiceNumberID,
		Channel:         "voice",
		Kind:            e.Kind(),
		SessionID:       e.SessionID,
		Caller:          e.CallerNumber,
		Callee:          e.DestinationNumber,
		Direction:       e.Direction,
		ConsentDTMF:     e.DTMFDigits,
		RecordingURL:    e.RecordingURL,
		DurationSeconds: e.DurationSec,
		OccurredAt:      time.Now().UTC(),
	}
}

func parseInt(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
