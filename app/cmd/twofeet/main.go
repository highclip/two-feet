// Command twofeet is the Two Feet server. With "migrate" it applies migrations and loads the venue;
// with "seed" it loads the venue only.
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
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/telemetry"
	"github.com/highclip/two-feet/app/internal/web"
)

// version is set at build time: -ldflags "-X main.version=<sha>".
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var err error
	switch command(os.Args) {
	case "migrate":
		err = migrate(ctx)
	case "seed":
		err = seed(ctx)
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

func serve(ctx context.Context, log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	shutdownTelemetry, err := telemetry.Setup(ctx)
	if err != nil {
		return fmt.Errorf("set up telemetry: %w", err)
	}
	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	db := store.New(pool)
	hub := live.NewHub()
	app := web.New(web.Deps{
		Store:     db,
		Hub:       hub,
		Search:    search.Noop{},
		Limiter:   web.NewLimiter(),
		Auth:      auth.New(db, auth.NewClerk(cfg.ClerkSecretKey, cfg.PublicURL), log),
		Log:       log,
		Version:   version,
		EventSlug: cfg.EventSlug,
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
	if err := shutdownTelemetry(shutdownCtx); err != nil {
		log.Error("shutdown telemetry", "err", err)
	}
	return nil
}
