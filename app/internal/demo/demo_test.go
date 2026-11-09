package demo_test

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/demo"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// Sample rows only, because the e2e tests commit rows of their own to this database.
const (
	samplePosts = `SELECT count(*)::text FROM post p JOIN person a ON a.id = p.author_id WHERE a.display_name LIKE '%(sample)'`
	sampleSpace = `SELECT count(*)::text FROM space sp JOIN person h ON h.id = sp.host_id WHERE h.display_name LIKE '%(sample)'`
)

func count(t *testing.T, s *store.Store, query string) string {
	t.Helper()
	return storetest.QueryString(t, s, query)
}

func collector(t *testing.T, s *store.Store, now time.Time) store.Space {
	t.Helper()
	id := count(t, s, `SELECT sp.id::text FROM space sp JOIN person h ON h.id = sp.host_id
		WHERE sp.title LIKE 'Collector tuning%' AND h.display_name LIKE '%(sample)'`)
	sp, err := s.SpaceByID(context.Background(), id)
	require.NoError(t, err)
	return sp
}

func TestDemoSeedLoadsTheCountsAndMustIncludeRows(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	now := time.Now()

	require.NoError(t, demo.Load(ctx, s, search.Noop{}, quiet(), "cnh", now))

	assert.Equal(t, "12", count(t, s, `SELECT count(*)::text FROM person WHERE display_name LIKE '%(sample)'`))
	assert.Equal(t, "20", count(t, s, samplePosts))
	assert.Equal(t, "6", count(t, s, sampleSpace))
	assert.Equal(t, "1", count(t, s, `SELECT count(*)::text FROM post p JOIN person a ON a.id = p.author_id
		WHERE a.display_name = 'Priya (sample)'
		  AND p.text = 'Rebuilding our OTel collector pipelines with tail sampling, happy to compare notes'`))

	sp := collector(t, s, now)
	assert.Equal(t, "Collector tuning: dropping spans under load", sp.Title)
	assert.Equal(t, "Long table", sp.SpotName, "the demo scan is at Kitchen counter, one minute away")
	assert.Equal(t, 8, sp.Capacity)
	assert.Equal(t, 5, sp.Going)
	assert.WithinDuration(t, now.Add(6*time.Minute), sp.StartsAt, time.Minute)
}

func TestDemoSeedRerunsWithFreshTimesAndNoDuplicates(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	require.NoError(t, demo.Load(ctx, s, search.Noop{}, quiet(), "cnh", time.Now()))

	later := time.Now().Add(2 * time.Hour)
	require.NoError(t, demo.Load(ctx, s, search.Noop{}, quiet(), "cnh", later))

	assert.Equal(t, "12", count(t, s, `SELECT count(*)::text FROM person WHERE display_name LIKE '%(sample)'`))
	assert.Equal(t, "20", count(t, s, samplePosts))
	assert.Equal(t, "6", count(t, s, sampleSpace))
	assert.WithinDuration(t, later.Add(6*time.Minute), collector(t, s, later).StartsAt, time.Minute)
}

func TestDemoSeedLeavesRealPeopleAlone(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	real, err := s.EnsurePerson(ctx, "user_real", store.RoleAttendee, "")
	require.NoError(t, err)
	_, err = s.CreatePost(ctx, real.ID, store.PostStuck, "a real post from a real person", []string{})
	require.NoError(t, err)

	require.NoError(t, demo.Load(ctx, s, search.Noop{}, quiet(), "cnh", time.Now()))
	require.NoError(t, demo.Load(ctx, s, search.Noop{}, quiet(), "cnh", time.Now()))

	assert.Equal(t, "1", count(t, s, `SELECT count(*)::text FROM post WHERE text = 'a real post from a real person'`))
}
