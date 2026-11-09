// Command twofeet is the Two Feet server. With "migrate" it applies migrations and loads the venue;
// with "seed" it loads the venue only; "seed-demo" also loads the sample demo data and "clear-demo"
// removes it.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	_ "time/tzdata" // America/Denver, whatever the image ships

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/config"
	"github.com/highclip/two-feet/app/internal/demo"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/telemetry"
	"github.com/highclip/two-feet/app/internal/web"
)

// version is set at build time: -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	log := slog.New(telemetry.RedactLogs(slog.NewJSONHandler(os.Stdout, nil)))
	slog.SetDefault(log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch command(os.Args) {
	case "migrate":
		err = migrate(ctx)
	case "seed":
		err = seed(ctx)
	case "seed-demo":
		err = seedDemo(ctx, log)
	case "clear-demo":
		err = clearDemo(ctx)
	default:
		err = serve(ctx, log)
	}
	if err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

func command(args []string) string {
	if len(args) > 1 {
		return args[1]
	}
	return ""
}

// migrate is what the Helm pre-upgrade job runs. It loads the venue too, so every deploy applies
// edits to the seed and a fresh database is ready to serve.
func migrate(ctx context.Context) error {
	databaseURL, _, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	if err := store.Migrate(ctx, databaseURL); err != nil {
		return err
	}
	return seed(ctx)
}

func seed(ctx context.Context) error {
	databaseURL, eventSlug, err := config.DatabaseURL()
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	return store.New(pool).Seed(ctx, eventSlug)
}

// seedDemo loads the sample data just before presenting. It runs through the same queue as the
// server, so a slow ELSER gets the same retries, and reports a lost write instead of a quiet gap.
func seedDemo(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := seed(ctx); err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	elastic, err := search.NewClient(search.Config{URL: cfg.ElasticURL, APIKey: cfg.ElasticAPIKey, Index: cfg.ElasticIndexPrefix})
	if err != nil {
		return err
	}
	if err := elastic.Ready(ctx); err != nil {
		return fmt.Errorf("elasticsearch isn't ready: %w", err)
	}
	indexer := search.NewQueue(elastic, log, search.QueueOptions{})
	defer indexer.Close(ctx)
	if err := demo.Load(ctx, store.New(pool), indexer, log, cfg.EventSlug, time.Now()); err != nil {
		return err
	}
	if indexer.Failed() {
		return errors.New("some sample documents didn't reach Elasticsearch; run seed-demo again")
	}
	return nil
}

// clearDemo deletes the sample people and everything they made. Their documents stay in the index
// until the next rebuild, and matching drops them meanwhile because their rows are gone.
func clearDemo(ctx context.Context) error {
	databaseURL, err := config.DatabaseOnly()
	if err != nil {
		return err
	}
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()
	return store.New(pool).ClearSamples(ctx)
}

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	shutdownTelemetry, err := telemetry.Setup(ctx)
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	pool, err := telemetry.NewPool(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	db := store.New(pool)
	go telemetry.ReportSpacesOpen(ctx, db.CountOpenSpaces, log)
	hub := live.NewHub()
	elastic, err := search.NewClient(search.Config{
		URL: cfg.ElasticURL, APIKey: cfg.ElasticAPIKey, Index: cfg.ElasticIndexPrefix,
		Transport: telemetry.ElasticTransport(http.DefaultTransport),
	})
	if err != nil {
		return err
	}
	indexer := search.NewQueue(elastic, log, search.QueueOptions{})
	go search.NewSync(db, indexer, log).Run(ctx, elastic.Ready, indexer)
	app := web.New(web.Deps{
		Store:     db,
		Hub:       hub,
		Search:    indexer,
		Finder:    elastic,
		Tags:      elastic,
		Limiter:   web.NewLimiter(),
		Auth:      auth.New(db, auth.NewClerk(cfg.ClerkSecretKey, cfg.PublicURL), log),
		Log:       log,
		Version:   version,
		EventSlug: cfg.EventSlug,
		PublicURL: cfg.PublicURL,
		ClerkKey:  cfg.ClerkPublishableKey,
		Ready:     pool.Ping,
	})
	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           app.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       2 * time.Minute,
		// No WriteTimeout: it would cut SSE streams. liveStream sets a deadline per write instead.
	}
	srv.RegisterOnShutdown(hub.Close)

	log.Info("listening", "addr", cfg.Addr, "version", version)
	failed := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			failed <- err
		}
	}()

	select {
	case err := <-failed:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}
	// ctx is already cancelled, so shutting down needs a deadline of its own.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
	indexer.Close(shutdownCtx)
	if err := shutdownTelemetry(shutdownCtx); err != nil {
		log.Error("shutdown telemetry", "err", err)
	}
	return nil
}
