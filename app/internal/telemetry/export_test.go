package telemetry

import (
	"context"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// InstallForTest runs the production pipeline, redaction included, into exporters a test can read.
func InstallForTest(spans sdktrace.SpanExporter, metrics sdkmetric.Reader) (func(context.Context) error, error) {
	return install(sdktrace.WithSyncer(redactSpans(spans)), metrics)
}
