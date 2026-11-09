package web_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/store"
)

const scanHint = "Scan a table code to see what's close"

// feedSetup is a host and a viewer with the clock stopped at noon.
func feedSetup(t *testing.T) (*fixture, store.Person, store.Person, time.Time) {
	t.Helper()
	f := newFixture(t)
	f.at = noon(t)
	return f, f.onboard(t, "user_host", "Dana"), f.onboard(t, "user_a", "Ana"), f.at
}

// assertOrder checks that each string appears in text, in this order.
func assertOrder(t *testing.T, text string, want ...string) {
	t.Helper()
	last := -1
	for _, s := range want {
		at := strings.Index(text, s)
		require.GreaterOrEqual(t, at, 0, "%q is missing from %q", s, text)
		assert.Greater(t, at, last, "%q is out of order in %q", s, text)
		last = at
	}
}

func TestS8_AttendeeInKitchenSeesKitchenSpaceInTenAboveLoungeInFive(t *testing.T) {
	f, host, viewer, now := feedSetup(t)
	f.checkInAt(t, viewer, "kitchen", now)
	f.spaceAt(t, host, f.spot(t, "North lounge"), "Lounge in five", now.Add(5*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "Kitchen counter"), "Kitchen in ten", now.Add(10*time.Minute), 30)

	w := f.get("/e/cnh", as("user_a"))

	require.Equal(t, http.StatusOK, w.Code)
	assertOrder(t, visible(w.Body.String()), "Kitchen in ten", "Lounge in five")
}

func TestS8_SameWalkSortsBySoonestStart(t *testing.T) {
	f, host, viewer, now := feedSetup(t)
	f.checkInAt(t, viewer, "kitchen", now)
	f.spaceAt(t, host, f.spot(t, "North lounge"), "Lounge later", now.Add(20*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "The bar"), "Cafe sooner", now.Add(5*time.Minute), 30)

	assertOrder(t, visible(f.get("/e/cnh", as("user_a")).Body.String()), "Cafe sooner", "Lounge later")
}

func TestS8_FeedHoldsSpacesHappeningNowOrStartingWithinThirtyMinutes(t *testing.T) {
	f, host, _, now := feedSetup(t)
	f.spaceAt(t, host, f.spot(t, "North lounge"), "Under way", now.Add(-5*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "Kitchen counter"), "On the edge", now.Add(30*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "The bar"), "Too far off", now.Add(31*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "Long table"), "All done", now.Add(-40*time.Minute), 30)
	cancelled := f.spaceAt(t, host, f.spot(t, "Round tables"), "Called off", now.Add(5*time.Minute), 30)
	_, err := f.store.CancelSpace(context.Background(), cancelled.ID)
	require.NoError(t, err)

	text := visible(f.get("/e/cnh", as("user_a")).Body.String())

	assert.Contains(t, text, "Under way")
	assert.Contains(t, text, "On the edge")
	assert.NotContains(t, text, "Too far off")
	assert.NotContains(t, text, "All done")
	assert.NotContains(t, text, "Called off")
}

func TestS8_SpaceStartedOverTenMinutesAgoShowsHappeningButIsNotJoinable(t *testing.T) {
	f, host, _, now := feedSetup(t)
	closed := f.spaceAt(t, host, f.spot(t, "North lounge"), "Long under way", now.Add(-11*time.Minute), 30)
	open := f.spaceAt(t, host, f.spot(t, "Kitchen counter"), "Just begun", now.Add(-9*time.Minute), 30)

	body := f.get("/e/cnh", as("user_a")).Body.String()

	assert.Equal(t, 2, strings.Count(visible(body), "Happening now"))
	assert.NotContains(t, body, "/spaces/"+closed.Slug+"/join", "no way to join after ten minutes")
	assert.Contains(t, body, "/spaces/"+open.Slug+"/join")
	assert.Contains(t, visible(body), "See the space")
}

func TestS8_EachCardShowsTitleSpotWalkTimeStartsAndGoingCount(t *testing.T) {
	f, host, viewer, now := feedSetup(t)
	f.checkInAt(t, viewer, "kitchen", now)
	f.spaceAt(t, host, f.spot(t, "North lounge"), "Coming up", now.Add(12*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "Kitchen counter"), "Going on", now.Add(-3*time.Minute), 30)

	text := visible(f.get("/e/cnh", as("user_a")).Body.String())

	for _, want := range []string{"Coming up", "North lounge", "1 min walk", "starts in 12 min", "1 of 4 going",
		"Going on", "Kitchen counter", "Right here", "Happening now"} {
		assert.Contains(t, text, want)
	}
}

func TestS8_CardsLinkToTheirSpaceAndCarryNoNames(t *testing.T) {
	f, host, _, now := feedSetup(t)
	sp := f.spaceAt(t, host, f.spot(t, "North lounge"), "Coming up", now.Add(12*time.Minute), 30)

	body := f.get("/e/cnh/feed", as("user_a")).Body.String()

	assert.Contains(t, body, `href="/e/cnh/spaces/`+sp.Slug+`"`)
	assert.NotContains(t, visible(body), "Dana")
}

func TestS8_NewSpaceNearbyReachesOpenFeeds(t *testing.T) {
	f, _, viewer, _ := feedSetup(t)
	f.checkInAt(t, viewer, "kitchen", f.at)

	home := f.get("/e/cnh", as("user_a")).Body.String()
	assert.Contains(t, home, url.QueryEscape(live.EventTopic), "the home page follows the event topic")
	assert.Contains(t, home, `hx-get="/e/cnh/feed"`)
	assert.Contains(t, home, "sse:spaces-changed")

	events, cancel := f.hub.Subscribe(context.Background(), []string{live.EventTopic})
	defer cancel()

	spot := f.spot(t, "North lounge")
	starts := f.at.Add(12 * time.Minute)
	w := f.post("/e/cnh/spaces", url.Values{
		"title": {"Fresh and nearby"}, "spot": {spot.ID}, "starts": {starts.In(denver(t)).Format("2006-01-02T15:04")},
		"minutes": {"30"}, "capacity": {strconv.Itoa(4)},
	}, as("user_host"))
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())

	deadline := time.After(2 * time.Second)
	for got := false; !got; {
		select {
		case fragment := <-events:
			got = fragment.Event == "spaces-changed"
		case <-deadline:
			t.Fatal("no spaces-changed event within 2 seconds")
		}
	}

	refetched := visible(f.get("/e/cnh/feed", as("user_a")).Body.String())
	assert.Contains(t, refetched, "Fresh and nearby")
	assert.Contains(t, refetched, "starts in 12 min")
	assert.Contains(t, refetched, "1 min walk")
}

func TestS8_NoCheckInSortsByStartTimeAndOffersScanHint(t *testing.T) {
	f, host, _, now := feedSetup(t)
	f.spaceAt(t, host, f.spot(t, "Kitchen counter"), "Later here", now.Add(20*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "North lounge"), "Sooner there", now.Add(5*time.Minute), 30)

	text := visible(f.get("/e/cnh", as("user_a")).Body.String())

	assert.Contains(t, text, scanHint)
	assertOrder(t, text, "Sooner there", "Later here")
	assert.NotContains(t, text, "min walk", "without a check-in there's no walk to show")
}

func TestS8_StaleCheckInCountsAsNoCheckIn(t *testing.T) {
	f, host, viewer, now := feedSetup(t)
	f.checkInAt(t, viewer, "kitchen", now.Add(-46*time.Minute))
	f.spaceAt(t, host, f.spot(t, "Kitchen counter"), "Later here", now.Add(20*time.Minute), 30)
	f.spaceAt(t, host, f.spot(t, "North lounge"), "Sooner there", now.Add(5*time.Minute), 30)

	text := visible(f.get("/e/cnh", as("user_a")).Body.String())

	assert.Contains(t, text, scanHint)
	assertOrder(t, text, "Sooner there", "Later here")
}

func TestS8_FreshCheckInDoesNotShowScanHint(t *testing.T) {
	f, _, viewer, now := feedSetup(t)
	f.checkInAt(t, viewer, "kitchen", now)

	assert.NotContains(t, visible(f.get("/e/cnh", as("user_a")).Body.String()), scanHint)
}

func TestS8_EmptyFeedNamesOneNextActionWithAButton(t *testing.T) {
	f, _, _, _ := feedSetup(t)

	body := f.get("/e/cnh", as("user_a")).Body.String()

	assert.Contains(t, visible(body), "Nothing on in the next 30 minutes")
	assert.Contains(t, body, `href="/e/cnh/spaces/new"`)
}

func TestS8_CardSaysWhereTheViewerAlreadyStands(t *testing.T) {
	f, host, viewer, now := feedSetup(t)
	mine := f.spaceAt(t, viewer, f.spot(t, "Kitchen counter"), "My own space", now.Add(10*time.Minute), 30)
	going := f.spaceAt(t, host, f.spot(t, "North lounge"), "One I joined", now.Add(40*time.Minute), 30)
	f.at = now.Add(15 * time.Minute)
	_, err := f.store.JoinSpace(context.Background(), store.JoinInput{SpaceID: going.ID, PersonID: viewer.ID, Now: now})
	require.NoError(t, err)

	body := f.get("/e/cnh", as("user_a")).Body.String()
	text := visible(body)

	assert.Contains(t, text, "You're hosting")
	assert.Contains(t, text, "You're going")
	assert.NotContains(t, body, "/spaces/"+mine.Slug+"/join")
	assert.NotContains(t, body, "/spaces/"+going.Slug+"/join")
}

func TestS8_OrganizerSeesTheFeedWithoutJoinOrStart(t *testing.T) {
	f, host, _, now := feedSetup(t)
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}
	f.onboard(t, "user_org", "Olive")
	sp := f.spaceAt(t, host, f.spot(t, "North lounge"), "Coming up", now.Add(12*time.Minute), 30)

	body := f.get("/e/cnh", as("user_org")).Body.String()

	assert.Contains(t, visible(body), "Coming up")
	assert.NotContains(t, body, "/spaces/"+sp.Slug+"/join")
	assert.NotContains(t, body, `href="/e/cnh/spaces/new"`)
}

func TestS8_EmptyFeedSendsOrganizersToTheWall(t *testing.T) {
	f := newFixture(t)
	f.at = noon(t)
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}
	f.onboard(t, "user_org", "Olive")

	body := f.get("/e/cnh", as("user_org")).Body.String()

	assert.Contains(t, body, `href="/e/cnh/wall"`)
	assert.NotContains(t, body, `href="/e/cnh/spaces/new"`)
}

func TestS8_FeedFragmentNeedsASignedInPerson(t *testing.T) {
	f := newFixture(t)

	w := f.get("/e/cnh/feed", header("HX-Request", "true"))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}
