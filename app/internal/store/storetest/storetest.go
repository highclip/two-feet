// Package storetest gives store and handler tests a migrated database and a rolled-back transaction.
package storetest

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
)

// databaseLockID lets one test at a time use the database. go test runs packages in parallel, and
// a test that truncates tables would otherwise deadlock with another package's open transaction.
const databaseLockID = 7202611

// connections remembers what each test Store runs on, for Exec and QueryString.
var connections sync.Map

// migrate takes the database for this test, until it ends, and applies the migrations. A test that
// holds the database mustn't start a subtest that asks for it again.
func migrate(t *testing.T, ctx context.Context) string {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, url, "DATABASE_URL must point at the local Postgres")

	lock, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Close(ctx) }) // closing the connection releases the lock
	_, err = lock.Exec(ctx, "SELECT pg_advisory_lock($1)", databaseLockID)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx, url))
	return url
}

// Tx returns a Store on a transaction that is rolled back when the test ends. It needs the local
// Postgres from infra/local/compose.yaml and DATABASE_URL from the environment.
func Tx(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	url := migrate(t, ctx)

	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	s := store.New(tx)
	connections.Store(s, store.DB(tx))
	t.Cleanup(func() { connections.Delete(s) })
	return s
}

// Pool returns a Store on real connections, for a test about concurrent transactions. What it
// writes is committed, so the test must use its own event and delete what it made.
func Pool(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	url := migrate(t, ctx)

	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	s := store.New(pool)
	connections.Store(s, store.DB(pool))
	t.Cleanup(func() { connections.Delete(s) })
	return s
}

func connection(t *testing.T, s *store.Store) store.DB {
	t.Helper()
	db, ok := connections.Load(s)
	require.True(t, ok, "that Store didn't come from storetest")
	return db.(store.DB)
}

// Exec runs a statement for test setup that no store function covers yet.
func Exec(t *testing.T, s *store.Store, sql string, args ...any) {
	t.Helper()
	_, err := connection(t, s).Exec(context.Background(), sql, args...)
	require.NoError(t, err)
}

// QueryString returns the single text value a query selects.
func QueryString(t *testing.T, s *store.Store, sql string, args ...any) string {
	t.Helper()
	var out string
	require.NoError(t, connection(t, s).QueryRow(context.Background(), sql, args...).Scan(&out))
	return out
}
