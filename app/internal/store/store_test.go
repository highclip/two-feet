package store_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

func emptyVenue(t *testing.T, s *store.Store) {
	t.Helper()
	require.NoError(t, s.ExecForTest(context.Background(),
		`TRUNCATE signup, space, post, person, sponsor, spot, zone_walk, zone, event CASCADE`))
}

func count(t *testing.T, s *store.Store, table string) int {
	t.Helper()
	n, err := s.CountForTest(context.Background(), table)
	require.NoError(t, err)
	return n
}

func TestS1_SeedRunTwiceLeavesEverythingExactlyOnce(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)

	require.NoError(t, s.Seed(ctx, "cnh"))
	first, err := s.SpotQRSlugs(ctx)
	require.NoError(t, err)
	require.NoError(t, s.Seed(ctx, "cnh"))
	second, err := s.SpotQRSlugs(ctx)
	require.NoError(t, err)

	assert.Equal(t, 1, count(t, s, "event"))
	assert.Equal(t, 4, count(t, s, "zone"))
	assert.Equal(t, 6, count(t, s, "zone_walk"))
	assert.Equal(t, 11, count(t, s, "spot"))
	assert.Equal(t, 6, count(t, s, "sponsor"))
	assert.Equal(t, first, second, "a re-run keeps every printed QR slug")
	for name, slug := range first {
		assert.GreaterOrEqual(t, len(slug), 16, "qr_slug for %s", name)
	}
}

func TestSeedAppliesVenueChangesButKeepsSponsorEdits(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	before, err := s.SpotQRSlugs(ctx)
	require.NoError(t, err)

	require.NoError(t, s.ExecForTest(ctx, `UPDATE spot SET "where" = 'old note', zone_id = 'cafe' WHERE name = 'Corner couches'`))
	require.NoError(t, s.ExecForTest(ctx, `UPDATE sponsor SET solves = 'edited by the rep' WHERE name = 'Dash0'`))
	require.NoError(t, s.Seed(ctx, "cnh"))

	spot, err := s.SpotByQRSlug(ctx, before["Corner couches"])
	require.NoError(t, err)
	assert.Equal(t, "lounge", spot.ZoneID)
	assert.NotEqual(t, "old note", spot.Where)

	sponsors, err := s.Sponsors(ctx)
	require.NoError(t, err)
	for _, sp := range sponsors {
		if sp.Name == "Dash0" {
			assert.Equal(t, "edited by the rep", sp.Solves)
		}
		assert.NotNil(t, sp.BoothSpotID, "the seed file maps every sponsor to a booth")
	}
}

// The real venue is one minute between any two zones, so the shortest-path case needs its own map.
const chainSeed = `{
  "event": {"name": "E", "timezone": "America/Denver", "starts_at": "2026-11-09T09:00:00-07:00", "ends_at": "2026-11-09T17:30:00-07:00"},
  "zones": [{"id": "main", "name": "Main"}, {"id": "kitchen", "name": "Kitchen"}, {"id": "cafe", "name": "Cafe"}, {"id": "lounge", "name": "Lounge"}],
  "walks": [{"a": "cafe", "b": "kitchen", "minutes": 2}, {"a": "kitchen", "b": "main", "minutes": 1}, {"a": "main", "b": "lounge", "minutes": 1}],
  "spots": [],
  "sponsors": []
}`

func TestWalkMinutesIsShortestPathAndZeroInSameZone(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	defer store.SwapSeedDataForTest([]byte(chainSeed))()
	require.NoError(t, s.Seed(ctx, "cnh"))

	same, err := s.WalkMinutes(ctx, "kitchen", "kitchen")
	require.NoError(t, err)
	assert.Equal(t, 0, same)

	// cafe to kitchen to main to lounge: 2 + 1 + 1.
	far, err := s.WalkMinutes(ctx, "lounge", "cafe")
	require.NoError(t, err)
	assert.Equal(t, 4, far)

	_, err = s.WalkMinutes(ctx, "kitchen", "nowhere")
	assert.ErrorIs(t, err, store.ErrNoWalk)
}

func TestFreshZoneExpiresAfter45Minutes(t *testing.T) {
	now := time.Date(2026, 11, 9, 12, 0, 0, 0, time.UTC)
	zone, seen := "kitchen", now.Add(-44*time.Minute)
	got, ok := store.FreshZone(store.Person{ZoneID: &zone, ZoneSeenAt: &seen}, now)
	assert.True(t, ok)
	assert.Equal(t, "kitchen", got)

	stale := now.Add(-46 * time.Minute)
	_, ok = store.FreshZone(store.Person{ZoneID: &zone, ZoneSeenAt: &stale}, now)
	assert.False(t, ok)

	_, ok = store.FreshZone(store.Person{}, now)
	assert.False(t, ok)
}

func TestEnsurePersonCreatesOnceAndCopiesRole(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))

	p, err := s.EnsurePerson(ctx, "user_1", store.RoleAttendee, "")
	require.NoError(t, err)
	assert.Empty(t, p.DisplayName)
	assert.Equal(t, store.RoleAttendee, p.Role)
	assert.True(t, p.Discoverable)

	again, err := s.EnsurePerson(ctx, "user_1", store.RoleSponsorRep, "Dash0")
	require.NoError(t, err)
	assert.Equal(t, p.ID, again.ID)
	assert.Equal(t, store.RoleSponsorRep, again.Role)
	require.NotNil(t, again.SponsorID)

	// A sponsor name that doesn't exist can't grant the sponsor_rep role.
	unknown, err := s.EnsurePerson(ctx, "user_2", store.RoleSponsorRep, "Nobody Inc")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAttendee, unknown.Role)
	assert.Nil(t, unknown.SponsorID)
	assert.Equal(t, 2, count(t, s, "person"))
}

func TestCompleteOnboardingSavesNameAndTags(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	p, err := s.EnsurePerson(ctx, "user_1", store.RoleAttendee, "")
	require.NoError(t, err)

	got, err := s.CompleteOnboarding(ctx, p.ID, "Priya", []string{"otel", "k8s"})
	require.NoError(t, err)
	assert.Equal(t, "Priya", got.DisplayName)
	assert.Equal(t, []string{"otel", "k8s"}, got.Tags)
}
