package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

func seededSponsor(t *testing.T, s *store.Store, name string) store.Sponsor {
	t.Helper()
	sponsors, err := s.Sponsors(context.Background())
	require.NoError(t, err)
	for _, sp := range sponsors {
		if sp.Name == name {
			return sp
		}
	}
	t.Fatalf("no sponsor %s", name)
	return store.Sponsor{}
}

func TestUpdateSponsorSolvesChangesOnlyThatSponsor(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	dash0, elastic := seededSponsor(t, s, "Dash0"), seededSponsor(t, s, "Elastic")

	got, err := s.UpdateSponsorSolves(ctx, dash0.ID, "New words")

	require.NoError(t, err)
	assert.Equal(t, "New words", got.Solves)
	assert.Equal(t, "Dash0", got.Name)
	again, err := s.SponsorByID(ctx, dash0.ID)
	require.NoError(t, err)
	assert.Equal(t, "New words", again.Solves)
	assert.Equal(t, elastic.Solves, seededSponsor(t, s, "Elastic").Solves)
}

func TestSponsorLookupsReportUnknownIDsAsNotFound(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	const missing = "00000000-0000-4000-8000-000000000000"

	_, err := s.SponsorByID(ctx, missing)
	assert.ErrorIs(t, err, store.ErrNotFound)
	_, err = s.UpdateSponsorSolves(ctx, missing, "words")
	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSponsorBoothIsNotFoundUntilMapped(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))

	_, err := s.SponsorBooth(ctx, seededSponsor(t, s, "Dash0").ID, "kitchen")

	assert.ErrorIs(t, err, store.ErrNotFound)
}

func TestSponsorBoothGivesSpotZoneAndShortestWalk(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	require.NoError(t, s.SetSponsorBooth(ctx, "Dash0", "Couches"))
	id := seededSponsor(t, s, "Dash0").ID

	from, err := s.SponsorBooth(ctx, id, "kitchen") // kitchen to main to lounge: 1 + 1
	require.NoError(t, err)
	assert.Equal(t, "Couches", from.SpotName)
	assert.Equal(t, "lounge", from.ZoneID)
	assert.Equal(t, "Lounge", from.ZoneName)
	require.NotNil(t, from.WalkMinutes)
	assert.Equal(t, 2, *from.WalkMinutes)

	same, err := s.SponsorBooth(ctx, id, "lounge")
	require.NoError(t, err)
	require.NotNil(t, same.WalkMinutes)
	assert.Equal(t, 0, *same.WalkMinutes)
}

func TestSponsorBoothWithoutACheckInHasNoWalk(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	require.NoError(t, s.SetSponsorBooth(ctx, "Dash0", "Couches"))

	booth, err := s.SponsorBooth(ctx, seededSponsor(t, s, "Dash0").ID, "")

	require.NoError(t, err)
	assert.Equal(t, "Couches", booth.SpotName)
	assert.Nil(t, booth.WalkMinutes)
}

func TestSponsorBoothWithNoRouteHasNoWalk(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	require.NoError(t, s.SetSponsorBooth(ctx, "Dash0", "Couches"))

	booth, err := s.SponsorBooth(ctx, seededSponsor(t, s, "Dash0").ID, "nowhere")

	require.NoError(t, err)
	assert.Nil(t, booth.WalkMinutes)
}

func TestSetSponsorBoothRefusesAnUnknownSpotOrSponsor(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))

	assert.ErrorIs(t, s.SetSponsorBooth(ctx, "Dash0", "No such spot"), store.ErrNotFound)
	assert.ErrorIs(t, s.SetSponsorBooth(ctx, "No such sponsor", "Couches"), store.ErrNotFound)
	assert.Nil(t, seededSponsor(t, s, "Dash0").BoothSpotID)
}

const boothSeed = `{
  "event": {"name": "E", "timezone": "America/Denver", "starts_at": "2026-11-09T09:00:00-07:00", "ends_at": "2026-11-09T17:30:00-07:00"},
  "zones": [{"id": "booths", "name": "Sponsor row"}],
  "walks": [],
  "spots": [{"name": "Dash0 booth", "zone": "booths", "where": "", "capacity": 4}],
  "sponsors": [{"name": "Dash0", "solves": "Traces", "booth": "%s"}]
}`

func TestSeedAppliesABoothNamedInTheSeedFile(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	defer store.SwapSeedDataForTest(fmt.Appendf(nil, boothSeed, "Dash0 booth"))()

	require.NoError(t, s.Seed(ctx, "cnh"))
	require.NoError(t, s.Seed(ctx, "cnh"))

	assert.NotNil(t, seededSponsor(t, s, "Dash0").BoothSpotID)
	assert.Equal(t, 1, count(t, s, "sponsor"))
}

func TestSeedFailsLoudlyOnABoothThatNamesNoSpot(t *testing.T) {
	s := storetest.Tx(t)
	emptyVenue(t, s)
	defer store.SwapSeedDataForTest(fmt.Appendf(nil, boothSeed, "Typo booth"))()

	assert.ErrorIs(t, s.Seed(context.Background(), "cnh"), store.ErrNotFound)
}

func TestSeedingTwiceKeepsAManualBoothWhenTheSeedNamesNone(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	require.NoError(t, s.SetSponsorBooth(ctx, "Dash0", "Couches"))

	require.NoError(t, s.Seed(ctx, "cnh"))

	assert.NotNil(t, seededSponsor(t, s, "Dash0").BoothSpotID)
}
