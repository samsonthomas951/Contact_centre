package voice

import "fmt"

// ConsentPrompt is the IVR Africa's Talking will play when a caller
// first reaches the number. Kenya DPA requires explicit consent for
// recording; pressing 1 records consent and continues, 2 routes to a
// non-recorded queue, no input hangs up after the timeout.
//
// Africa's Talking XML schema:
//   <Response>
//     <GetDigits timeout="N" finishOnKey="#" callbackUrl="...">
//       <Say>...</Say>
//     </GetDigits>
//   </Response>
func ConsentPrompt(callbackURL string, sayText string) string {
	if sayText == "" {
		sayText = "Welcome. This call may be recorded for support quality. " +
			"Press 1 to consent and continue. Press 2 to continue without recording."
	}
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<Response>`+
			`<GetDigits timeout="20" finishOnKey="#" numDigits="1" callbackUrl="%s">`+
			`<Say>%s</Say>`+
			`</GetDigits>`+
			`</Response>`,
		escapeXML(callbackURL), escapeXML(sayText))
}

// BridgeToAgent returns the AT XML that bridges the caller to a SIP
// agent destination or dials the agent's mobile.
func BridgeToAgent(dialNumber string) string {
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<Response>`+
			`<Dial phoneNumbers="%s" record="true" sequential="true" />`+
			`</Response>`,
		escapeXML(dialNumber))
}

// NoRecordingBridge bridges without recording -- used when the caller
// declined recording during the consent prompt.
func NoRecordingBridge(dialNumber string) string {
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<Response>`+
			`<Dial phoneNumbers="%s" record="false" sequential="true" />`+
			`</Response>`,
		escapeXML(dialNumber))
}

// PolitelyEnd returns AT XML that thanks the caller and hangs up. Used
// when no agent is available or the caller declined the call entirely.
func PolitelyEnd(message string) string {
	if message == "" {
		message = "We're sorry, no agent is available right now. Please try again later."
	}
	return fmt.Sprintf(
		`<?xml version="1.0" encoding="UTF-8"?>`+
			`<Response><Say>%s</Say></Response>`,
		escapeXML(message))
}

// escapeXML replaces the five XML-reserved characters. We don't import
// encoding/xml because we're emitting a tiny static envelope, and
// `xml.EscapeText` requires a writer.
func escapeXML(s string) string {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		switch r {
		case '&':
			out = append(out, []byte("&amp;")...)
		case '<':
			out = append(out, []byte("&lt;")...)
		case '>':
			out = append(out, []byte("&gt;")...)
		case '"':
			out = append(out, []byte("&quot;")...)
		case '\'':
			out = append(out, []byte("&apos;")...)
		default:
			if r < 0x20 && r != '\t' && r != '\n' && r != '\r' {
				continue
			}
			out = append(out, []byte(string(r))...)
		}
	}
	return string(out)
}
