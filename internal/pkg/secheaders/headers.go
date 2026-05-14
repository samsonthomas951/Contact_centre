// Package secheaders applies the security headers a third-party
// pentest expects to find on every response.
//
// Defaults are pessimistic; callers can override individual headers
// via Options when a specific surface needs to relax one. The widget
// JS endpoint, for example, is served from a CDN with its own CSP.
package secheaders

import "net/http"

// Options are the headers we set. Empty strings disable the
// corresponding header.
type Options struct {
	ContentSecurityPolicy   string
	StrictTransportSecurity string
	ReferrerPolicy          string
	XContentTypeOptions     string
	XFrameOptions           string
	PermissionsPolicy       string
	CrossOriginOpener       string
	CrossOriginResource     string
}

// Defaults returns a hardened set:
//
//   - HSTS: 1 year, include subdomains, opt into preload.
//   - CSP: default-src 'none'; explicit allowlists per directive --
//     callers extend via Options.ContentSecurityPolicy.
//   - Referrer-Policy: no-referrer (no Referer leak to third parties).
//   - X-Content-Type-Options: nosniff.
//   - X-Frame-Options: DENY (defence in depth against clickjacking
//     on UAs that don't honour the frame-ancestors CSP).
//   - Permissions-Policy: disable every Powerful Feature we don't use.
//   - Cross-Origin-Opener-Policy: same-origin (isolate from third parties).
//   - Cross-Origin-Resource-Policy: same-origin.
func Defaults() Options {
	return Options{
		ContentSecurityPolicy: "default-src 'none'; " +
			"frame-ancestors 'none'; " +
			"base-uri 'none'; " +
			"form-action 'self'",
		StrictTransportSecurity: "max-age=31536000; includeSubDomains; preload",
		ReferrerPolicy:          "no-referrer",
		XContentTypeOptions:     "nosniff",
		XFrameOptions:           "DENY",
		PermissionsPolicy: "accelerometer=(), camera=(), geolocation=(), gyroscope=(), " +
			"magnetometer=(), microphone=(), payment=(), usb=()",
		CrossOriginOpener:   "same-origin",
		CrossOriginResource: "same-origin",
	}
}

// Middleware writes the configured headers on every response.
func Middleware(o Options) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			if o.ContentSecurityPolicy != "" {
				h.Set("Content-Security-Policy", o.ContentSecurityPolicy)
			}
			if o.StrictTransportSecurity != "" {
				h.Set("Strict-Transport-Security", o.StrictTransportSecurity)
			}
			if o.ReferrerPolicy != "" {
				h.Set("Referrer-Policy", o.ReferrerPolicy)
			}
			if o.XContentTypeOptions != "" {
				h.Set("X-Content-Type-Options", o.XContentTypeOptions)
			}
			if o.XFrameOptions != "" {
				h.Set("X-Frame-Options", o.XFrameOptions)
			}
			if o.PermissionsPolicy != "" {
				h.Set("Permissions-Policy", o.PermissionsPolicy)
			}
			if o.CrossOriginOpener != "" {
				h.Set("Cross-Origin-Opener-Policy", o.CrossOriginOpener)
			}
			if o.CrossOriginResource != "" {
				h.Set("Cross-Origin-Resource-Policy", o.CrossOriginResource)
			}
			next.ServeHTTP(w, r)
		})
	}
}
