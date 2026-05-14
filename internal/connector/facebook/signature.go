package facebook

import "github.com/samsonthomas951/contact-centre/internal/connector/meta"

// SignatureHeader is the HTTP header Meta sets on every webhook
// delivery; aliased from the shared meta package.
const SignatureHeader = meta.SignatureHeader

// VerifySignature delegates to the shared Meta implementation. Kept as
// a thin wrapper so existing call sites stay unchanged while the
// implementation lives in one place.
func VerifySignature(rawBody []byte, header, appSecret string) bool {
	return meta.VerifySignature(rawBody, header, appSecret)
}
