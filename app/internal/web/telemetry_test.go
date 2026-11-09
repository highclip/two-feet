package web_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
)

// TestS10_RealRoutesProduceOneNamedServerSpan drives the real middleware chain as a signed-in
// person. The auth middleware then copies the request, so the outer span can only get its route
// name through telemetry.Route. An anonymous request wouldn't prove that.
func TestS10_RealRoutesProduceOneNamedServerSpan(t *testing.T) {
	spans := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spans))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(provider)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = provider.Shutdown(context.Background())
	})

	f := newFixture(t)
	w := f.get("/e/cnh", as("user_s10"))
	require.Equal(t, http.StatusSeeOther, w.Code, "a new person is sent to onboarding")
	f.get("/healthz")

	got := spans.GetSpans()
	require.Len(t, got, 1, "the probe isn't traced")
	assert.Equal(t, "GET /e/{slug}", got[0].Name)
	assert.Equal(t, trace.SpanKindServer, got[0].SpanKind)
	assert.Contains(t, got[0].Attributes, attribute.String("http.route", "/e/{slug}"))
}
