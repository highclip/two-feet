package store_test

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store/storetest"
)

func TestS10_SpacesOpenCountsOnlySpacesNotCancelledAndNotOver(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	emptyVenue(t, s)
	require.NoError(t, s.Seed(ctx, "cnh"))
	host, err := s.EnsurePerson(ctx, "user_s10", "attendee", "")
	require.NoError(t, err)

	insert := func(slug, status, startsAt string) {
		t.Helper()
		require.NoError(t, s.ExecForTest(ctx, fmt.Sprintf(`
			INSERT INTO space (slug, host_id, title, spot_id, starts_at, minutes, capacity, status)
			SELECT '%s', '%s', 'A space title', id, %s, 30, 8, '%s' FROM spot LIMIT 1`,
			slug, host.ID, startsAt, status)))
	}
	insert("upcoming", "open", "now() + interval '10 minutes'")
	insert("happening", "open", "now() - interval '10 minutes'")
	insert("over", "open", "now() - interval '45 minutes'")
	insert("called-off", "cancelled", "now() + interval '10 minutes'")

	n, err := s.CountOpenSpaces(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
}
