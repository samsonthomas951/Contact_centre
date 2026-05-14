package whatsapp

import "time"

// CustomerWindow is the 24-hour service-message window WhatsApp opens
// each time the customer sends an inbound message. Outbound replies
// that fit inside the window are *free*. Outside it, a marketing or
// utility *template* (pre-approved by Meta) must be used and is
// per-message billable. See §17 of the technical plan and the WA
// Business pricing model that switched to per-message on 2025-07-01.
const CustomerWindow = 24 * time.Hour

// WindowOpen returns true when an outbound at `outboundAt` qualifies
// as a free service message — i.e. the last inbound message from the
// customer arrived within the last 24 hours.
//
// `lastInboundAt` is the wall-clock time of the most recent message
// the customer sent on this conversation. A zero time means the
// customer has never written to us, so the window is closed and a
// template is required.
func WindowOpen(lastInboundAt, outboundAt time.Time) bool {
	if lastInboundAt.IsZero() {
		return false
	}
	return outboundAt.Sub(lastInboundAt) <= CustomerWindow
}

// Strategy advises the agent UI / outbound code path on what to do.
//
// "service" → send via /messages with type=text or media.
// "template" → must use a pre-approved template; bill applies.
type Strategy string

const (
	StrategyService  Strategy = "service"
	StrategyTemplate Strategy = "template"
)

// Recommend returns the outbound strategy. The agent UI uses this to
// surface "Window is open, free reply" vs "Window closed, pick a
// template" before composing.
func Recommend(lastInboundAt, outboundAt time.Time) Strategy {
	if WindowOpen(lastInboundAt, outboundAt) {
		return StrategyService
	}
	return StrategyTemplate
}
