// Command smoke is a throwaway service that proves the deploy path works end to end before the
// event: image build, Cloudflare tunnel, server-sent events through the tunnel, Clerk sign-in,
// reaching PlanetScale and Elastic Cloud from the cluster, and traces arriving in Dash0.
//
// It is not the app and none of it is reused. Open it on a phone on venue-like wifi and watch the
// live counter: if it ticks steadily and survives backgrounding the tab, SSE works.
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/clerk/clerk-sdk-go/v2"
	clerkhttp "github.com/clerk/clerk-sdk-go/v2/http"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// version is set at build time: -ldflags "-X main.version=<sha>".
var version = "dev"

type config struct {
	port, databaseURL, elasticURL, elasticAPIKey, clerkPK, clerkSK string
}

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 && os.Args[1] == "migrate" {
		log.Info("smoke has no migrations")
		return
	}

	cfg := config{
		port:          env("PORT", "8080"),
		databaseURL:   os.Getenv("DATABASE_URL"),
		elasticURL:    strings.TrimRight(os.Getenv("ELASTIC_URL"), "/"),
		elasticAPIKey: os.Getenv("ELASTIC_API_KEY"),
		clerkPK:       os.Getenv("CLERK_PUBLISHABLE_KEY"),
		clerkSK:       os.Getenv("CLERK_SECRET_KEY"),
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTracing, err := setupTracing(ctx)
	if err != nil {
		log.Error("tracing disabled", "err", err)
	}
	defer shutdownTracing(context.Background())

	if cfg.clerkSK != "" {
		clerk.SetKey(cfg.clerkSK)
	}

	srv := &http.Server{
		Addr:              ":" + cfg.port,
		Handler:           otelhttp.NewHandler(routes(cfg, log), "smoke"),
		ReadHeaderTimeout: 5 * time.Second,
		// No WriteTimeout: it would cut SSE streams. Each handler bounds its own work instead.
	}

	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()

	log.Info("listening", "port", cfg.port, "version", version)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Error("server", "err", err)
		os.Exit(1)
	}
}

func routes(cfg config, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "ok %s\n", version)
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, "ready")
	})

	mux.HandleFunc("GET /api/checks", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		writeJSON(w, map[string]string{
			"version":  version,
			"postgres": checkPostgres(ctx, cfg.databaseURL),
			"elastic":  checkElastic(ctx, cfg.elasticURL, cfg.elasticAPIKey),
			"clerk":    configured(cfg.clerkPK != "" && cfg.clerkSK != ""),
			"tracing":  configured(os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != ""),
		})
	})

	mux.Handle("GET /api/whoami", clerkhttp.RequireHeaderAuthorization()(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, ok := clerk.SessionClaimsFromContext(r.Context())
			if !ok {
				http.Error(w, "no session", http.StatusUnauthorized)
				return
			}
			writeJSON(w, map[string]string{"user": claims.Subject, "session": claims.SessionID})
		})))

	mux.HandleFunc("GET /sse", sse(log))

	page := template.Must(template.New("page").Parse(pageHTML))
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = page.Execute(w, map[string]string{
			"Version":     version,
			"ClerkPK":     cfg.clerkPK,
			"ClerkScript": clerkScriptURL(cfg.clerkPK),
		})
	})
	return mux
}

// sse streams a tick every second and a heartbeat comment every 15 seconds, the same contract
// the app uses (spec.live.flaky_wifi).
func sse(log *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/event-stream")
		h.Set("Cache-Control", "no-cache")
		h.Set("Connection", "keep-alive")
		h.Set("X-Accel-Buffering", "no")
		fmt.Fprint(w, "retry: 2000\n\n")
		flusher.Flush()

		tick := time.NewTicker(time.Second)
		beat := time.NewTicker(15 * time.Second)
		defer tick.Stop()
		defer beat.Stop()
		start, n := time.Now(), 0
		for {
			select {
			case <-r.Context().Done():
				log.Info("sse closed", "ticks", n, "seconds", int(time.Since(start).Seconds()))
				return
			case <-beat.C:
				fmt.Fprint(w, ": heartbeat\n\n")
			case t := <-tick.C:
				n++
				fmt.Fprintf(w, "event: tick\ndata: {\"n\":%d,\"server\":%q,\"version\":%q}\n\n", n, t.UTC().Format(time.RFC3339), version)
			}
			flusher.Flush()
		}
	}
}

func checkPostgres(ctx context.Context, url string) string {
	if url == "" {
		return "not configured"
	}
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		return "error: " + err.Error()
	}
	defer conn.Close(context.Background())
	var v string
	if err := conn.QueryRow(ctx, "select version()").Scan(&v); err != nil {
		return "error: " + err.Error()
	}
	return "ok: " + v
}

func checkElastic(ctx context.Context, url, apiKey string) string {
	if url == "" {
		return "not configured"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url+"/", nil)
	if err != nil {
		return "error: " + err.Error()
	}
	if apiKey != "" {
		req.Header.Set("Authorization", "ApiKey "+apiKey)
	}
	res, err := otelhttp.DefaultClient.Do(req)
	if err != nil {
		return "error: " + err.Error()
	}
	defer res.Body.Close()
	var body struct {
		Version struct {
			Number string `json:"number"`
		} `json:"version"`
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Sprintf("error: HTTP %d", res.StatusCode)
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return "error: " + err.Error()
	}
	return "ok: Elasticsearch " + body.Version.Number
}

// clerkScriptURL derives the Frontend API host from the publishable key
// (pk_test_<base64 of "host$">) and returns the clerk-js script URL served from it.
func clerkScriptURL(pk string) string {
	parts := strings.SplitN(pk, "_", 3)
	if len(parts) != 3 {
		return ""
	}
	raw, err := base64.RawStdEncoding.DecodeString(strings.TrimRight(parts[2], "="))
	if err != nil {
		return ""
	}
	host := strings.TrimSuffix(string(raw), "$")
	if host == "" || strings.ContainsAny(host, "/ \"'<>") {
		return ""
	}
	return "https://" + host + "/npm/@clerk/clerk-js@5/dist/clerk.browser.js"
}

func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return noop, nil
	}
	// Reads OTEL_EXPORTER_OTLP_ENDPOINT, _HEADERS and _INSECURE from the environment.
	exp, err := otlptracegrpc.New(ctx)
	if err != nil {
		return noop, err
	}
	// The default resource reads OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES.
	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp))
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

func configured(ok bool) string {
	if ok {
		return "configured"
	}
	return "not configured"
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
