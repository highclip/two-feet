package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"

	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// maxValueChars is the spec's limit on post text in telemetry. Nothing downstream can tell post text
// from other strings, so every value is held to it except the keys in keepWhole.
const maxValueChars = 40

// keepWhole lists values that are long by nature and hold no post text. Emails are still removed.
var keepWhole = map[string]bool{
	"db.query.text":       true, // parameterized, so it never holds a value
	"url.path":            true, // slugs only
	"http.route":          true,
	"user_agent.original": true,
	"err":                 true, // wrapped errors name ids, never user text (CLAUDE.md)
	"path":                true,
}

// emailAddress also matches the percent-encoded form, which reaches spans through URLs.
var emailAddress = regexp.MustCompile(`(?i)[a-z0-9._%+\-]+(?:@|%40)[a-z0-9.\-]+\.[a-z]{2,}`)

// scrub removes email addresses. It has to run before clip, or a cut could leave part of one.
func scrub(s string) string {
	return emailAddress.ReplaceAllString(s, "[email]")
}

// clip cuts s to maxValueChars characters.
func clip(s string) string {
	seen := 0
	for i := range s {
		if seen == maxValueChars {
			return s[:i]
		}
		seen++
	}
	return s
}

func safeValue(key, value string) string {
	value = scrub(value)
	if keepWhole[key] {
		return value
	}
	return clip(value)
}

func safeAttributes(attrs []attribute.KeyValue) []attribute.KeyValue {
	out := make([]attribute.KeyValue, len(attrs))
	for i, a := range attrs {
		key := string(a.Key)
		switch a.Value.Type() {
		case attribute.STRING:
			a = attribute.String(key, safeValue(key, a.Value.AsString()))
		case attribute.STRINGSLICE:
			items := a.Value.AsStringSlice()
			for j := range items {
				items[j] = safeValue(key, items[j])
			}
			a = attribute.StringSlice(key, items)
		}
		out[i] = a
	}
	return out
}

// redactSpans applies the rules at the exporter, so they hold for every span from any source,
// including the instrumentation libraries.
func redactSpans(next sdktrace.SpanExporter) sdktrace.SpanExporter {
	return spanRedactor{next: next}
}

type spanRedactor struct {
	next sdktrace.SpanExporter
}

func (r spanRedactor) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	clean := make([]sdktrace.ReadOnlySpan, len(spans))
	for i, s := range spans {
		clean[i] = redactedSpan{s}
	}
	return r.next.ExportSpans(ctx, clean)
}

func (r spanRedactor) Shutdown(ctx context.Context) error {
	return r.next.Shutdown(ctx)
}

// redactedSpan overrides every part of a span that can hold text.
type redactedSpan struct {
	sdktrace.ReadOnlySpan
}

func (s redactedSpan) Name() string {
	return scrub(s.ReadOnlySpan.Name())
}

func (s redactedSpan) Attributes() []attribute.KeyValue {
	return safeAttributes(s.ReadOnlySpan.Attributes())
}

func (s redactedSpan) Events() []sdktrace.Event {
	events := append([]sdktrace.Event(nil), s.ReadOnlySpan.Events()...)
	for i := range events {
		events[i].Name = scrub(events[i].Name)
		events[i].Attributes = safeAttributes(events[i].Attributes)
	}
	return events
}

func (s redactedSpan) Links() []sdktrace.Link {
	links := append([]sdktrace.Link(nil), s.ReadOnlySpan.Links()...)
	for i := range links {
		links[i].Attributes = safeAttributes(links[i].Attributes)
	}
	return links
}

func (s redactedSpan) Status() sdktrace.Status {
	status := s.ReadOnlySpan.Status()
	status.Description = safeValue("", status.Description)
	return status
}

// RedactLogs wraps a log handler with the same rules. The app's stdout logs reach Dash0 through the
// operator, so this is the only place they can be cleaned.
func RedactLogs(next slog.Handler) slog.Handler {
	return logRedactor{next: next}
}

type logRedactor struct {
	next slog.Handler
}

func (h logRedactor) Enabled(ctx context.Context, level slog.Level) bool {
	return h.next.Enabled(ctx, level)
}

func (h logRedactor) Handle(ctx context.Context, record slog.Record) error {
	clean := slog.NewRecord(record.Time, record.Level, scrub(record.Message), record.PC)
	record.Attrs(func(a slog.Attr) bool {
		clean.AddAttrs(safeLogAttr(a))
		return true
	})
	return h.next.Handle(ctx, clean)
}

func (h logRedactor) WithAttrs(attrs []slog.Attr) slog.Handler {
	return logRedactor{next: h.next.WithAttrs(safeLogAttrs(attrs))}
}

func (h logRedactor) WithGroup(name string) slog.Handler {
	return logRedactor{next: h.next.WithGroup(name)}
}

func safeLogAttrs(attrs []slog.Attr) []slog.Attr {
	out := make([]slog.Attr, len(attrs))
	for i, a := range attrs {
		out[i] = safeLogAttr(a)
	}
	return out
}

func safeLogAttr(a slog.Attr) slog.Attr {
	value := a.Value.Resolve()
	switch value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, safeValue(a.Key, value.String()))
	case slog.KindAny:
		return slog.String(a.Key, safeValue(a.Key, fmt.Sprint(value.Any())))
	case slog.KindGroup:
		return slog.Attr{Key: a.Key, Value: slog.GroupValue(safeLogAttrs(value.Group())...)}
	default:
		return slog.Attr{Key: a.Key, Value: value}
	}
}
