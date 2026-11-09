package store_test

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

type signupFixture struct {
	s    *store.Store
	spot string
	host store.Person
	now  time.Time
}

func newSignupFixture(t *testing.T) *signupFixture {
	t.Helper()
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	host, err := s.EnsurePerson(ctx, "user_host", store.RoleAttendee, "")
	require.NoError(t, err)
	return &signupFixture{
		s:    s,
		spot: storetest.QueryString(t, s, `SELECT id::text FROM spot WHERE name = 'Long table'`),
		host: host,
		now:  time.Now().Truncate(time.Second),
	}
}

func (f *signupFixture) person(t *testing.T, id string) store.Person {
	t.Helper()
	p, err := f.s.EnsurePerson(context.Background(), id, store.RoleAttendee, "")
	require.NoError(t, err)
	return p
}

// space makes a space at its own spot time so it doesn't hit S3's overlap rule. offset is minutes
// from now.
func (f *signupFixture) space(t *testing.T, spot string, offset, minutes, capacity int) store.Space {
	t.Helper()
	sp, err := f.s.CreateSpace(context.Background(), f.host.ID, store.SpaceInput{
		Title: "Collector tuning", SpotID: spot,
		StartsAt: f.now.Add(time.Duration(offset) * time.Minute), Minutes: minutes, Capacity: capacity,
	})
	require.NoError(t, err)
	return sp
}

func (f *signupFixture) join(t *testing.T, space, person string) store.JoinResult {
	t.Helper()
	got, err := f.s.JoinSpace(context.Background(), store.JoinInput{SpaceID: space, PersonID: person, Now: f.now})
	require.NoError(t, err)
	return got
}

func (f *signupFixture) status(t *testing.T, space, person string) string {
	t.Helper()
	return storetest.QueryString(t, f.s, `SELECT status FROM signup WHERE space_id = $1 AND person_id = $2`, space, person)
}

// TestS4_TwentyJoinsAtOnceFillExactlyEight commits for real: a rolled-back transaction can't race
// itself. The host's seat is removed first so the twenty joiners compete for all eight.
func TestS4_TwentyJoinsAtOnceFillExactlyEight(t *testing.T) {
	s := storetest.Pool(t)
	ctx := context.Background()
	tag := fmt.Sprintf("join-race-%d", time.Now().UnixNano())

	storetest.Exec(t, s, `INSERT INTO event (slug, name, timezone, starts_at, ends_at)
		VALUES ($1, 'Race', 'UTC', now(), now() + interval '1 day')`, tag)
	storetest.Exec(t, s, `INSERT INTO zone (id, event_id, name)
		SELECT $1, id, 'Race zone' FROM event WHERE slug = $1`, tag)
	spot := storetest.QueryString(t, s, `INSERT INTO spot (zone_id, name, capacity, qr_slug)
		VALUES ($1, $1, 8, $1 || '-qr-code-slug') RETURNING id::text`, tag)
	host, err := s.EnsurePerson(ctx, tag+"-host", store.RoleAttendee, "")
	require.NoError(t, err)

	const joiners = 20
	people := make([]store.Person, joiners)
	for i := range people {
		people[i], err = s.EnsurePerson(ctx, fmt.Sprintf("%s-%d", tag, i), store.RoleAttendee, "")
		require.NoError(t, err)
	}
	t.Cleanup(func() {
		storetest.Exec(t, s, `DELETE FROM space WHERE spot_id = $1`, spot)
		storetest.Exec(t, s, `DELETE FROM spot WHERE id = $1`, spot)
		storetest.Exec(t, s, `DELETE FROM event WHERE slug = $1`, tag)
		storetest.Exec(t, s, `DELETE FROM person WHERE clerk_user_id LIKE $1`, tag+"%")
	})

	space, err := s.CreateSpace(ctx, host.ID, store.SpaceInput{
		Title: "Racing for a seat", SpotID: spot, StartsAt: time.Now().Add(time.Hour), Minutes: 30, Capacity: 8,
	})
	require.NoError(t, err)
	storetest.Exec(t, s, `DELETE FROM signup WHERE space_id = $1`, space.ID)

	results := make([]store.JoinResult, joiners)
	errs := make([]error, joiners)
	var wg sync.WaitGroup
	release := make(chan struct{})
	for i := range people {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-release
			results[i], errs[i] = s.JoinSpace(ctx, store.JoinInput{SpaceID: space.ID, PersonID: people[i].ID, Now: time.Now()})
		}()
	}
	close(release)
	wg.Wait()

	going, waitlisted := 0, 0
	places := map[int]bool{}
	for i, res := range results {
		require.NoError(t, errs[i])
		switch res.Status {
		case store.SignupGoing:
			going++
		case store.SignupWaitlist:
			waitlisted++
			places[res.WaitlistPlace] = true
		}
	}
	assert.Equal(t, 8, going)
	assert.Equal(t, 12, waitlisted)
	for place := 1; place <= 12; place++ {
		assert.True(t, places[place], "place %d in line was handed out once", place)
	}

	got, err := s.SpaceByID(ctx, space.ID)
	require.NoError(t, err)
	assert.Equal(t, 8, got.Going)
	assert.Equal(t, 12, got.Waitlist)
}

func TestS4_WaitlistFollowsJoinOrder(t *testing.T) {
	f := newSignupFixture(t)
	sp := f.space(t, f.spot, 60, 30, 2) // the host has one of the two seats

	assert.Equal(t, store.SignupGoing, f.join(t, sp.ID, f.person(t, "a").ID).Status)
	for i, id := range []string{"b", "c", "d"} {
		got := f.join(t, sp.ID, f.person(t, id).ID)
		assert.Equal(t, store.SignupWaitlist, got.Status)
		assert.Equal(t, i+1, got.WaitlistPlace)
	}
}

func TestS4_LeavingPromotesTheFirstWaitlisted(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 60, 30, 2)
	a, b, c := f.person(t, "a"), f.person(t, "b"), f.person(t, "c")
	f.join(t, sp.ID, a.ID)
	f.join(t, sp.ID, b.ID)
	f.join(t, sp.ID, c.ID)

	got, err := f.s.LeaveSpace(ctx, sp.ID, a.ID, f.now)
	require.NoError(t, err)

	assert.Equal(t, []string{b.ID}, got.Promoted)
	assert.Equal(t, store.SignupLeft, f.status(t, sp.ID, a.ID))
	assert.Equal(t, store.SignupGoing, f.status(t, sp.ID, b.ID))
	assert.Equal(t, store.SignupWaitlist, f.status(t, sp.ID, c.ID))

	place, err := f.s.SignupOf(ctx, sp.ID, c.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, place.WaitlistPlace, "c moved up a place")
}

func TestS4_LeavingTheWaitlistPromotesNobody(t *testing.T) {
	f := newSignupFixture(t)
	sp := f.space(t, f.spot, 60, 30, 2)
	a, b := f.person(t, "a"), f.person(t, "b")
	f.join(t, sp.ID, a.ID)
	f.join(t, sp.ID, b.ID)

	got, err := f.s.LeaveSpace(context.Background(), sp.ID, b.ID, f.now)
	require.NoError(t, err)
	assert.Empty(t, got.Promoted)
	assert.False(t, got.WasGoing)
}

func TestJoinTwiceChangesNothing(t *testing.T) {
	f := newSignupFixture(t)
	sp := f.space(t, f.spot, 60, 30, 3)
	a := f.person(t, "a")

	first := f.join(t, sp.ID, a.ID)
	assert.True(t, first.Changed)
	second := f.join(t, sp.ID, a.ID)
	assert.False(t, second.Changed)
	assert.Equal(t, store.SignupGoing, second.Status)

	got, err := f.s.SpaceByID(context.Background(), sp.ID)
	require.NoError(t, err)
	assert.Equal(t, 2, got.Going)
}

func TestRejoiningAfterLeavingGoesToTheBackOfTheLine(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 60, 30, 2)
	a, b, c := f.person(t, "a"), f.person(t, "b"), f.person(t, "c")
	f.join(t, sp.ID, a.ID) // takes the second seat
	f.join(t, sp.ID, b.ID) // first in line
	f.join(t, sp.ID, c.ID) // second in line

	_, err := f.s.LeaveSpace(ctx, sp.ID, b.ID, f.now)
	require.NoError(t, err)
	got := f.join(t, sp.ID, b.ID)
	assert.Equal(t, store.SignupWaitlist, got.Status)
	assert.Equal(t, 2, got.WaitlistPlace, "b lost their place by leaving")
}

func TestS4_JoiningAnOverlappingSpaceAsksToLeaveTheFirst(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	couches := storetest.QueryString(t, f.s, `SELECT id::text FROM spot WHERE name = 'Couches'`)
	first := f.space(t, f.spot, 60, 30, 2)
	second := f.space(t, couches, 75, 30, 4)
	third := f.space(t, couches, 120, 15, 4)
	a := f.person(t, "a")
	f.join(t, first.ID, a.ID)

	_, err := f.s.JoinSpace(ctx, store.JoinInput{SpaceID: second.ID, PersonID: a.ID, Now: f.now})
	var overlap *store.OverlapError
	require.ErrorAs(t, err, &overlap)
	assert.Equal(t, first.ID, overlap.SpaceID)
	assert.Equal(t, "Collector tuning", overlap.Title)
	assert.Equal(t, store.SignupGoing, f.status(t, first.ID, a.ID), "nothing changes until they agree")

	got := f.join(t, third.ID, a.ID)
	assert.Equal(t, store.SignupGoing, got.Status, "a space later in the day doesn't overlap")
}

func TestSwitchingLeavesTheFirstSpaceAndPromotesItsWaitlist(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	couches := storetest.QueryString(t, f.s, `SELECT id::text FROM spot WHERE name = 'Couches'`)
	first := f.space(t, f.spot, 60, 30, 2)
	second := f.space(t, couches, 75, 30, 4)
	a, b := f.person(t, "a"), f.person(t, "b")
	f.join(t, first.ID, a.ID)
	f.join(t, first.ID, b.ID) // waitlisted

	got, err := f.s.JoinSpace(ctx, store.JoinInput{SpaceID: second.ID, PersonID: a.ID, LeaveSpaceID: first.ID, Now: f.now})
	require.NoError(t, err)

	assert.Equal(t, store.SignupGoing, got.Status)
	assert.Equal(t, []string{b.ID}, got.Promoted)
	assert.Equal(t, first.ID, got.LeftSpaceID)
	assert.Equal(t, store.SignupLeft, f.status(t, first.ID, a.ID))
	assert.Equal(t, store.SignupGoing, f.status(t, first.ID, b.ID))
}

func TestHostCannotLeaveTheirOwnSpace(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	couches := storetest.QueryString(t, f.s, `SELECT id::text FROM spot WHERE name = 'Couches'`)
	hosted := f.space(t, f.spot, 60, 30, 4)
	other := f.space(t, couches, 75, 30, 4)
	storetest.Exec(t, f.s, `DELETE FROM signup WHERE space_id = $1 AND person_id = $2`, other.ID, f.host.ID)

	_, err := f.s.LeaveSpace(ctx, hosted.ID, f.host.ID, f.now)
	assert.ErrorIs(t, err, store.ErrHostCannotLeave)

	_, err = f.s.JoinSpace(ctx, store.JoinInput{SpaceID: other.ID, PersonID: f.host.ID, LeaveSpaceID: hosted.ID, Now: f.now})
	assert.ErrorIs(t, err, store.ErrHostCannotLeave, "switching away from a hosted space is leaving it")

	_, err = f.s.JoinSpace(ctx, store.JoinInput{SpaceID: other.ID, PersonID: f.host.ID, Now: f.now})
	var overlap *store.OverlapError
	require.ErrorAs(t, err, &overlap)
	assert.True(t, overlap.Hosting)
}

func TestBeingOnlyWaitlistedDoesNotCountAsOverlap(t *testing.T) {
	f := newSignupFixture(t)
	couches := storetest.QueryString(t, f.s, `SELECT id::text FROM spot WHERE name = 'Couches'`)
	first := f.space(t, f.spot, 60, 30, 2)
	second := f.space(t, couches, 75, 30, 4)
	a, b := f.person(t, "a"), f.person(t, "b")
	f.join(t, first.ID, a.ID)
	f.join(t, first.ID, b.ID) // waitlisted

	assert.Equal(t, store.SignupGoing, f.join(t, second.ID, b.ID).Status)
}

func TestJoinIsRefusedAfterTenMinutesAndOnCancelledSpaces(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 0, 45, 4)
	a, b := f.person(t, "a"), f.person(t, "b")

	_, err := f.s.JoinSpace(ctx, store.JoinInput{SpaceID: sp.ID, PersonID: a.ID, Now: f.now.Add(10 * time.Minute)})
	require.NoError(t, err, "exactly ten minutes is still in")
	_, err = f.s.JoinSpace(ctx, store.JoinInput{SpaceID: sp.ID, PersonID: b.ID, Now: f.now.Add(10*time.Minute + time.Second)})
	assert.ErrorIs(t, err, store.ErrJoinClosed)

	_, err = f.s.CancelSpace(ctx, sp.ID)
	require.NoError(t, err)
	_, err = f.s.JoinSpace(ctx, store.JoinInput{SpaceID: sp.ID, PersonID: b.ID, Now: f.now})
	assert.ErrorIs(t, err, store.ErrSpaceCancelled)
}

func TestLeaveIsRefusedOnceTheSpaceHasFinished(t *testing.T) {
	f := newSignupFixture(t)
	sp := f.space(t, f.spot, 0, 15, 4)
	a := f.person(t, "a")
	f.join(t, sp.ID, a.ID)

	_, err := f.s.LeaveSpace(context.Background(), sp.ID, a.ID, f.now.Add(15*time.Minute))
	assert.ErrorIs(t, err, store.ErrSpaceOver)
}

func TestUndoLeaveReturnsToTheSameSeat(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 60, 30, 4)
	a, b := f.person(t, "a"), f.person(t, "b")
	f.join(t, sp.ID, a.ID)
	f.join(t, sp.ID, b.ID)

	_, err := f.s.LeaveSpace(ctx, sp.ID, a.ID, f.now)
	require.NoError(t, err)
	require.NoError(t, f.s.UndoLeave(ctx, sp.ID, a.ID, f.now))

	going, err := f.s.GoingTo(ctx, sp.ID)
	require.NoError(t, err)
	require.Len(t, going, 3)
	assert.Equal(t, a.ID, going[1].ID, "the same seat, not the end of the row")
}

func TestUndoLeaveRestoresAPlaceInLine(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 60, 30, 2)
	a, b, c := f.person(t, "a"), f.person(t, "b"), f.person(t, "c")
	f.join(t, sp.ID, a.ID)
	f.join(t, sp.ID, b.ID)
	f.join(t, sp.ID, c.ID)

	_, err := f.s.LeaveSpace(ctx, sp.ID, b.ID, f.now)
	require.NoError(t, err)
	require.NoError(t, f.s.UndoLeave(ctx, sp.ID, b.ID, f.now))

	first, err := f.s.SignupOf(ctx, sp.ID, b.ID)
	require.NoError(t, err)
	assert.Equal(t, store.SignupWaitlist, first.Status)
	assert.Equal(t, 1, first.WaitlistPlace)
}

func TestUndoLeaveIsRefusedWhenTheSeatHasGone(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 60, 30, 2)
	a, b := f.person(t, "a"), f.person(t, "b")
	f.join(t, sp.ID, a.ID)

	_, err := f.s.LeaveSpace(ctx, sp.ID, a.ID, f.now)
	require.NoError(t, err)
	f.join(t, sp.ID, b.ID)

	assert.ErrorIs(t, f.s.UndoLeave(ctx, sp.ID, a.ID, f.now), store.ErrSeatTaken)
	assert.Equal(t, store.SignupLeft, f.status(t, sp.ID, a.ID))
}

func TestRaisedCapacityPromotesTheWaitlist(t *testing.T) {
	f := newSignupFixture(t)
	ctx := context.Background()
	sp := f.space(t, f.spot, 60, 30, 2)
	a, b, c := f.person(t, "a"), f.person(t, "b"), f.person(t, "c")
	f.join(t, sp.ID, a.ID)
	f.join(t, sp.ID, b.ID)
	f.join(t, sp.ID, c.ID)
	storetest.Exec(t, f.s, `UPDATE space SET capacity = 3 WHERE id = $1`, sp.ID)

	promoted, err := f.s.FillOpenSeats(ctx, sp.ID, f.now)
	require.NoError(t, err)
	assert.Equal(t, []string{b.ID}, promoted)
	assert.Equal(t, store.SignupWaitlist, f.status(t, sp.ID, c.ID))

	again := f.join(t, sp.ID, f.person(t, "d").ID)
	assert.Equal(t, store.SignupWaitlist, again.Status, "new joiners don't jump the line")
}
