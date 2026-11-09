// Package telemetry is the app's one seam for traces and metrics. S10 fills it in; until then
// spans go to OpenTelemetry's default no-op provider and the metric calls record nothing.
package telemetry

import (
	"context"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/highclip/two-feet/app"

// Setup starts telemetry and returns a function that flushes it on shutdown.
func Setup(context.Context) (shutdown func(context.Context) error, err error) {
	return func(context.Context) error { return nil }, nil
}

// Start opens a span. Spans must never carry an email address or post text beyond 40 chars.
func Start(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationName).Start(ctx, name)
}

// RecordSignup counts one signup.
func RecordSignup(context.Context) {}

// RecordPost counts one post of a kind.
func RecordPost(context.Context, string) {}

// SetSpacesOpen reports how many spaces are open.
func SetSpacesOpen(int) {}
