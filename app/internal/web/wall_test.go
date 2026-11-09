package web_test

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/web"
)

const (
	wallPath  = "/e/cnh/wall"
	boardPath = "/e/cnh/wall/board"
)

// fakeTags stands in for the Elasticsearch aggregation behind the Wall's tag strip.
type fakeTags struct {
	mu    sync.Mutex
	tags  []search.TagCount
	err   error
	calls int
}

func (f *fakeTags) TopTags(context.Context, time.Time, int) ([]search.TagCount, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.tags, f.err
}

func (f *fixture) wallBody(t *testing.T, path string, opts ...requestOpt) string {
	t.Helper()
	w := f.get(path, opts...)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w.Body.String()
}

func TestS9_WallShowsTheNextTwoHoursOfEveryZoneInEightFifteenMinuteSlots(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_host", "Host")
	inside := f.spaceForm(t, "Long table", soon(t, 30), "30", "8")
	inside.Set("title", "Inside the window")
	f.createSpace(t, "user_host", inside)
	beyond := f.spaceForm(t, "Long table", soon(t, 150), "30", "8")
	beyond.Set("title", "Beyond the window")
	f.createSpace(t, "user_host", beyond)

	body := f.wallBody(t, wallPath)

	assert.Equal(t, 8, strings.Count(body, "data-slot"))
	event, err := f.store.EventBySlug(context.Background(), "cnh")
	require.NoError(t, err)
	spots, err := f.store.Spots(context.Background(), event.ID)
	require.NoError(t, err)
	zones, err := f.store.ZoneNames(context.Background(), event.ID)
	require.NoError(t, err)
	require.NotEmpty(t, spots)
	for _, spot := range spots {
		assert.Contains(t, body, spot.Name)
		assert.Contains(t, body, zones[spot.ZoneID])
	}
	assert.Equal(t, len(spots), strings.Count(body, `class="wall-spot"`))
	assert.Contains(t, body, "Inside the window")
	assert.Contains(t, body, "wall-note-title")
	assert.NotContains(t, body, "Beyond the window")
}

func TestS9_WallIsOpenToVisitorsAtItsOwnURL(t *testing.T) {
	f := newFixture(t)
	assert.Equal(t, http.StatusOK, f.get(wallPath).Code)
	assert.Equal(t, http.StatusOK, f.get(boardPath).Code)
	assert.Equal(t, http.StatusNotFound, f.get("/e/nope/wall").Code)
}

func TestS9_SignupAnywhereChangesTheWallsCountWithinTwoSeconds(t *testing.T) {
	f := newFixture(t)
	path := f.createSpace(t, "user_host", func() url.Values {
		f.onboard(t, "user_host", "Host")
		return f.spaceForm(t, "Long table", soon(t, 30), "30", "8")
	}())
	f.onboard(t, "user_a", "Ana")
	assert.Contains(t, f.wallBody(t, boardPath), "1 of 8")
	wall := f.subscribe(t, live.EventTopic)

	require.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_a")).Code)

	expectFragment(t, wall, "wall-changed")
	assert.Contains(t, f.wallBody(t, boardPath), "2 of 8")
}

func TestS9_WallShowsNoPersonNamesOnlyTitlesSpotsAndCounts(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_host", "Zelda Quimby")
	path := f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 30), "30", "8"))
	f.onboard(t, "user_a", "Ansel Rosario")
	require.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_a")).Code)
	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/posts",
		url.Values{"kind": {"stuck"}, "text": {"our collector drops spans under load"}}, as("user_a")).Code)

	for name, opts := range map[string][]requestOpt{"visitor": nil, "signed in": {as("user_a")}} {
		t.Run(name, func(t *testing.T) {
			for _, target := range []string{wallPath, boardPath} {
				body := f.wallBody(t, target, opts...)
				assert.Contains(t, body, "Collector tuning")
				assert.Contains(t, body, "Long table")
				assert.Contains(t, body, "2 of 8")
				assert.NotContains(t, body, "Zelda")
				assert.NotContains(t, body, "Quimby")
				assert.NotContains(t, body, "Ansel")
				assert.NotContains(t, body, "Rosario")
				assert.NotContains(t, body, "person:", "a projector must not follow anyone's own topic")
			}
		})
	}
}

// A fragment with its own SSE listeners would leak one per card per refresh, because the extension
// only removes a listener when that card's event fires again. The Wall keeps one, on an element that
// is never replaced.
func TestS9_WallStaysFlatBecauseRefreshesAddNoListeners(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_host", "Host")
	f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 30), "30", "8"))

	board := f.wallBody(t, boardPath)
	assert.NotContains(t, board, "sse-swap")
	assert.NotContains(t, board, "hx-trigger")
	assert.NotContains(t, board, "hx-get")

	page := f.wallBody(t, wallPath)
	assert.Equal(t, 1, strings.Count(page, "sse-connect"))
	assert.Equal(t, 1, strings.Count(page, "sse:wall-changed"))
}

func TestS9_WallConnectsToTheEventTopicAndReconnectsOnItsOwn(t *testing.T) {
	f := newFixture(t)

	page := f.wallBody(t, wallPath)

	assert.Contains(t, page, `sse-connect="`+live.URL("cnh", live.EventTopic)+`"`)
	assert.Contains(t, page, "live.js", "live.js reopens a dropped stream and refetches the board")
	assert.Contains(t, page, "data-live-note")
	assert.Contains(t, page, "Reconnecting…")
}

func TestWallNewAndCancelledSpacesReachTheWall(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_host", "Host")
	wall := f.subscribe(t, live.EventTopic)

	path := f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 30), "30", "8"))
	expectFragment(t, wall, "wall-changed")
	assert.Contains(t, f.wallBody(t, boardPath), "Collector tuning")

	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_host")).Code)
	expectFragment(t, wall, "wall-changed")
	assert.NotContains(t, f.wallBody(t, boardPath), "Collector tuning")
}

func TestWallTickerShowsTheLatestPostsAsPlainText(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ansel")
	wall := f.subscribe(t, live.EventTopic)

	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/posts",
		url.Values{"kind": {"offer"}, "text": {"<script>alert(1)</script> kube-scheduler profiles"}}, as("user_a")).Code)

	expectFragment(t, wall, "wall-changed")
	body := f.wallBody(t, boardPath)
	assert.Contains(t, body, "Just posted")
	assert.Contains(t, body, "Can help:")
	assert.Contains(t, body, "&lt;script&gt;")
	assert.NotContains(t, body, "<script>alert")
}

func TestWallNamesTheNextActionWhenNothingIsOn(t *testing.T) {
	f := newFixture(t)

	body := f.wallBody(t, boardPath)

	assert.Contains(t, body, "Whoever comes are the right people.")
	assert.Contains(t, body, `href="/e/cnh/spaces/new"`)
	assert.NotContains(t, body, "Just posted", "an empty ticker has nothing to label")
}

func TestD42_WallHeaderHasAQRForTheEventWithNothingPersonalInIt(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ansel")
	want, _, err := web.EventQRPath("https://twofeet.test/e/cnh")
	require.NoError(t, err)

	for name, opts := range map[string][]requestOpt{"visitor": nil, "signed in": {as("user_a")}} {
		t.Run(name, func(t *testing.T) {
			body := f.wallBody(t, boardPath, opts...)
			assert.Contains(t, body, want, "the code encodes the event's own URL for everyone")
			assert.Contains(t, body, "Scan to post or start a space")
		})
	}
}

func TestD26_WallShowsTopTagsWithCountsOnly(t *testing.T) {
	f := newFixture(t)
	f.tags.tags = []search.TagCount{{Tag: "otel-collector", Count: 4}, {Tag: "k8s", Count: 2}}

	body := f.wallBody(t, boardPath)

	assert.Contains(t, body, "What the room is stuck on")
	assert.Contains(t, body, "otel-collector")
	assert.Contains(t, body, ">4<")
	assert.Contains(t, body, "k8s")
}

func TestD26_WallAsksElasticsearchAboutOnceAMinute(t *testing.T) {
	f := newFixture(t)
	f.tags.tags = []search.TagCount{{Tag: "k8s", Count: 2}}

	f.wallBody(t, boardPath)
	f.wallBody(t, boardPath)
	f.wallBody(t, wallPath)

	assert.Equal(t, 1, f.tags.calls)
}

func TestD26_StripIsHiddenWhenElasticsearchIsDownAndTheWallStillWorks(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_host", "Host")
	f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 30), "30", "8"))
	f.tags.err = errors.New("elasticsearch is down")

	body := f.wallBody(t, boardPath)

	assert.NotContains(t, body, "What the room is stuck on")
	assert.Contains(t, body, "Collector tuning")
}

func TestD26_StripIsHiddenWhenThereAreNoTags(t *testing.T) {
	f := newFixture(t)

	assert.NotContains(t, f.wallBody(t, boardPath), "What the room is stuck on")
}
