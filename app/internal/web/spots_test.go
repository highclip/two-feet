package web_test

import (
	"context"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
	"github.com/highclip/two-feet/app/internal/web"
)

var (
	tagPattern   = regexp.MustCompile(`<[^>]+>`)
	spacePattern = regexp.MustCompile(`\s+`)
)

// visible is a page's text as someone reads it: no tags, no scripts' attributes, single spaces.
func visible(body string) string {
	return strings.TrimSpace(spacePattern.ReplaceAllString(html.UnescapeString(tagPattern.ReplaceAllString(body, "")), " "))
}

// noon is a fixed moment far from any row an e2e run leaves in a kept database, and from midnight,
// so "today" means the same thing whenever the tests run.
func noon(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2031, 3, 12, 12, 0, 0, 0, denver(t))
}

func (f *fixture) spot(t *testing.T, name string) store.Spot {
	t.Helper()
	ctx := context.Background()
	e, err := f.store.EventBySlug(ctx, "cnh")
	require.NoError(t, err)
	spots, err := f.store.Spots(ctx, e.ID)
	require.NoError(t, err)
	for _, sp := range spots {
		if sp.Name == name {
			return sp
		}
	}
	t.Fatalf("no spot called %s", name)
	return store.Spot{}
}

func spotPath(sp store.Spot) string { return "/e/cnh?spot=" + url.QueryEscape(sp.QRSlug) }

func (f *fixture) spaceAt(t *testing.T, host store.Person, spot store.Spot, title string, starts time.Time, minutes int) store.Space {
	t.Helper()
	sp, err := f.store.CreateSpace(context.Background(), host.ID, store.SpaceInput{
		Title: title, SpotID: spot.ID, StartsAt: starts, Minutes: minutes, Capacity: 4,
	})
	require.NoError(t, err)
	return sp
}

func (f *fixture) person(t *testing.T, clerkUserID string) store.Person {
	t.Helper()
	p, err := f.store.PersonByClerkID(context.Background(), clerkUserID)
	require.NoError(t, err)
	return p
}

func (f *fixture) checkInAt(t *testing.T, p store.Person, zone string, at time.Time) {
	t.Helper()
	require.NoError(t, f.store.CheckIn(context.Background(), p.ID, zone, at))
}

func TestS2_SignedInAttendeeOpensSpotQRZoneIsSetAndPageSaysYoureAt(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")
	spot := f.spot(t, "Kitchen island")

	before := time.Now()
	w := f.get(spotPath(spot), as("user_a"))

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, visible(w.Body.String()), "You're at Kitchen island, Kitchen")
	p := f.person(t, "user_a")
	require.NotNil(t, p.ZoneID)
	assert.Equal(t, "kitchen", *p.ZoneID)
	require.NotNil(t, p.ZoneSeenAt)
	assert.WithinDuration(t, before, *p.ZoneSeenAt, 5*time.Second)
}

func TestS2_SpotPageShowsNameWhereNowAndNextTwoTodayWithCountsAndJoin(t *testing.T) {
	f := newFixture(t)
	now := noon(t)
	f.at = now
	host := f.onboard(t, "user_host", "Dana")
	f.onboard(t, "user_a", "Ana")
	spot := f.spot(t, "Kitchen island")
	f.spaceAt(t, host, spot, "Happening right now", now.Add(-10*time.Minute), 30)
	f.spaceAt(t, host, spot, "First one next", now.Add(30*time.Minute), 30)
	f.spaceAt(t, host, spot, "Second one next", now.Add(90*time.Minute), 30)
	f.spaceAt(t, host, spot, "Third is too far down", now.Add(150*time.Minute), 30)
	f.spaceAt(t, host, spot, "Tomorrow is not today", now.Add(24*time.Hour), 30)
	cancelled := f.spaceAt(t, host, spot, "Called off", now.Add(4*time.Hour), 30)
	_, err := f.store.CancelSpace(context.Background(), cancelled.ID)
	require.NoError(t, err)

	body := f.get(spotPath(spot), as("user_a")).Body.String()
	text := visible(body)

	assert.Contains(t, text, "Kitchen island")
	assert.Contains(t, text, "The tall table by the coffee machine")
	assert.Contains(t, text, "Happening right now")
	assert.Contains(t, text, "Happening now · until 12:20")
	assert.Contains(t, text, "First one next")
	assert.Contains(t, text, "Second one next")
	assert.NotContains(t, text, "Third is too far down")
	assert.NotContains(t, text, "Tomorrow is not today")
	assert.NotContains(t, text, "Called off")
	assert.Equal(t, 3, strings.Count(text, "1 of 4 going"), text)
	assert.Len(t, regexp.MustCompile(`action="[^"]*/join"`).FindAllString(body, -1), 3, "one Join form per space")
	assert.Contains(t, text, "Join · 3 seats left")
	assert.Contains(t, text, "Dana", "signed-in people see hosts")
}

func TestSpotPageWithNothingOnSaysSoAndOffersToStart(t *testing.T) {
	f := newFixture(t)
	f.at = noon(t)
	f.onboard(t, "user_a", "Ana")

	text := visible(f.get(spotPath(f.spot(t, "Couches")), as("user_a")).Body.String())

	assert.Contains(t, text, "Nothing here yet. Whoever comes are the right people. Start one.")
	assert.Contains(t, text, "Start a space here")
}

func TestS2_StartASpaceHereOpensFormWithThisSpotAndNextFreeSlot(t *testing.T) {
	f := newFixture(t)
	now := time.Date(2031, 3, 12, 14, 38, 0, 0, denver(t))
	f.at = now
	host := f.onboard(t, "user_host", "Dana")
	f.onboard(t, "user_a", "Ana")
	spot := f.spot(t, "Kitchen island")
	f.spaceAt(t, host, spot, "Happening right now", now.Add(-8*time.Minute), 30)                         // until 3:00
	f.spaceAt(t, host, spot, "Three o'clock one", time.Date(2031, 3, 12, 15, 0, 0, 0, denver(t)), 30)    // until 3:30
	f.spaceAt(t, host, spot, "Quarter to four one", time.Date(2031, 3, 12, 15, 45, 0, 0, denver(t)), 30) // leaves a 15-minute gap

	body := f.get(spotPath(spot), as("user_a")).Body.String()
	assert.Contains(t, visible(body), "Next free: 3:30 · 15 minutes")

	match := regexp.MustCompile(`href="(/e/cnh/spaces/new[^"]*)"`).FindStringSubmatch(body)
	require.NotNil(t, match, "the Start a space here link")
	form := f.get(html.UnescapeString(match[1]), as("user_a"))
	require.Equal(t, http.StatusOK, form.Code)
	page := form.Body.String()
	assert.Regexp(t, fmt.Sprintf(`value="%s" selected`, spot.ID), page)
	assert.Contains(t, page, `value="2031-03-12T15:30"`)
	assert.Regexp(t, `value="15" checked`, page, "only 15 minutes fits before the next space")
}

func TestNextFreeSlotRoundsUpSkipsBusyTimeAndSaysWhatFits(t *testing.T) {
	at := func(h, m int) time.Time { return time.Date(2031, 3, 12, h, m, 0, 0, time.UTC) }
	space := func(h, m, minutes int) store.Space { return store.Space{StartsAt: at(h, m), Minutes: minutes} }

	cases := []struct {
		name     string
		schedule []store.Space
		now      time.Time
		want     time.Time
		fits     int
	}{
		{"nothing on", nil, at(14, 38), at(14, 45), 45},
		{"exactly on a boundary still moves on", nil, at(14, 45), at(15, 0), 45},
		{"a later space limits what fits", []store.Space{space(15, 15, 30)}, at(14, 38), at(14, 45), 30},
		{"skips a space and rounds up after it", []store.Space{space(14, 30, 40)}, at(14, 38), at(15, 15), 45},
		{"a gap of exactly one slot is used", []store.Space{space(14, 30, 15), space(15, 0, 15)}, at(14, 44), at(14, 45), 15},
		{"a gap too small to use is skipped", []store.Space{space(14, 30, 20), space(15, 0, 15)}, at(14, 44), at(15, 15), 45},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, fits := web.NextFreeSlot(tc.schedule, tc.now)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.fits, fits)
		})
	}
}

func TestS2_SpotPageUpdatesWhenSomeoneJoinsOrASpaceIsCreatedHere(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_host", "Dana")
	f.onboard(t, "user_a", "Ana")
	spot := f.spot(t, "Long table")

	page := f.get(spotPath(spot)).Body.String()
	assert.Contains(t, page, html.EscapeString(live.URL("cnh", live.SpotTopic(spot.ID))), "the page follows its spot")
	assert.Regexp(t, `hx-trigger="sse:spaces-changed"`, page, "the board refetches when spaces change")

	updates := f.subscribe(t, live.SpotTopic(spot.ID))
	path := f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 30), "30", "4"))
	id := spaceID(t, f, path)
	expectFragment(t, updates, "spaces-changed")

	board := f.get(boardPath(spot)).Body.String()
	assert.Contains(t, board, `sse-swap="space-`+id+`"`, "the new row takes count updates")

	require.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_a")).Code)
	count := expectFragment(t, updates, "space-"+id)
	assert.Contains(t, count, "2 of 4 going")
	assert.NotContains(t, count, "Ana", "public topics carry counts only")
}

func boardPath(spot store.Spot) string {
	return "/e/cnh/spots/" + url.PathEscape(spot.QRSlug) + "/board"
}

func TestBoardRefetchDoesNotCheckAnyoneIn(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")
	spot := f.spot(t, "Couches")

	require.Equal(t, http.StatusOK, f.get(boardPath(spot), as("user_a")).Code)

	assert.Nil(t, f.person(t, "user_a").ZoneID)
	assert.Equal(t, http.StatusNotFound, f.get("/e/cnh/spots/nope/board").Code)
}

func TestS2_VisitorSeesNowAndNextWithoutNamesAndSignInReturnsToSameSpot(t *testing.T) {
	f := newFixture(t)
	now := noon(t)
	f.at = now
	host := f.onboard(t, "user_host", "Dana")
	spot := f.spot(t, "Kitchen island")
	f.spaceAt(t, host, spot, "Happening right now", now.Add(-10*time.Minute), 30)
	next := f.spaceAt(t, host, spot, "First one next", now.Add(30*time.Minute), 30)
	f.checkInAt(t, host, "kitchen", now)

	w := f.get(spotPath(spot))
	require.Equal(t, http.StatusOK, w.Code)
	body := w.Body.String()
	text := visible(body)

	assert.Contains(t, text, "Happening right now")
	assert.Contains(t, text, "First one next")
	assert.Contains(t, text, "1 of 4 going")
	assert.NotContains(t, text, "Dana")
	assert.NotContains(t, text, "in the Kitchen", "zone counts are for people who are signed in")
	assert.NotContains(t, text, "You're at")

	back := auth.SignInURL(spotPath(spot))
	assert.Contains(t, body, html.EscapeString(back), "signing in comes back here")

	seats := f.get("/e/cnh/spaces/" + next.Slug + "/seats?spot=" + url.QueryEscape(spot.QRSlug)).Body.String()
	assert.Contains(t, seats, html.EscapeString(back), "a refetched Join button comes back here too")

	f.onboard(t, "user_a", "Ana")
	returned := f.get(spotPath(spot), as("user_a"))
	assert.Contains(t, visible(returned.Body.String()), "You're at Kitchen island, Kitchen")
	zone := f.person(t, "user_a").ZoneID
	require.NotNil(t, zone)
	assert.Equal(t, "kitchen", *zone)
}

func TestSeatsIgnoresTheSpotParameterForSignedInPeople(t *testing.T) {
	f := newFixture(t)
	path, _ := f.joinableSpace(t, "4")
	f.onboard(t, "user_a", "Ana")

	body := f.get(path+"/seats?spot=abc", as("user_a")).Body.String()

	assert.NotContains(t, body, "/sign-in")
	assert.Contains(t, body, path+"/join")
}

func TestS2_UnknownCodeSaysNotFromThisEventAndZoneUnchanged(t *testing.T) {
	f := newFixture(t)
	p := f.onboard(t, "user_a", "Ana")
	f.checkInAt(t, p, "patio", time.Now())

	w := f.get("/e/cnh?spot=not-a-real-code", as("user_a"))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Contains(t, visible(w.Body.String()), "That code isn't from this event")
	zone := f.person(t, "user_a").ZoneID
	require.NotNil(t, zone)
	assert.Equal(t, "patio", *zone)
}

func TestCodeFromAnotherEventIsUnknown(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")
	storetest.Exec(t, f.store, `INSERT INTO event (slug, name, timezone, starts_at, ends_at)
		VALUES ('other', 'Other', 'America/Denver', now(), now() + interval '1 day')`)
	storetest.Exec(t, f.store, `INSERT INTO zone (id, event_id, name)
		SELECT 'elsewhere', id, 'Elsewhere' FROM event WHERE slug = 'other'`)
	storetest.Exec(t, f.store, `INSERT INTO spot (zone_id, name, "where", capacity, qr_slug)
		VALUES ('elsewhere', 'Their table', '', 4, 'other-event-code-123')`)

	w := f.get("/e/cnh?spot=other-event-code-123", as("user_a"))

	assert.Equal(t, http.StatusNotFound, w.Code)
	assert.Nil(t, f.person(t, "user_a").ZoneID)
}

func TestS2_CheckInOlderThan45MinutesAsksWhereAndIsNotCounted(t *testing.T) {
	f := newFixture(t)
	f.at = noon(t) // well clear of real check-ins an e2e run leaves in a kept database
	p := f.onboard(t, "user_a", "Ana")
	others := []string{"user_b", "user_c", "user_d"}
	for i, id := range others {
		f.checkInAt(t, f.onboard(t, id, fmt.Sprintf("Other %d", i)), "kitchen", f.at)
	}
	f.checkInAt(t, p, "kitchen", f.at.Add(-46*time.Minute))

	ctx := context.Background()
	e, err := f.store.EventBySlug(ctx, "cnh")
	require.NoError(t, err)
	counts, err := f.store.ZoneCounts(ctx, e.ID, f.at)
	require.NoError(t, err)
	assert.Equal(t, 3, counts["kitchen"], "the stale check-in isn't counted")

	stale := visible(f.get("/e/cnh", as("user_a")).Body.String())
	assert.Contains(t, stale, "Where are you now?")
	assert.NotContains(t, stale, "You're in", "a stale check-in is treated as none")

	fresh := visible(f.get("/e/cnh", as("user_b")).Body.String())
	assert.NotContains(t, fresh, "Where are you now?")
	assert.Contains(t, fresh, "You're in the Kitchen")
}

func TestS2_ZoneWithFewerThanThreeShowsAFew(t *testing.T) {
	f := newFixture(t)
	f.at = noon(t) // well clear of real check-ins an e2e run leaves in a kept database
	people := []store.Person{
		f.onboard(t, "user_a", "Ana"),
		f.onboard(t, "user_b", "Ben"),
		f.onboard(t, "user_c", "Cal"),
	}
	spot := f.spot(t, "Kitchen island")

	f.checkInAt(t, people[0], "kitchen", f.at)
	f.checkInAt(t, people[1], "kitchen", f.at)
	assert.Contains(t, visible(f.get(spotPath(spot), as("user_a")).Body.String()), "a few people in the Kitchen")
	assert.Contains(t, visible(f.get("/e/cnh", as("user_a")).Body.String()), "Kitchen a few")

	f.checkInAt(t, people[2], "kitchen", f.at)
	assert.Contains(t, visible(f.get(spotPath(spot), as("user_a")).Body.String()), "3 people in the Kitchen")
	home := visible(f.get("/e/cnh", as("user_a")).Body.String())
	assert.Contains(t, home, "Kitchen 3")
	assert.Contains(t, home, "Patio a few", "an empty zone looks the same as one with one person in it")
}

func TestManualZonePickerChecksYouIn(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")
	assert.Contains(t, visible(f.get("/e/cnh", as("user_a")).Body.String()), "Where are you?")

	w := f.post("/e/cnh/checkin", url.Values{"zone": {"patio"}}, as("user_a"))

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/e/cnh", w.Header().Get("Location"))
	zone := f.person(t, "user_a").ZoneID
	require.NotNil(t, zone)
	assert.Equal(t, "patio", *zone)
	assert.Contains(t, visible(f.get("/e/cnh", as("user_a")).Body.String()), "You're in the Patio")
}

func TestManualCheckInRefusesAnUnknownZoneVisitorsAndCrossSiteRequests(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")

	assert.Equal(t, http.StatusUnprocessableEntity, f.post("/e/cnh/checkin", url.Values{"zone": {"mars"}}, as("user_a")).Code)
	assert.Nil(t, f.person(t, "user_a").ZoneID)
	assert.Equal(t, http.StatusUnauthorized, f.post("/e/cnh/checkin", url.Values{"zone": {"patio"}}).Code)
	assert.Equal(t, http.StatusForbidden, f.post("/e/cnh/checkin", url.Values{"zone": {"patio"}}, as("user_a"),
		header("Sec-Fetch-Site", "cross-site")).Code)
	assert.Nil(t, f.person(t, "user_a").ZoneID)
}

func TestManualCheckInNeedsAName(t *testing.T) {
	f := newFixture(t)
	f.get("/e/cnh", as("user_new"))

	w := f.post("/e/cnh/checkin", url.Values{"zone": {"patio"}}, as("user_new"))

	assert.Equal(t, http.StatusForbidden, w.Code)
	assert.Nil(t, f.person(t, "user_new").ZoneID)
}

func TestSpotPageChecksInOnlyWhenTheBrowserIsOpeningThePage(t *testing.T) {
	cases := []struct {
		name      string
		opts      []requestOpt
		checkedIn bool
	}{
		{"a scanned code", []requestOpt{header("Sec-Fetch-Dest", "document"), header("Sec-Fetch-Site", "none")}, true},
		{"an old browser with no fetch metadata", nil, true},
		{"an image tag on another site", []requestOpt{header("Sec-Fetch-Dest", "image"), header("Sec-Fetch-Site", "cross-site")}, false},
		{"an iframe", []requestOpt{header("Sec-Fetch-Dest", "iframe"), header("Sec-Fetch-Site", "cross-site")}, false},
		{"a browser prefetch", []requestOpt{header("Sec-Fetch-Dest", "document"), header("Sec-Purpose", "prefetch")}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.onboard(t, "user_a", "Ana")
			spot := f.spot(t, "Couches")

			w := f.get(spotPath(spot), append(tc.opts, as("user_a"))...)

			require.Equal(t, http.StatusOK, w.Code)
			assert.Equal(t, tc.checkedIn, f.person(t, "user_a").ZoneID != nil)
			assert.Equal(t, tc.checkedIn, strings.Contains(visible(w.Body.String()), "You're at"))
		})
	}
}

func TestOrganizersCheckInButAreNotOfferedJoinOrStart(t *testing.T) {
	f := newFixture(t)
	now := noon(t)
	f.at = now
	f.meta["user_org"] = auth.Metadata{Role: "organizer"}
	host := f.onboard(t, "user_host", "Dana")
	f.onboard(t, "user_org", "Olu")
	spot := f.spot(t, "Couches")
	f.spaceAt(t, host, spot, "First one next", now.Add(30*time.Minute), 30)

	body := f.get(spotPath(spot), as("user_org")).Body.String()

	assert.Contains(t, visible(body), "You're at Couches, Lounge")
	assert.NotContains(t, body, "/join")
	assert.NotContains(t, body, "/spaces/new")
}

func TestHostAndGoingPeopleSeeTheirPlaceInsteadOfJoin(t *testing.T) {
	f := newFixture(t)
	now := noon(t)
	f.at = now
	host := f.onboard(t, "user_host", "Dana")
	spot := f.spot(t, "Couches")
	f.spaceAt(t, host, spot, "First one next", now.Add(30*time.Minute), 30)

	body := f.get(spotPath(spot), as("user_host")).Body.String()

	assert.Contains(t, visible(body), "Hosting")
	assert.NotContains(t, body, "/join")
}

func TestSpotTitlesAreEscaped(t *testing.T) {
	f := newFixture(t)
	now := noon(t)
	f.at = now
	host := f.onboard(t, "user_host", "<b>Dana</b>")
	spot := f.spot(t, "Couches")
	f.spaceAt(t, host, spot, "<script>alert(1)</script>", now.Add(30*time.Minute), 30)

	body := f.get(spotPath(spot), as("user_host")).Body.String()

	assert.NotContains(t, body, "<script>alert(1)")
	assert.NotContains(t, body, "<b>Dana")
}

func TestS2_NoResponseContainsAnotherPersonsZone(t *testing.T) {
	f := newFixture(t)
	now := noon(t)
	f.at = now
	host := f.onboard(t, "user_host", "Dana")
	other := f.onboard(t, "user_other", "Zeb")
	f.onboard(t, "user_a", "Ana")
	spot := f.spot(t, "Long table")
	sp := f.spaceAt(t, host, spot, "A space Zeb is going to", now.Add(30*time.Minute), 30)
	addSignup(t, f, sp.ID, other.ID, store.SignupGoing)
	f.checkInAt(t, other, "patio", now)
	f.checkInAt(t, host, "booths", now)

	updates := f.subscribe(t, live.SpotTopic(spot.ID))
	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/spaces/"+sp.Slug+"/join", nil, as("user_a")).Code)
	fragment := expectFragment(t, updates, "space-"+sp.ID)

	responses := map[string]string{
		"spot page":   f.get(spotPath(spot), as("user_a")).Body.String(),
		"board":       f.get(boardPath(spot), as("user_a")).Body.String(),
		"space page":  f.get("/e/cnh/spaces/"+sp.Slug, as("user_a")).Body.String(),
		"seats":       f.get("/e/cnh/spaces/"+sp.Slug+"/seats", as("user_a")).Body.String(),
		"live update": fragment,
	}
	for name, body := range responses {
		assert.NotContains(t, body, "Patio", name)
		assert.NotContains(t, body, "patio", name)
		assert.NotContains(t, body, "Sponsor row", name)
		assert.NotContains(t, body, "booths", name)
	}
}
