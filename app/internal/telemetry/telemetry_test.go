package telemetry_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	"github.com/highclip/two-feet/app/internal/telemetry"
)

const (
	email = "priya+clerk_test@example.com"
	// postText is longer than any span value may carry; its first 41 chars must never appear.
	postText = "our OTel collector drops spans under load on k8s and we cannot find out why at all"
)

type pipeline struct {
	spans   *tracetest.InMemoryExporter
	metrics *sdkmetric.ManualReader
}

func install(t *testing.T) pipeline {
	t.Helper()
	p := pipeline{spans: tracetest.NewInMemoryExporter(), metrics: sdkmetric.NewManualReader()}
	shutdown, err := telemetry.InstallForTest(p.spans, p.metrics)
	require.NoError(t, err)
	t.Cleanup(func() { _ = shutdown(context.Background()) })
	return p
}

func tracedPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, databaseURL, "DATABASE_URL must point at the local Postgres")
	pool, err := telemetry.NewPool(context.Background(), databaseURL)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	return pool
}

// copiesRequest does what the auth middleware does: it hands the next handler a new *http.Request,
// so whatever the mux records on its request isn't visible to the outer ones.
func copiesRequest(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(context.WithoutCancel(r.Context())))
	})
}

func byName(t *testing.T, spans tracetest.SpanStubs, name string) tracetest.SpanStub {
	t.Helper()
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		if s.Name == name {
			return s
		}
		names = append(names, s.Name)
	}
	require.Failf(t, "span not found", "no span named %q in %v", name, names)
	return tracetest.SpanStub{}
}

func TestS10_MatchRequestTraceShowsHandlerElasticsearchAndPostgresAsChildSpans(t *testing.T) {
	p := install(t)
	pool := tracedPool(t)
	elastic := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"hits":{"hits":[]}}`)
	}))
	defer elastic.Close()
	client := &http.Client{Transport: telemetry.ElasticTransport(http.DefaultTransport)}

	// S6's handler doesn't exist yet (D4), so this one does what it will: Elasticsearch, then Postgres.
	mux := http.NewServeMux()
	mux.HandleFunc("POST /e/{slug}/match", func(w http.ResponseWriter, r *http.Request) {
		ctx, span := telemetry.Start(r.Context(), "match")
		defer span.End()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, elastic.URL+"/twofeet/_search", strings.NewReader(`{}`))
		require.NoError(t, err)
		res, err := client.Do(req)
		require.NoError(t, err)
		require.NoError(t, res.Body.Close())
		var one int
		require.NoError(t, pool.QueryRow(ctx, "SELECT 1").Scan(&one))
		w.WriteHeader(http.StatusOK)
	})
	handler := telemetry.HTTP(copiesRequest(telemetry.Route(mux)))

	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/e/cnh/match", nil))
	require.Equal(t, http.StatusOK, w.Code)

	spans := p.spans.GetSpans()
	root := byName(t, spans, "POST /e/{slug}/match")
	match := byName(t, spans, "match")
	search := byName(t, spans, "elasticsearch _search")
	reads := byName(t, spans, "postgres SELECT")

	assert.False(t, root.Parent.IsValid(), "the handler span is the root of the trace")
	assert.Equal(t, trace.SpanKindServer, root.SpanKind)
	for _, s := range []tracetest.SpanStub{match, search, reads} {
		assert.Equal(t, root.SpanContext.TraceID(), s.SpanContext.TraceID(), "%s is in the same trace", s.Name)
	}
	assert.Equal(t, root.SpanContext.SpanID(), match.Parent.SpanID())
	assert.Equal(t, match.SpanContext.SpanID(), search.Parent.SpanID())
	assert.Equal(t, match.SpanContext.SpanID(), reads.Parent.SpanID())
	assert.Contains(t, search.Attributes, attribute.String("db.system.name", "elasticsearch"))
	assert.Contains(t, reads.Attributes, attribute.String("db.system.name", "postgresql"))
	assert.Contains(t, root.Attributes, attribute.String("http.route", "/e/{slug}/match"))
}

func TestS10_PostgresOutsideARequestStartsNoTrace(t *testing.T) {
	p := install(t)
	pool := tracedPool(t)
	require.NoError(t, pool.Ping(context.Background()))
	assert.Empty(t, p.spans.GetSpans(), "background queries and probes would each be their own trace")
}

func TestS10_ProbesStaticFilesAndTheLiveStreamAreNotTraced(t *testing.T) {
	p := install(t)
	handler := telemetry.HTTP(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	for _, path := range []string{"/healthz", "/readyz", "/static/app.css", "/e/cnh/live?topics=event"} {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}
	assert.Empty(t, p.spans.GetSpans())

	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/e/cnh", nil))
	assert.Len(t, p.spans.GetSpans(), 1)
}

func collect(t *testing.T, p pipeline) map[string]metricdata.Metrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	require.NoError(t, p.metrics.Collect(context.Background(), &rm))
	found := map[string]metricdata.Metrics{}
	for _, scope := range rm.ScopeMetrics {
		for _, m := range scope.Metrics {
			found[m.Name] = m
		}
	}
	return found
}

func postsByKind(t *testing.T, m metricdata.Metrics) map[string]int64 {
	t.Helper()
	sum, ok := m.Data.(metricdata.Sum[int64])
	require.True(t, ok, "posts_total is a counter")
	counts := map[string]int64{}
	for _, dp := range sum.DataPoints {
		kind, ok := dp.Attributes.Value("kind")
		require.True(t, ok, "posts_total carries kind")
		counts[kind.AsString()] += dp.Value
	}
	return counts
}

func TestS10_MetricsReportSpacesOpenSignupsAndPostsByKind(t *testing.T) {
	p := install(t)
	ctx := context.Background()

	telemetry.RecordSignup(ctx)
	telemetry.RecordSignup(ctx)
	telemetry.RecordPost(ctx, "stuck")
	telemetry.RecordPost(ctx, "stuck")
	telemetry.RecordPost(ctx, "curious")
	telemetry.RecordPost(ctx, "offer")
	telemetry.SetSpacesOpen(3)
	telemetry.SetSpacesOpen(5)

	metrics := collect(t, p)

	signups, ok := metrics["signups_total"].Data.(metricdata.Sum[int64])
	require.True(t, ok, "signups_total is a counter")
	require.Len(t, signups.DataPoints, 1)
	assert.Equal(t, int64(2), signups.DataPoints[0].Value)
	assert.True(t, signups.IsMonotonic)

	assert.Equal(t, map[string]int64{"stuck": 2, "curious": 1, "offer": 1}, postsByKind(t, metrics["posts_total"]))

	spaces, ok := metrics["spaces_open"].Data.(metricdata.Gauge[int64])
	require.True(t, ok, "spaces_open is a gauge")
	require.Len(t, spaces.DataPoints, 1)
	assert.Equal(t, int64(5), spaces.DataPoints[0].Value)
}

func TestS10_PostKindOutsideTheSpecCannotBecomeAMetricLabel(t *testing.T) {
	p := install(t)
	telemetry.RecordPost(context.Background(), postText)
	assert.Equal(t, map[string]int64{"other": 1}, postsByKind(t, collect(t, p)["posts_total"]))
}

func TestS10_SpacesOpenIsReportedWhileTheAppRuns(t *testing.T) {
	p := install(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	counted := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		telemetry.ReportSpacesOpen(ctx, func(context.Context) (int, error) {
			select {
			case counted <- struct{}{}:
			default:
			}
			return 4, nil
		}, slog.New(slog.DiscardHandler))
	}()

	select {
	case <-counted:
	case <-time.After(5 * time.Second):
		require.Fail(t, "spaces were never counted")
	}
	require.Eventually(t, func() bool {
		gauge, ok := collect(t, p)["spaces_open"].Data.(metricdata.Gauge[int64])
		return ok && len(gauge.DataPoints) == 1 && gauge.DataPoints[0].Value == 4
	}, 5*time.Second, 10*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		require.Fail(t, "reporter did not stop with its context")
	}
}

// values lists every string a span would send, so a leak anywhere fails the test.
func values(spans tracetest.SpanStubs) []string {
	var out []string
	add := func(attrs []attribute.KeyValue) {
		for _, a := range attrs {
			switch a.Value.Type() {
			case attribute.STRING:
				out = append(out, a.Value.AsString())
			case attribute.STRINGSLICE:
				out = append(out, a.Value.AsStringSlice()...)
			}
		}
	}
	for _, s := range spans {
		out = append(out, s.Name, s.Status.Description)
		add(s.Attributes)
		for _, e := range s.Events {
			out = append(out, e.Name)
			add(e.Attributes)
		}
		for _, l := range s.Links {
			add(l.Attributes)
		}
	}
	return out
}

func attr(t *testing.T, s tracetest.SpanStub, key attribute.Key) string {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key == key {
			return a.Value.AsString()
		}
	}
	require.Failf(t, "attribute not found", "%s has no %s", s.Name, key)
	return ""
}

func assertNothingPrivate(t *testing.T, text string) {
	t.Helper()
	assert.NotContains(t, text, email)
	assert.NotContains(t, text, "example.com", "not even part of an address")
	assert.NotContains(t, text, postText[:41])
}

func TestS10_SpansAndLogsCarryNoEmailAndNoPostTextBeyond40Chars(t *testing.T) {
	t.Run("attributes, events and status", func(t *testing.T) {
		p := install(t)
		_, span := telemetry.Start(context.Background(), "signup")
		span.SetAttributes(
			attribute.String("post.text", postText),
			attribute.String("person.email", email),
			attribute.StringSlice("post.lines", []string{postText, "reach me at " + email}),
		)
		span.AddEvent("posted", trace.WithAttributes(attribute.String("post.text", postText)))
		span.AddLink(trace.Link{Attributes: []attribute.KeyValue{attribute.String("from", email)}})
		span.RecordError(errors.New("could not notify " + email + " about: " + postText))
		span.SetStatus(codes.Error, "failed for "+email)
		span.End()

		got := values(p.spans.GetSpans())
		require.NotEmpty(t, got)
		for _, v := range got {
			assertNothingPrivate(t, v)
		}
		stub := p.spans.GetSpans()[0]
		text := attr(t, stub, "post.text")
		assert.Equal(t, 40, utf8.RuneCountInString(text), "a post's opening words are still useful")
		assert.Equal(t, postText[:40], text)
	})

	t.Run("an email is removed before text is cut, so no fragment survives", func(t *testing.T) {
		p := install(t)
		_, span := telemetry.Start(context.Background(), "signup")
		span.SetAttributes(attribute.String("note", strings.Repeat("x", 30)+" "+email))
		span.End()
		for _, v := range values(p.spans.GetSpans()) {
			assert.NotContains(t, v, "priya")
		}
	})

	t.Run("cutting text never splits a character", func(t *testing.T) {
		p := install(t)
		_, span := telemetry.Start(context.Background(), "signup")
		span.SetAttributes(attribute.String("note", strings.Repeat("é", 60)))
		span.End()
		assert.Equal(t, strings.Repeat("é", 40), attr(t, p.spans.GetSpans()[0], "note"))
	})

	t.Run("http request", func(t *testing.T) {
		p := install(t)
		mux := http.NewServeMux()
		mux.HandleFunc("GET /e/{slug}/search", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
		handler := telemetry.HTTP(copiesRequest(telemetry.Route(mux)))

		query := url.Values{"q": {postText}, "email": {email}}
		r := httptest.NewRequest(http.MethodGet, "/e/cnh/search?"+query.Encode(), nil)
		r.Host = "twofeet.test" // httptest's default host is the domain of the test address
		r.Header.Set("User-Agent", "phone "+email)
		handler.ServeHTTP(httptest.NewRecorder(), r)
		unrouted := httptest.NewRequest(http.MethodGet, "/"+url.PathEscape(email)+"?"+query.Encode(), nil)
		unrouted.Host = "twofeet.test"
		handler.ServeHTTP(httptest.NewRecorder(), unrouted)

		got := values(p.spans.GetSpans())
		require.NotEmpty(t, got)
		for _, v := range got {
			assertNothingPrivate(t, v)
			assert.NotContains(t, v, strings.ReplaceAll(email, "@", "%40"))
			assert.NotContains(t, v, "priya", "an unrouted path must not name the span either")
		}
	})

	t.Run("sql arguments", func(t *testing.T) {
		p := install(t)
		pool := tracedPool(t)
		ctx, span := telemetry.Start(context.Background(), "signup")
		var a, b string
		require.NoError(t, pool.QueryRow(ctx, "SELECT $1::text, $2::text", email, postText).Scan(&a, &b))
		span.End()

		got := values(p.spans.GetSpans())
		for _, v := range got {
			assertNothingPrivate(t, v)
		}
		assert.Contains(t, strings.Join(got, "\n"), "SELECT $1::text, $2::text", "parameterized SQL is safe to keep whole")
	})

	t.Run("logs", func(t *testing.T) {
		var out bytes.Buffer
		log := slog.New(telemetry.RedactLogs(slog.NewJSONHandler(&out, nil)))
		log = log.With("owner", email).WithGroup("request").With("note", postText)

		log.Error("could not reach "+email,
			"err", fmt.Errorf("notify %s: %w", email, errors.New("timed out")),
			"post", postText,
			slog.Group("author", "email", email, "text", postText),
			"count", 3,
		)

		text := out.String()
		assertNothingPrivate(t, text)
		assert.Contains(t, text, `"post":"`+postText[:40]+`"`)
		assert.Contains(t, text, "timed out", "errors stay readable")
		assert.Contains(t, text, `"count":3`, "numbers are untouched")
	})

	t.Run("a logged value that prints itself is checked too", func(t *testing.T) {
		var out bytes.Buffer
		log := slog.New(telemetry.RedactLogs(slog.NewJSONHandler(&out, nil)))
		log.Info("loaded", "person", struct{ Email, Text string }{email, postText})
		assertNothingPrivate(t, out.String())
	})
}

func TestS10_WithoutAnEndpointTelemetryStaysOffAndSetupSucceeds(t *testing.T) {
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "")
	shutdown, err := telemetry.Setup(context.Background())
	require.NoError(t, err)
	require.NoError(t, shutdown(context.Background()))
}
