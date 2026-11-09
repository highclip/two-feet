// Package store is the Postgres access layer. All SQL lives here, in one file per area.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	_ "github.com/jackc/pgx/v5/stdlib" // database/sql driver, for goose
	"github.com/pressly/goose/v3"
)

//go:embed migrations/*.sql
var migrations embed.FS

// ErrNotFound means the row asked for doesn't exist.
var ErrNotFound = errors.New("not found")

// DB is satisfied by a pool and by a transaction, so store code runs unchanged inside a test's
// rolled-back transaction. Begin on a transaction makes a savepoint, so pgx.BeginFunc nests.
type DB interface {
	Begin(ctx context.Context) (pgx.Tx, error)
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Store runs every query the app makes.
type Store struct {
	db DB
}

// New returns a Store on a pool or a transaction.
func New(db DB) *Store {
	return &Store{db: db}
}

// Migrate applies the embedded goose migrations. Slices add migrations in their own number ranges
// and merge in any order, so older numbers that arrive late are applied too.
func Migrate(ctx context.Context, databaseURL string) error {
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	dir, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return fmt.Errorf("open migrations: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, dir, goose.WithAllowOutofOrder(true))
	if err != nil {
		return fmt.Errorf("load migrations: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}
	return nil
}
