package store_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

// TestS3_OverlappingCreatesAtOnceLeaveOneSpace commits for real: a rolled-back transaction can't
// race itself. It uses its own event and spot, so nothing else can see these rows.
func TestS3_OverlappingCreatesAtOnceLeaveOneSpace(t *testing.T) {
	s := storetest.Pool(t)
	ctx := context.Background()
	tag := fmt.Sprintf("race-%d", time.Now().UnixNano())

	storetest.Exec(t, s, `INSERT INTO event (slug, name, timezone, starts_at, ends_at)
		VALUES ($1, 'Race', 'UTC', now(), now() + interval '1 day')`, tag)
	storetest.Exec(t, s, `INSERT INTO zone (id, event_id, name)
		SELECT $1, id, 'Race zone' FROM event WHERE slug = $1`, tag)
	spot := storetest.QueryString(t, s, `INSERT INTO spot (zone_id, name, capacity, qr_slug)
		VALUES ($1, $1, 8, $1 || '-qr-code-slug') RETURNING id::text`, tag)
	host, err := s.EnsurePerson(ctx, tag, store.RoleAttendee, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		storetest.Exec(t, s, `DELETE FROM space WHERE spot_id = $1`, spot)
		storetest.Exec(t, s, `DELETE FROM spot WHERE id = $1`, spot)
		storetest.Exec(t, s, `DELETE FROM event WHERE slug = $1`, tag)
		storetest.Exec(t, s, `DELETE FROM person WHERE id = $1`, host.ID)
	})

	starts := time.Now().Add(24 * time.Hour).Truncate(time.Minute)
	const attempts = 12
	results := make([]error, attempts)
	var wg sync.WaitGroup
	release := make(chan struct{})
	for i := range attempts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			_, results[i] = s.CreateSpace(ctx, host.ID, store.SpaceInput{
				Title: "Racing for the table", SpotID: spot, StartsAt: starts.Add(time.Duration(i) * time.Minute), Minutes: 30,
			})
		}()
	}
	close(release)
	wg.Wait()

	created := 0
	for _, err := range results {
		if err == nil {
			created++
			continue
		}
		var taken *store.SpotTakenError
		assert.True(t, errors.As(err, &taken), "unexpected error: %v", err)
	}
	assert.Equal(t, 1, created, "every start is within 30 minutes of every other")
}

func TestCancelAndRestoreReturnThoseToTell(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	spot := storetest.QueryString(t, s, `SELECT id::text FROM spot WHERE name = 'Long table'`)
	host, err := s.EnsurePerson(ctx, "user_h", store.RoleAttendee, "")
	require.NoError(t, err)
	other, err := s.EnsurePerson(ctx, "user_o", store.RoleAttendee, "")
	require.NoError(t, err)

	space, err := s.CreateSpace(ctx, host.ID, store.SpaceInput{
		Title: "Tell these people", SpotID: spot, StartsAt: time.Now().Add(time.Hour), Minutes: 15, Capacity: 4,
	})
	require.NoError(t, err)
	storetest.Exec(t, s, `INSERT INTO signup (space_id, person_id, status) VALUES ($1, $2, 'left')`, space.ID, other.ID)

	told, err := s.CancelSpace(ctx, space.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{host.ID}, told, "someone who left isn't told")

	_, err = s.CancelSpace(ctx, space.ID)
	assert.ErrorIs(t, err, store.ErrSpaceCancelled, "a second cancel tells no one twice")

	told, err = s.RestoreSpace(ctx, space.ID)
	require.NoError(t, err)
	assert.Equal(t, []string{host.ID}, told)
	got, err := s.SpaceByID(ctx, space.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SpaceOpen, got.Status)
}
