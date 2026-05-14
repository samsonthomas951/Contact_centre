// Package logging configures slog for the service binaries.
//
// Output is line-delimited JSON to stdout so Promtail/Loki can ingest
// without extra parsing. Every log line includes:
//   - service name and version (constants set at startup)
//   - correlation_id pulled from context when present
//   - tenant_id pulled from context when present
//
// PII redaction is handled by the slog.HandlerOptions.ReplaceAttr hook;
// callers should still avoid logging customer message bodies.
package logging

import (
	"context"
	"io"
	"log/slog"
	"os"
	"regexp"
	"strings"

	ctxkeys "github.com/samsonthomas951/contact-centre/internal/pkg/ctxkeys"
)

// Options configures the package logger.
type Options struct {
	Service string
	Version string
	Level   slog.Level
	Out     io.Writer // defaults to os.Stdout
}

// Init installs a JSON slog handler as the default logger.
func Init(opts Options) *slog.Logger {
	out := opts.Out
	if out == nil {
		out = os.Stdout
	}
	h := slog.NewJSONHandler(out, &slog.HandlerOptions{
		Level:       opts.Level,
		AddSource:   false,
		ReplaceAttr: redact,
	})
	logger := slog.New(&contextHandler{Handler: h}).With(
		slog.String("service", opts.Service),
		slog.String("version", opts.Version),
	)
	slog.SetDefault(logger)
	return logger
}

// contextHandler enriches each record with values pulled from context.
type contextHandler struct{ slog.Handler }

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if cid, ok := ctx.Value(ctxkeys.CorrelationID).(string); ok && cid != "" {
		r.AddAttrs(slog.String("correlation_id", cid))
	}
	if tid, ok := ctx.Value(ctxkeys.TenantID).(string); ok && tid != "" {
		r.AddAttrs(slog.String("tenant_id", tid))
	}
	if aid, ok := ctx.Value(ctxkeys.AgentID).(string); ok && aid != "" {
		r.AddAttrs(slog.String("agent_id", aid))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *contextHandler) WithAttrs(as []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(as)}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name)}
}

// Kenyan national ID — 7 or 8 digits, sometimes prefixed by "ID".
var kenyaIDRe = regexp.MustCompile(`\b(ID[: ]?)?\d{7,8}\b`)

// Kenyan mobile MSISDN: +2547xxxxxxxx / 07xxxxxxxx / 2547xxxxxxxx.
var kenyaPhoneRe = regexp.MustCompile(`\b(?:\+?254|0)7\d{8}\b`)

// Bearer/JWT-shaped tokens.
var bearerRe = regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._\-]+`)

func redact(_ []string, a slog.Attr) slog.Attr {
	if a.Value.Kind() != slog.KindString {
		return a
	}
	s := a.Value.String()
	switch strings.ToLower(a.Key) {
	case "password", "token", "access_token", "refresh_token", "app_secret",
		"page_token", "client_secret", "authorization":
		return slog.String(a.Key, "[REDACTED]")
	}
	s = bearerRe.ReplaceAllString(s, "[REDACTED]")
	s = kenyaIDRe.ReplaceAllString(s, "[REDACTED_ID]")
	s = kenyaPhoneRe.ReplaceAllString(s, "[REDACTED_MSISDN]")
	return slog.String(a.Key, s)
}
