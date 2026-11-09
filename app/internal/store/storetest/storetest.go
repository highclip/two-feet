// Package storetest gives store and handler tests a migrated database and a rolled-back transaction.
package storetest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
)

// migrateLockID serializes migrations between test packages, which go test runs in parallel.
const migrateLockID = 7202611

// Tx returns a Store on a transaction that is rolled back when the test ends. It needs the local
// Postgres from infra/local/compose.yaml and DATABASE_URL from the environment.
func Tx(t *testing.T) *store.Store {
	t.Helper()
	ctx := context.Background()
	url := os.Getenv("DATABASE_URL")
	require.NotEmpty(t, url, "DATABASE_URL must point at the local Postgres")

	lock, err := pgx.Connect(ctx, url)
	require.NoError(t, err)
	defer func() { _ = lock.Close(ctx) }()
	_, err = lock.Exec(ctx, "SELECT pg_advisory_lock($1)", migrateLockID)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx, url))

	pool, err := pgxpool.New(ctx, url)
	require.NoError(t, err)
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback(ctx) })
	return store.New(tx)
}
