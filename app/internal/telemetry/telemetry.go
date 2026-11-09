// Package telemetry is the app's one seam for traces and metrics. Everything leaves through OTLP,
// configured by the standard OTEL_* variables, and passes the redaction in redact.go first.
package telemetry

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	instrumentationName = "github.com/highclip/two-feet/app"
	spacesOpenEvery     = 30 * time.Second
)

// instruments stays nil until Setup runs with an endpoint, and the Record functions then do nothing.
type instruments struct {
	signups    metric.Int64Counter
	posts      metric.Int64Counter
	spacesOpen metric.Int64Gauge
}

var current atomic.Pointer[instruments]

// Setup starts telemetry and returns a function that flushes it on shutdown. With no
// OTEL_EXPORTER_OTLP_ENDPOINT, telemetry stays off. The exporters read that variable, _HEADERS and
// _INSECURE themselves, and the default resource reads OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
//
// No propagator is set on purpose: requests come from the public internet, so a caller's traceparent
// must not choose our trace ids or sampling.
func Setup(ctx context.Context) (shutdown func(context.Context) error, err error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		slog.Warn("telemetry export", "err", err)
	}))
	spans, err := otlptracegrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create trace exporter: %w", err)
	}
	metrics, err := otlpmetricgrpc.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create metric exporter: %w", err)
	}
	return install(sdktrace.WithBatcher(redactSpans(spans)), sdkmetric.NewPeriodicReader(metrics))
}

// install makes a span pipeline and metric reader the app's own.
func install(spans sdktrace.TracerProviderOption, reader sdkmetric.Reader) (func(context.Context) error, error) {
	tracers := sdktrace.NewTracerProvider(spans)
	meters := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))

	meter := meters.Meter(instrumentationName)
	signups, err := meter.Int64Counter("signups_total", metric.WithDescription("Seats taken in spaces"))
	if err != nil {
		return nil, fmt.Errorf("create signups_total: %w", err)
	}
	posts, err := meter.Int64Counter("posts_total", metric.WithDescription("Posts written, by kind"))
	if err != nil {
		return nil, fmt.Errorf("create posts_total: %w", err)
	}
	spacesOpen, err := meter.Int64Gauge("spaces_open", metric.WithDescription("Spaces not cancelled and not yet over"))
	if err != nil {
		return nil, fmt.Errorf("create spaces_open: %w", err)
	}

	otel.SetTracerProvider(tracers)
	otel.SetMeterProvider(meters)
	current.Store(&instruments{signups: signups, posts: posts, spacesOpen: spacesOpen})
	return func(ctx context.Context) error {
		return errors.Join(tracers.Shutdown(ctx), meters.Shutdown(ctx))
	}, nil
}

// Start opens a span. Name it with a constant, never with user text. Attributes are redacted on the
// way out, but don't rely on it: spans must never carry an email address or post text beyond 40 chars.
func Start(ctx context.Context, name string) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationName).Start(ctx, name)
}

// HTTP opens the server span for each request. It goes outermost, with Route just around the mux.
// Probes and static files would only be noise, and a live stream would be a span hours long.
func HTTP(next http.Handler) http.Handler {
	return otelhttp.NewHandler(next, "http request", otelhttp.WithFilter(worthTracing))
}

func worthTracing(r *http.Request) bool {
	path := r.URL.Path
	return path != "/healthz" && path != "/readyz" && !strings.HasPrefix(path, "/static/") && !strings.HasSuffix(path, "/live")
}

// Route names the request's span after the pattern that matched, like "GET /e/{slug}", so a trace
// groups by route and never shows a slug in its name. It reads the pattern after serving because
// the mux records it on the request it's given, which is the only request that ever has it.
func Route(mux http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r)
		if r.Pattern == "" {
			return
		}
		span := trace.SpanFromContext(r.Context())
		span.SetName(r.Pattern)
		if _, route, found := strings.Cut(r.Pattern, " "); found {
			span.SetAttributes(attribute.String("http.route", route))
		}
	})
}

// ElasticTransport traces calls to Elasticsearch as child spans. Pass it as the client's Transport.
// Request bodies, which hold the text being matched, are never recorded.
func ElasticTransport(base http.RoundTripper) http.RoundTripper {
	return otelhttp.NewTransport(base,
		otelhttp.WithSpanNameFormatter(elasticSpanName),
		otelhttp.WithSpanOptions(trace.WithAttributes(attribute.String("db.system.name", "elasticsearch"))),
	)
}

// elasticSpanName uses the API's own path segment, such as _search or _doc, and skips ids.
func elasticSpanName(_ string, r *http.Request) string {
	for segment := range strings.SplitSeq(r.URL.Path, "/") {
		if strings.HasPrefix(segment, "_") {
			return "elasticsearch " + segment
		}
	}
	return "elasticsearch " + r.Method
}

// RecordSignup counts one signup.
func RecordSignup(ctx context.Context) {
	if i := current.Load(); i != nil {
		i.signups.Add(ctx, 1)
	}
}

// RecordPost counts one post of a kind. Only the spec's kinds become labels, so a bad value can't
// put user text in a metric or grow the number of series.
func RecordPost(ctx context.Context, kind string) {
	i := current.Load()
	if i == nil {
		return
	}
	switch kind {
	case "stuck", "curious", "offer":
	default:
		kind = "other"
	}
	i.posts.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind)))
}

// SetSpacesOpen reports how many spaces are open.
func SetSpacesOpen(n int) {
	if i := current.Load(); i != nil {
		i.spacesOpen.Record(context.Background(), int64(n))
	}
}

// ReportSpacesOpen keeps spaces_open current until ctx ends. It polls because a space stops being
// open when its time runs out, and no request happens at that moment.
func ReportSpacesOpen(ctx context.Context, count func(context.Context) (int, error), log *slog.Logger) {
	report := func() {
		n, err := count(ctx)
		if err != nil {
			if ctx.Err() == nil {
				log.Error("count open spaces", "err", err)
			}
			return
		}
		SetSpacesOpen(n)
	}
	report()
	ticker := time.NewTicker(spacesOpenEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			report()
		}
	}
}
