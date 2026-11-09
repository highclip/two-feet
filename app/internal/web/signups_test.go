package web_test

import (
	"bufio"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"runtime"
	"strings"
	"sync"
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

var htmxRequest = header("HX-Request", "true")

// joinableSpace is a space of the given size, starting in an hour, with user_host as its host.
func (f *fixture) joinableSpace(t *testing.T, capacity string) (path, id string) {
	t.Helper()
	f.onboard(t, "user_host", "Host")
	path = f.createSpace(t, "user_host", f.spaceForm(t, "Long table", soon(t, 60), "30", capacity))
	return path, spaceID(t, f, path)
}

// soon is a form start time some minutes from now in Denver.
func soon(t *testing.T, minutes int) string {
	t.Helper()
	return time.Now().In(denver(t)).Add(time.Duration(minutes) * time.Minute).Format("2006-01-02T15:04")
}

func (f *fixture) signupStatus(t *testing.T, space, person string) string {
	t.Helper()
	return storetest.QueryString(t, f.store, `SELECT status FROM signup WHERE space_id = $1 AND person_id = $2`, space, person)
}

func (f *fixture) subscribe(t *testing.T, topic string) <-chan live.Fragment {
	t.Helper()
	ch, cancel := f.hub.Subscribe(context.Background(), []string{topic})
	t.Cleanup(cancel)
	return ch
}

func expectFragment(t *testing.T, ch <-chan live.Fragment, event string) string {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		select {
		case got := <-ch:
			if got.Event == event {
				return string(got.HTML)
			}
		case <-deadline:
			t.Fatalf("no %s fragment within 2 seconds", event)
			return ""
		}
	}
}

func TestS4_FullSpaceSomeoneLeavesFirstWaitlistedSeesYoureInWithinTwoSeconds(t *testing.T) {
	f := newFixture(t)
	path, id := f.joinableSpace(t, "2")
	f.onboard(t, "user_a", "Ana")
	b := f.onboard(t, "user_b", "Ben")
	require.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_a")).Code)
	require.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_b")).Code)
	require.Equal(t, "waitlist", f.signupStatus(t, id, b.ID))
	notices := f.subscribe(t, live.PersonTopic(b.ID))

	w := f.post(path+"/leave", nil, as("user_a"))
	require.Equal(t, http.StatusSeeOther, w.Code)

	assert.Contains(t, expectFragment(t, notices, "notice"), "You&#39;re in")
	assert.Equal(t, "going", f.signupStatus(t, id, b.ID))
	assert.Contains(t, f.get(path, as("user_b")).Body.String(), "You're in!")
}

func TestS4_OtherBrowsersCountUpdatesWithoutAReload(t *testing.T) {
	f := newFixture(t)
	path, id := f.joinableSpace(t, "8")
	f.onboard(t, "user_a", "Ana")
	other := f.subscribe(t, live.SpaceTopic(id))

	f.post(path+"/join", nil, as("user_a"))

	html := expectFragment(t, other, "space-"+id)
	assert.Contains(t, html, "2 of 8 going")
	assert.NotContains(t, html, "Ana", "public topics carry counts only")
}

func TestS4_JoinFromHtmxReturnsTheLiveSectionNotARedirect(t *testing.T) {
	f := newFixture(t)
	path, id := f.joinableSpace(t, "8")
	a := f.onboard(t, "user_a", "Ana")

	w := f.post(path+"/join", nil, as("user_a"), htmxRequest)

	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "2 of 8 going")
	assert.Contains(t, w.Body.String(), "You're in!")
	assert.Contains(t, w.Body.String(), ">Leave<")
	assert.Equal(t, "going", f.signupStatus(t, id, a.ID))
}

func TestWaitlistedPersonIsToldTheirPlaceInLine(t *testing.T) {
	f := newFixture(t)
	path, _ := f.joinableSpace(t, "2")
	f.onboard(t, "user_a", "Ana")
	f.onboard(t, "user_b", "Ben")
	f.onboard(t, "user_c", "Cy")
	f.post(path+"/join", nil, as("user_a"))
	f.post(path+"/join", nil, as("user_b"))

	w := f.post(path+"/join", nil, as("user_c"), htmxRequest)

	assert.Contains(t, w.Body.String(), "Full. You're 2nd on the waitlist; we'll move you in if a seat opens.")
	assert.Contains(t, w.Body.String(), "Leave the waitlist")
}

func TestS4_JoiningAnOverlappingSpaceAsksInlineToLeaveTheFirst(t *testing.T) {
	f := newFixture(t)
	first, firstID := f.joinableSpace(t, "4")
	other := f.spaceForm(t, "Couches", soon(t, 75), "30", "4")
	other.Set("title", "Tail sampling")
	second := f.createSpace(t, "user_host", other)
	secondID := spaceID(t, f, second)
	a := f.onboard(t, "user_a", "Ana")
	require.Equal(t, http.StatusSeeOther, f.post(first+"/join", nil, as("user_a")).Code)

	w := f.post(second+"/join", nil, as("user_a"), htmxRequest)

	require.Equal(t, http.StatusOK, w.Code, "htmx swaps the prompt in place of a dialog")
	assert.Contains(t, w.Body.String(), "You&#39;re going to Collector tuning")
	assert.Contains(t, w.Body.String(), `name="leave"`)
	assert.Contains(t, w.Body.String(), "Leave it and join")
	assert.Equal(t, "going", f.signupStatus(t, firstID, a.ID), "asked, not moved")

	plain := f.post(second+"/join", nil, as("user_a"))
	assert.Equal(t, http.StatusConflict, plain.Code, "a form post without htmx gets the prompt on the page")
	assert.Contains(t, plain.Body.String(), "Leave it and join")

	moved := f.post(second+"/join", url.Values{"leave": {firstID}}, as("user_a"))
	assert.Equal(t, http.StatusSeeOther, moved.Code)
	assert.Equal(t, "left", f.signupStatus(t, firstID, a.ID))
	assert.Equal(t, "going", f.signupStatus(t, secondID, a.ID))
}

func TestJoinAndLeaveNeedAPersonAndTheRightRole(t *testing.T) {
	f := newFixture(t)
	path, _ := f.joinableSpace(t, "4")

	assert.Equal(t, http.StatusUnauthorized, f.post(path+"/join", nil).Code, "visitor")
	assert.Equal(t, http.StatusUnauthorized, f.post(path+"/leave", nil).Code, "visitor")

	f.meta["user_org"] = auth.Metadata{Role: "organizer"}
	f.onboard(t, "user_org", "Olu")
	assert.Equal(t, http.StatusForbidden, f.post(path+"/join", nil, as("user_org")).Code, "organizer")

	f.meta["user_rep"] = auth.Metadata{Role: "sponsor_rep", Sponsor: "Dash0"}
	f.onboard(t, "user_rep", "Rae")
	assert.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_rep")).Code, "sponsor rep")

	f.get("/e/cnh", as("user_nameless")) // creates the row, with no display name yet
	assert.Equal(t, http.StatusForbidden, f.post(path+"/join", nil, as("user_nameless")).Code)
}

func TestJoinPagesShowVisitorsASignInLinkAndNoNames(t *testing.T) {
	f := newFixture(t)
	path, _ := f.joinableSpace(t, "4")

	page := f.get(path).Body.String()

	assert.Contains(t, page, auth.SignInURL(path))
	assert.NotContains(t, page, "Host, hosting")
	assert.NotContains(t, page, `action="`+path+`/join"`)
}

func TestSignupsAreLimitedToThirtyInTenMinutes(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_a", "Ana")
	host := f.onboard(t, "user_host", "Host")
	spot := f.spotID(t, "Long table")
	paths := make([]string, 0, 31)
	ids := make([]string, 0, 31)
	for i := range 31 {
		// Straight through the store, because S3 limits how many spaces one person can start.
		space, err := f.store.CreateSpace(context.Background(), host.ID, store.SpaceInput{
			Title: fmt.Sprintf("Space number %d", i), SpotID: spot,
			StartsAt: time.Now().AddDate(0, 0, 1+i), Minutes: 15, Capacity: 4,
		})
		require.NoError(t, err)
		paths = append(paths, "/e/cnh/spaces/"+space.Slug)
		ids = append(ids, space.ID)
	}
	for i, path := range paths[:30] {
		require.Equal(t, http.StatusSeeOther, f.post(path+"/join", nil, as("user_a")).Code, "join %d", i)
	}

	w := f.post(paths[30]+"/join", nil, as("user_a"), htmxRequest)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "You can join another at")
	assert.Equal(t, "1", storetest.QueryString(t, f.store,
		`SELECT count(*)::text FROM signup WHERE space_id = $1 AND status = 'going'`, ids[30]),
		"the refused join changed nothing")
}

func TestLeaveToastOffersUndoOnlyWhenNobodyWasPromoted(t *testing.T) {
	f := newFixture(t)
	path, id := f.joinableSpace(t, "3")
	a := f.onboard(t, "user_a", "Ana")
	f.onboard(t, "user_b", "Ben")
	f.post(path+"/join", nil, as("user_a"))

	left := f.post(path+"/leave", nil, as("user_a"), htmxRequest)
	assert.Contains(t, left.Body.String(), "You&#39;ve left.")
	assert.Contains(t, left.Body.String(), "/leave/undo")

	undo := f.post(path+"/leave/undo", nil, as("user_a"), htmxRequest)
	assert.Equal(t, http.StatusOK, undo.Code)
	assert.Equal(t, "going", f.signupStatus(t, id, a.ID))

	// Now fill the space and put someone on the waitlist, so the next leave promotes them.
	f.post(path+"/join", nil, as("user_b"))
	f.onboard(t, "user_c", "Cy")
	f.post(path+"/join", nil, as("user_c"))
	promoted := f.post(path+"/leave", nil, as("user_a"), htmxRequest)
	assert.Contains(t, promoted.Body.String(), "your seat went to the next person")
	assert.NotContains(t, promoted.Body.String(), "/leave/undo")
}

func TestUndoLeaveIsTooLateOnceTheWindowHasClosedAndHarmlessWhenAlreadyBack(t *testing.T) {
	f := newFixture(t)
	path, id := f.joinableSpace(t, "3")
	a := f.onboard(t, "user_a", "Ana")
	addSignup(t, f, id, a.ID, "left") // left long ago: there's no window open for this

	assert.Equal(t, http.StatusConflict, f.post(path+"/leave/undo", nil, as("user_a")).Code)

	f.post(path+"/join", nil, as("user_a"))
	assert.Equal(t, http.StatusSeeOther, f.post(path+"/leave/undo", nil, as("user_a")).Code,
		"a second tap lands here with them already back in")
	assert.Equal(t, "going", f.signupStatus(t, id, a.ID))
}

func TestPublishesAndNoticesFollowASwitchBetweenSpaces(t *testing.T) {
	f := newFixture(t)
	first, firstID := f.joinableSpace(t, "2")
	otherForm := f.spaceForm(t, "Couches", soon(t, 75), "30", "4")
	second := f.createSpace(t, "user_host", otherForm)
	f.onboard(t, "user_a", "Ana")
	waiting := f.onboard(t, "user_b", "Ben")
	f.post(first+"/join", nil, as("user_a"))
	f.post(first+"/join", nil, as("user_b"))
	notices := f.subscribe(t, live.PersonTopic(waiting.ID))
	firstCounts := f.subscribe(t, live.SpaceTopic(firstID))

	f.post(second+"/join", url.Values{"leave": {firstID}}, as("user_a"))

	assert.Contains(t, expectFragment(t, notices, "notice"), "You&#39;re in")
	assert.Contains(t, expectFragment(t, firstCounts, "space-"+firstID), "2 of 2 going")
}

func TestRaisingCapacityMovesTheWaitlistInAndTellsThem(t *testing.T) {
	f := newFixture(t)
	path, _ := f.joinableSpace(t, "2")
	f.onboard(t, "user_a", "Ana")
	waiting := f.onboard(t, "user_b", "Ben")
	f.post(path+"/join", nil, as("user_a"))
	f.post(path+"/join", nil, as("user_b"))
	notices := f.subscribe(t, live.PersonTopic(waiting.ID))

	starts := storetest.QueryString(t, f.store,
		`SELECT to_char(starts_at AT TIME ZONE 'America/Denver', 'YYYY-MM-DD"T"HH24:MI') FROM space WHERE slug = $1`,
		strings.TrimPrefix(path, "/e/cnh/spaces/"))
	require.Equal(t, http.StatusSeeOther,
		f.post(path+"/edit", f.spaceForm(t, "Long table", starts, "30", "3"), as("user_host")).Code)

	assert.Contains(t, expectFragment(t, notices, "notice"), "You&#39;re in")
}

// waitForEvent reads SSE lines until one matches, or fails the test via the channel closing.
func waitForEvent(stream *bufio.Reader, want string, done chan<- time.Time) {
	for {
		line, err := stream.ReadString('\n')
		if err != nil {
			return
		}
		if strings.Contains(line, want) {
			done <- time.Now()
			return
		}
	}
}

func connectMany(t *testing.T, f *fixture, srv *httptest.Server, n int) []*bufio.Reader {
	t.Helper()
	streams := make([]*bufio.Reader, n)
	for i := range streams {
		streams[i] = openStream(t, srv, live.URL("cnh", live.EventTopic), nil)
	}
	return streams
}

func fanOut(f *fixture, streams []*bufio.Reader, event string) time.Duration {
	arrived := make(chan time.Time, len(streams))
	var wg sync.WaitGroup
	wg.Add(len(streams))
	for _, s := range streams {
		go func() {
			defer wg.Done()
			waitForEvent(s, "event: "+event, arrived)
		}()
	}
	start := time.Now()
	f.hub.Publish(live.EventTopic, live.Fragment{Event: event, HTML: []byte("<p>2 of 8 going</p>")})
	wg.Wait()
	close(arrived)
	var slowest time.Time
	for at := range arrived {
		if at.After(slowest) {
			slowest = at
		}
	}
	return slowest.Sub(start)
}

func heapInUse() uint64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc + m.StackInuse
}

func TestS4_HundredOpenConnectionsStayUnderMemoryAndFanOutBudget(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close)
	before := heapInUse()

	streams := connectMany(t, f, srv, 100)

	grown := int64(heapInUse()) - int64(before)
	assert.Less(t, grown, int64(50<<20), "100 connections grew memory by %d bytes", grown)
	assert.Less(t, fanOut(f, streams, "count"), 200*time.Millisecond)
}

func TestLive_ThreeHundredConnectionsReachEverySubscriberInUnder500ms(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close)

	streams := connectMany(t, f, srv, 300)

	assert.Less(t, fanOut(f, streams, "count"), 500*time.Millisecond)
}

func TestLive_HeartbeatKeepsTheStreamOpenAndIsVisibleToThePage(t *testing.T) {
	t.Cleanup(web.SetHeartbeatEvery(20 * time.Millisecond)) // runs after the server below has stopped
	f := newFixture(t)
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close)

	stream := openStream(t, srv, live.URL("cnh", live.EventTopic), nil)

	got := make(chan bool, 1)
	go func() {
		var comment, event bool
		for !comment || !event {
			line, err := stream.ReadString('\n')
			if err != nil {
				return
			}
			comment = comment || strings.HasPrefix(line, ": heartbeat")
			event = event || strings.HasPrefix(line, "event: heartbeat")
		}
		got <- true
	}()
	select {
	case <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no heartbeat comment and event")
	}
}
