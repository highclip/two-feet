package web_test

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/auth"
	"github.com/highclip/two-feet/app/internal/live"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

// spaceIndexer stands in for Elasticsearch and can be told to fail.
type spaceIndexer struct {
	mu      sync.Mutex
	docs    map[string]search.Doc
	deleted []string
	fail    bool
}

func (i *spaceIndexer) Index(_ context.Context, d search.Doc) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.fail {
		return errors.New("elasticsearch is down")
	}
	if i.docs == nil {
		i.docs = map[string]search.Doc{}
	}
	i.docs[d.Type+"/"+d.ID] = d
	return nil
}

func (i *spaceIndexer) Delete(_ context.Context, docType, id string) error {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.deleted = append(i.deleted, docType+"/"+id)
	return nil
}

func (i *spaceIndexer) doc(docType, id string) (search.Doc, bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	d, ok := i.docs[docType+"/"+id]
	return d, ok
}

// denver is the event's timezone, the one forms are filled in.
func denver(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/Denver")
	require.NoError(t, err)
	return loc
}

// tomorrowAt is a datetime-local value tomorrow in Denver, so it's never in the past.
func tomorrowAt(t *testing.T, hour, minute int) string {
	t.Helper()
	now := time.Now().In(denver(t)).AddDate(0, 0, 1)
	return time.Date(now.Year(), now.Month(), now.Day(), hour, minute, 0, 0, now.Location()).Format("2006-01-02T15:04")
}

func (f *fixture) spotID(t *testing.T, name string) string {
	t.Helper()
	return storetest.QueryString(t, f.store, `SELECT id::text FROM spot WHERE name = $1`, name)
}

func (f *fixture) spaceForm(t *testing.T, spot, starts, minutes, capacity string) url.Values {
	t.Helper()
	return url.Values{
		"title": {"Collector tuning"}, "blurb": {"Bring your worst config."},
		"spot": {f.spotID(t, spot)}, "starts": {starts}, "minutes": {minutes}, "capacity": {capacity},
	}
}

// createSpace makes a space through the handler and returns its path.
func (f *fixture) createSpace(t *testing.T, clerkUserID string, form url.Values) string {
	t.Helper()
	w := f.post("/e/cnh/spaces", form, as(clerkUserID))
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	path := w.Header().Get("Location")
	require.True(t, strings.HasPrefix(path, "/e/cnh/spaces/"), path)
	return path
}

func spaceID(t *testing.T, f *fixture, path string) string {
	t.Helper()
	slug := strings.TrimPrefix(path, "/e/cnh/spaces/")
	return storetest.QueryString(t, f.store, `SELECT id::text FROM space WHERE slug = $1`, slug)
}

func addSignup(t *testing.T, f *fixture, space, person, status string) {
	t.Helper()
	storetest.Exec(t, f.store, `INSERT INTO signup (space_id, person_id, status) VALUES ($1, $2, $3)`, space, person, status)
}

func TestS3_CreatedSpaceAppearsWithCreatorAsHostAndFirstGoing(t *testing.T) {
	f := newFixture(t)
	host := f.onboard(t, "user_jun", "Jun")

	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "8"))

	assert.GreaterOrEqual(t, len(strings.TrimPrefix(path, "/e/cnh/spaces/")), 16, "slugs are unguessable")
	page := f.get(path, as("user_jun"))
	require.Equal(t, http.StatusOK, page.Code)
	body := page.Body.String()
	assert.Contains(t, body, "Collector tuning")
	assert.Contains(t, body, "Long table")
	assert.Contains(t, body, "Jun, hosting")
	assert.Contains(t, body, "1 of 8 going")

	id := spaceID(t, f, path)
	assert.Equal(t, host.ID, storetest.QueryString(t, f.store, `SELECT host_id::text FROM space WHERE id = $1`, id))
	assert.Equal(t, "going", storetest.QueryString(t, f.store,
		`SELECT status FROM signup WHERE space_id = $1 AND person_id = $2`, id, host.ID))
}

func TestS3_CreateAcceptsEachLengthAndDefaultsCapacityToTheSpot(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	for i, minutes := range []string{"15", "30", "45"} {
		path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 9+i*2, 0), minutes, ""))
		assert.Contains(t, f.get(path, as("user_jun")).Body.String(), "1 of 10 going")
	}
	bad := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", tomorrowAt(t, 16, 0), "20", ""), as("user_jun"))
	assert.Equal(t, http.StatusUnprocessableEntity, bad.Code)
}

func TestS3_OverlappingSpaceIsRefusedSayingUntilWhenAndWhereToTry(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	f.onboard(t, "user_kofi", "Kofi")
	f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))

	w := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 15), "30", ""), as("user_kofi"))

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "Long table is taken until 2:30. Try Couches.")
	assert.Contains(t, w.Body.String(), `value="Collector tuning"`, "what they typed stays in the form")

	touching := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 30), "15", ""), as("user_kofi"))
	assert.Equal(t, http.StatusSeeOther, touching.Code, "a space may start the minute another ends")
}

func TestS3_OverlapSuggestionSkipsBusySpotsAndSaysWhenNoneIsFree(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	starts := tomorrowAt(t, 10, 0)
	f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", starts, "30", ""))
	f.createSpace(t, "user_jun", f.spaceForm(t, "Couches", starts, "30", ""))

	w := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", starts, "30", ""), as("user_jun"))
	assert.Contains(t, w.Body.String(), "Long table is taken until 10:30. Try Kitchen island.")

	f.createSpace(t, "user_jun", f.spaceForm(t, "Kitchen island", starts, "30", ""))
	f.createSpace(t, "user_jun", f.spaceForm(t, "Picnic table", starts, "30", ""))
	f.createSpace(t, "user_jun", f.spaceForm(t, "Window table", starts, "30", ""))
	// Five creations is the limit, so this last refusal comes from a second person.
	f.onboard(t, "user_kofi", "Kofi")
	full := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", starts, "30", ""), as("user_kofi"))
	assert.Contains(t, full.Body.String(), "Long table is taken until 10:30. Pick another time.")
}

func TestEditSuggestionCanOfferTheSpotTheSpaceIsLeaving(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	starts := tomorrowAt(t, 10, 0)
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", starts, "30", ""))
	f.createSpace(t, "user_jun", f.spaceForm(t, "Couches", starts, "30", ""))
	// Kitchen island sorts before Long table, so it has to be busy for Long table to be next.
	f.createSpace(t, "user_jun", f.spaceForm(t, "Kitchen island", starts, "30", ""))

	w := f.post(path+"/edit", f.spaceForm(t, "Couches", starts, "30", ""), as("user_jun"))

	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "Couches is taken until 10:30. Try Long table.")
}

func TestS3_CapacityLargerThanTheSpotIsRefused(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")

	w := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "11"), as("user_jun"))
	assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
	assert.Contains(t, w.Body.String(), "Long table seats 10. Pick 10 or fewer.")

	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "10"))
	edit := f.post(path+"/edit", f.spaceForm(t, "Window table", tomorrowAt(t, 14, 0), "30", "10"), as("user_jun"))
	assert.Equal(t, http.StatusUnprocessableEntity, edit.Code)
	assert.Contains(t, edit.Body.String(), "Window table seats 6. Pick 6 or fewer.")
}

func TestS3_SomeoneOtherThanHostOrOrganizerGets403(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	f.onboard(t, "user_kofi", "Kofi")
	f.meta["user_rep"] = auth.Metadata{Role: "sponsor_rep", Sponsor: "Dash0"}
	f.onboard(t, "user_rep", "Rep")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))
	changed := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")
	changed.Set("title", "Hijacked title")

	for _, person := range []string{"user_kofi", "user_rep"} {
		assert.Equal(t, http.StatusForbidden, f.get(path+"/edit", as(person)).Code, person)
		assert.Equal(t, http.StatusForbidden, f.post(path+"/edit", changed, as(person)).Code, person)
		assert.Equal(t, http.StatusForbidden, f.post(path+"/cancel", nil, as(person)).Code, person)
		assert.Equal(t, http.StatusForbidden, f.post(path+"/undo", nil, as(person)).Code, person)
	}
	assert.Equal(t, http.StatusUnauthorized, f.post(path+"/cancel", nil).Code, "visitor")

	page := f.get(path, as("user_kofi")).Body.String()
	assert.NotContains(t, page, "Hijacked title")
	assert.Contains(t, page, "1 of 10 going", "still open")
	assert.NotContains(t, page, path+"/cancel", "the controls aren't offered either")
}

func TestS3_HostAndOrganizerCanEditAndCancel(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	f.meta["user_org"] = auth.Metadata{Role: "organizer"}
	f.onboard(t, "user_org", "Organizer")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))

	assert.Contains(t, f.get(path+"/edit", as("user_jun")).Body.String(), `value="Collector tuning"`)
	changed := f.spaceForm(t, "Couches", tomorrowAt(t, 15, 0), "45", "6")
	changed.Set("title", "Collector tuning, part two")
	w := f.post(path+"/edit", changed, as("user_jun"))
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.Equal(t, path, w.Header().Get("Location"))
	page := f.get(path, as("user_jun")).Body.String()
	assert.Contains(t, page, "Collector tuning, part two")
	assert.Contains(t, page, "Couches")
	assert.Contains(t, page, "1 of 6 going")

	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_org")).Code)
	assert.Contains(t, f.get(path).Body.String(), "Cancelled by host")
}

func TestS3_OrganizersAndVisitorsCannotCreate(t *testing.T) {
	f := newFixture(t)
	f.meta["user_org"] = auth.Metadata{Role: "organizer"}
	f.onboard(t, "user_org", "Organizer")
	form := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")

	assert.Equal(t, http.StatusForbidden, f.post("/e/cnh/spaces", form, as("user_org")).Code)
	assert.Equal(t, http.StatusForbidden, f.get("/e/cnh/spaces/new", as("user_org")).Code)
	assert.Equal(t, http.StatusUnauthorized, f.post("/e/cnh/spaces", form).Code)
	assert.Equal(t, http.StatusSeeOther, f.get("/e/cnh/spaces/new").Code)
}

func TestSpaceFormRedirectsSomeoneWithoutANameToOnboarding(t *testing.T) {
	f := newFixture(t)
	w := f.get("/e/cnh/spaces/new", as("user_new"))
	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Contains(t, w.Header().Get("Location"), "/e/cnh/onboarding")
	refused := f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""), as("user_new"))
	assert.Equal(t, http.StatusForbidden, refused.Code)
}

func TestS3_CancelShowsEveryoneGoingCancelledByHostWithinTwoSeconds(t *testing.T) {
	f := newFixture(t)
	host := f.onboard(t, "user_jun", "Jun")
	going := f.onboard(t, "user_priya", "Priya")
	waiting := f.onboard(t, "user_wait", "Wen")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "2"))
	id := spaceID(t, f, path)
	addSignup(t, f, id, going.ID, "going")
	addSignup(t, f, id, waiting.ID, "waitlist")

	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close) // after the streams' own cleanup: Close waits for open requests
	goingStream := openStream(t, srv, live.URL("cnh", live.PersonTopic(going.ID)), &http.Cookie{Name: "__session", Value: "token-user_priya"})
	waitingStream := openStream(t, srv, live.URL("cnh", live.PersonTopic(waiting.ID)), &http.Cookie{Name: "__session", Value: "token-user_wait"})
	hostStream := openStream(t, srv, live.URL("cnh", live.PersonTopic(host.ID)), &http.Cookie{Name: "__session", Value: "token-user_jun"})

	start := time.Now()
	w := f.post(path+"/cancel", nil, as("user_jun"))
	require.Equal(t, http.StatusSeeOther, w.Code)

	for name, stream := range map[string]*bufio.Reader{"going": goingStream, "waitlist": waitingStream} {
		body := readUntil(t, stream, "Cancelled by host", 2*time.Second-time.Since(start))
		assert.Contains(t, body, "event: notice", name)
		assert.Contains(t, body, "Collector tuning", name)
	}
	assertSilent(t, hostStream, 200*time.Millisecond, "the host did this themselves and gets the Undo toast instead")
}

func TestS3_CancelledSpaceStaysShownAsCancelledToEveryoneOnThePublicTopics(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))
	id := spaceID(t, f, path)
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close)
	stream := openStream(t, srv, live.URL("cnh", live.SpaceTopic(id)), nil)
	eventStream := openStream(t, srv, live.URL("cnh", live.EventTopic), nil)

	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)

	body := readUntil(t, stream, "Cancelled by host", 2*time.Second)
	assert.Contains(t, body, "event: space-"+id)
	assert.NotContains(t, body, "Jun", "public topics carry no names")
	readUntil(t, eventStream, "event: spaces-changed", 2*time.Second)
}

func TestNewSpacePublishesOnEventAndSpotTopics(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	spot := f.spotID(t, "Long table")
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close)
	event := openStream(t, srv, live.URL("cnh", live.EventTopic), nil)
	spotStream := openStream(t, srv, live.URL("cnh", live.SpotTopic(spot)), nil)

	f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))

	readUntil(t, event, "event: spaces-changed", 2*time.Second)
	readUntil(t, spotStream, "event: spaces-changed", 2*time.Second)
}

func TestCancelUndoRestoresTheSpaceAndTellsPeopleItIsBack(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	going := f.onboard(t, "user_priya", "Priya")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "2"))
	id := spaceID(t, f, path)
	addSignup(t, f, id, going.ID, "going")
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close)
	stream := openStream(t, srv, live.URL("cnh", live.PersonTopic(going.ID)), &http.Cookie{Name: "__session", Value: "token-user_priya"})

	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)
	readUntil(t, stream, "Cancelled by host", 2*time.Second)
	cancelled := f.get(path, as("user_jun")).Body.String()
	assert.Contains(t, cancelled, path+"/undo", "the Undo toast is offered right after cancelling")
	assert.Contains(t, cancelled, `data-expire-ms="5000"`, "and goes away by itself")

	w := f.post(path+"/undo", nil, as("user_jun"))
	require.Equal(t, http.StatusSeeOther, w.Code)
	readUntil(t, stream, "Back on", 2*time.Second)

	page := f.get(path, as("user_jun")).Body.String()
	assert.Contains(t, page, "2 of 2 going", "everyone is back in their seat")
	assert.NotContains(t, page, "Cancelled by host")
}

func TestUndoIsRefusedOnceTheMomentHasPassed(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))
	// Cancelled outside this process, so the server never offered an Undo for it.
	storetest.Exec(t, f.store, `UPDATE space SET status = 'cancelled' WHERE id = $1`, spaceID(t, f, path))

	assert.NotContains(t, f.get(path, as("user_jun")).Body.String(), path+"/undo")
	w := f.post(path+"/undo", nil, as("user_jun"))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "too late")
	assert.Contains(t, f.get(path, as("user_jun")).Body.String(), "Cancelled by host")
}

func TestUndoThatLosesARaceToAnotherUndoStillLandsOnTheSpace(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))
	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)
	// The other tap has restored the space but hasn't closed the Undo window yet.
	storetest.Exec(t, f.store, `UPDATE space SET status = 'open' WHERE id = $1`, spaceID(t, f, path))

	w := f.post(path+"/undo", nil, as("user_jun"))

	assert.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.Equal(t, path, w.Header().Get("Location"))
}

func TestUndoTappedAgainAfterItWorkedLandsOnTheSpace(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))
	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)
	require.Equal(t, http.StatusSeeOther, f.post(path+"/undo", nil, as("user_jun")).Code)

	w := f.post(path+"/undo", nil, as("user_jun"))

	assert.Equal(t, http.StatusSeeOther, w.Code, "it's back on, so this isn't too late: %s", w.Body.String())
	assert.Equal(t, path, w.Header().Get("Location"))
}

func TestUndoIsRefusedWhenTheSpotHasBeenTaken(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	f.onboard(t, "user_kofi", "Kofi")
	starts := tomorrowAt(t, 14, 0)
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", starts, "30", "8"))
	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)
	f.createSpace(t, "user_kofi", f.spaceForm(t, "Long table", starts, "30", ""))

	w := f.post(path+"/undo", nil, as("user_jun"))
	assert.Equal(t, http.StatusConflict, w.Code)
	assert.Contains(t, w.Body.String(), "Long table is taken until 2:30. Try Couches.")
}

func TestEditCannotDropCapacityBelowThoseGoingOrTouchACancelledSpace(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	going := f.onboard(t, "user_priya", "Priya")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "5"))
	addSignup(t, f, spaceID(t, f, path), going.ID, "going")
	third := f.onboard(t, "user_third", "Third")
	addSignup(t, f, spaceID(t, f, path), third.ID, "going")

	low := f.post(path+"/edit", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "2"), as("user_jun"))
	assert.Equal(t, http.StatusUnprocessableEntity, low.Code)
	assert.Contains(t, low.Body.String(), "3 people are going. Keep the capacity at 3 or more.")

	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)
	assert.Equal(t, http.StatusConflict, f.get(path+"/edit", as("user_jun")).Code)
	assert.Equal(t, http.StatusConflict,
		f.post(path+"/edit", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "5"), as("user_jun")).Code)
}

func TestEditKeepsAStartTimeThatHasAlreadyPassed(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))
	id := spaceID(t, f, path)
	started := time.Now().Add(-10 * time.Minute).Truncate(time.Second)
	storetest.Exec(t, f.store, `UPDATE space SET starts_at = $2 WHERE id = $1`, id, started)

	form := f.spaceForm(t, "Long table", started.In(denver(t)).Format("2006-01-02T15:04"), "30", "")
	form.Set("title", "Retitled while it runs")
	w := f.post(path+"/edit", form, as("user_jun"))
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	assert.Contains(t, f.get(path).Body.String(), "Retitled while it runs")
	stored := storetest.QueryString(t, f.store, `SELECT to_char(starts_at AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') FROM space WHERE id = $1`, id)
	assert.Equal(t, started.UTC().Format("2006-01-02 15:04:05"), stored, "an unchanged minute keeps the exact start")
}

func TestCreateRejectsBadInput(t *testing.T) {
	cases := []struct {
		name   string
		change func(url.Values)
		want   string
	}{
		{"short title", func(v url.Values) { v.Set("title", "abcd") }, "5 to 80"},
		{"long title", func(v url.Values) { v.Set("title", strings.Repeat("a", 81)) }, "5 to 80"},
		{"control chars", func(v url.Values) { v.Set("title", "Hello\x07 there") }, "plain text"},
		{"long blurb", func(v url.Values) { v.Set("blurb", strings.Repeat("a", 281)) }, "280"},
		{"unknown spot", func(v url.Values) { v.Set("spot", "not-a-spot") }, "Pick a spot"},
		{"bad time", func(v url.Values) { v.Set("starts", "soonish") }, "Pick a start time"},
		{"past time", func(v url.Values) {
			v.Set("starts", time.Now().Add(-time.Hour).In(time.FixedZone("Denver", -7*3600)).Format("2006-01-02T15:04"))
		}, "That time has passed"},
		{"capacity of one", func(v url.Values) { v.Set("capacity", "1") }, "at least 2"},
		{"capacity not a number", func(v url.Values) { v.Set("capacity", "lots") }, "at least 2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			f.onboard(t, "user_jun", "Jun")
			form := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")
			tc.change(form)
			w := f.post("/e/cnh/spaces", form, as("user_jun"))
			assert.Equal(t, http.StatusUnprocessableEntity, w.Code)
			assert.Contains(t, w.Body.String(), tc.want)
		})
	}
}

func TestTitlesAreEscaped(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	form := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")
	form.Set("title", "<script>alert(1)</script>")
	body := f.get(f.createSpace(t, "user_jun", form), as("user_jun")).Body.String()
	assert.NotContains(t, body, "<script>alert(1)")
	assert.Contains(t, body, "&lt;script&gt;alert(1)")
}

func TestVisitorsSeeNoNamesOnASpace(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Junipero")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", ""))

	visitor := f.get(path)
	require.Equal(t, http.StatusOK, visitor.Code)
	assert.NotContains(t, visitor.Body.String(), "Junipero")
	assert.Contains(t, visitor.Body.String(), "1 of 10 going")
	assert.NotContains(t, f.get(path+"/seats").Body.String(), "Junipero")

	assert.Contains(t, f.get(path, as("user_jun")).Body.String(), "Junipero")
	assert.Contains(t, f.get(path+"/seats", as("user_jun")).Body.String(), "Junipero")
}

func TestUnknownSpaceIs404(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	assert.Equal(t, http.StatusNotFound, f.get("/e/cnh/spaces/nothing-here").Code)
	assert.Equal(t, http.StatusNotFound, f.get("/e/cnh/spaces/nothing-here/edit", as("user_jun")).Code)
	assert.Equal(t, http.StatusNotFound, f.post("/e/cnh/spaces/nothing-here/cancel", nil, as("user_jun")).Code)
}

func TestCreatingSpacesIsRateLimitedPerPersonAndSaysWhenToTryAgain(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	f.onboard(t, "user_kofi", "Kofi")
	starts := tomorrowAt(t, 9, 0)
	f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", starts, "15", ""))

	// A refusal doesn't use one up.
	for range 6 {
		require.Equal(t, http.StatusConflict,
			f.post("/e/cnh/spaces", f.spaceForm(t, "Long table", starts, "15", ""), as("user_jun")).Code)
	}
	for i := 1; i < 5; i++ {
		f.createSpace(t, "user_jun", f.spaceForm(t, "Long table", tomorrowAt(t, 9+i, 0), "15", ""))
	}

	w := f.post("/e/cnh/spaces", f.spaceForm(t, "Couches", tomorrowAt(t, 15, 0), "15", ""), as("user_jun"))
	assert.Equal(t, http.StatusTooManyRequests, w.Code)
	assert.Contains(t, w.Body.String(), "You can start another at")

	f.createSpace(t, "user_kofi", f.spaceForm(t, "Couches", tomorrowAt(t, 15, 0), "15", ""))
}

func TestCreateFromAPostPrefillsATrimmedTitleAndLinksThePost(t *testing.T) {
	f := newFixture(t)
	author := f.onboard(t, "user_jun", "Jun")
	f.onboard(t, "user_kofi", "Kofi")
	long := "our OTel collector drops spans under load on k8s every single time we deploy on a Friday night"
	post := storetest.QueryString(t, f.store,
		`INSERT INTO post (author_id, kind, text, tags) VALUES ($1, 'stuck', $2, '{otel,k8s}') RETURNING id::text`, author.ID, long)

	form := f.get("/e/cnh/spaces/new?post="+post, as("user_kofi")).Body.String()
	assert.Contains(t, form, `value="our OTel collector drops spans under load on k8s every single time we deploy on"`)
	assert.Contains(t, form, `name="post" value="`+post+`"`)

	submit := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")
	submit.Set("post", post)
	path := f.createSpace(t, "user_kofi", submit)
	id := spaceID(t, f, path)
	assert.Equal(t, post, storetest.QueryString(t, f.store, `SELECT from_post_id::text FROM space WHERE id = $1`, id))
	assert.Contains(t, f.get(path, as("user_kofi")).Body.String(), "Stuck on")

	doc, ok := f.index.doc("space", id)
	require.True(t, ok)
	assert.Equal(t, []string{"otel", "k8s"}, doc.Tags)
}

func TestSpaceDocumentAddsThePostsTagsWithoutRepeatingItsOwn(t *testing.T) {
	f := newFixture(t)
	author := f.onboard(t, "user_jun", "Jun")
	f.onboard(t, "user_kofi", "Kofi")
	post := storetest.QueryString(t, f.store,
		`INSERT INTO post (author_id, kind, text, tags) VALUES ($1, 'stuck', 'our spans keep vanishing somewhere', '{otel,k8s,otel}') RETURNING id::text`, author.ID)

	submit := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")
	submit.Set("title", "Tuning #otel together")
	submit.Set("post", post)
	id := spaceID(t, f, f.createSpace(t, "user_kofi", submit))

	doc, ok := f.index.doc(search.TypeSpace, id)
	require.True(t, ok)
	assert.Equal(t, []string{"otel", "k8s"}, doc.Tags)
	assert.Contains(t, doc.Text, "Tuning #otel together. ")
}

func TestPrefillAndFromPostIgnoreClosedOrMalformedPosts(t *testing.T) {
	f := newFixture(t)
	author := f.onboard(t, "user_jun", "Jun")
	closed := storetest.QueryString(t, f.store,
		`INSERT INTO post (author_id, kind, text, status) VALUES ($1, 'stuck', 'an already resolved problem', 'resolved') RETURNING id::text`, author.ID)

	assert.NotContains(t, f.get("/e/cnh/spaces/new?post="+closed, as("user_jun")).Body.String(), "already resolved")
	assert.Equal(t, http.StatusOK, f.get("/e/cnh/spaces/new?post=garbage", as("user_jun")).Code)

	submit := f.spaceForm(t, "Long table", tomorrowAt(t, 14, 0), "30", "")
	submit.Set("post", "garbage")
	path := f.createSpace(t, "user_jun", submit)
	assert.Empty(t, storetest.QueryString(t, f.store,
		`SELECT coalesce(from_post_id::text, '') FROM space WHERE id = $1`, spaceID(t, f, path)))
}

func TestFormPrefillsSpotAndStartFromTheLink(t *testing.T) {
	f := newFixture(t)
	f.onboard(t, "user_jun", "Jun")
	spot := f.spotID(t, "Couches")
	starts := time.Date(2030, 1, 2, 22, 30, 0, 0, time.UTC) // 3:30 PM in Denver

	body := f.get("/e/cnh/spaces/new?spot="+spot+"&starts="+url.QueryEscape(starts.Format(time.RFC3339)), as("user_jun")).Body.String()
	assert.Contains(t, body, `value="`+spot+`" selected`)
	assert.Contains(t, body, `value="2030-01-02T15:30"`)
}

func TestIndexingFollowsTheSpaceAndNeverFailsTheRequest(t *testing.T) {
	f := newFixture(t)
	host := f.onboard(t, "user_jun", "Jun")
	path := f.createSpace(t, "user_jun", f.spaceForm(t, "Kitchen island", tomorrowAt(t, 14, 0), "30", ""))
	id := spaceID(t, f, path)

	doc, ok := f.index.doc("space", id)
	require.True(t, ok)
	assert.Equal(t, host.ID, doc.OwnerID)
	assert.Equal(t, "kitchen", doc.ZoneID)
	assert.Contains(t, doc.Text, "Collector tuning")
	require.NotNil(t, doc.StartsAt)

	require.Equal(t, http.StatusSeeOther, f.post(path+"/cancel", nil, as("user_jun")).Code)
	assert.Contains(t, f.index.deleted, "space/"+id)

	f.index.fail = true
	f.createSpace(t, "user_jun", f.spaceForm(t, "Couches", tomorrowAt(t, 16, 0), "30", ""))
}

func TestSignedInPagesFollowTheirOwnPersonTopic(t *testing.T) {
	f := newFixture(t)
	p := f.onboard(t, "user_jun", "Jun")
	home := f.get("/e/cnh", as("user_jun")).Body.String()
	assert.Contains(t, home, `sse-connect="`+live.URL("cnh", live.PersonTopic(p.ID))+`"`)
	assert.Contains(t, home, `sse-swap="notice"`)
	assert.NotContains(t, f.get("/e/cnh").Body.String(), "sse-connect")
}

// readUntil reads a stream until it has seen want, failing the test after the timeout.
func readUntil(t *testing.T, stream *bufio.Reader, want string, timeout time.Duration) string {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := stream.ReadString('\n')
			if err != nil {
				return
			}
			b.WriteString(line)
			if strings.Contains(b.String(), want) {
				got <- b.String()
				return
			}
		}
	}()
	select {
	case body := <-got:
		return body
	case <-time.After(timeout):
		t.Fatalf("no %q within %s", want, timeout)
		return ""
	}
}

// assertSilent checks nothing but heartbeats arrives for a while.
func assertSilent(t *testing.T, stream *bufio.Reader, wait time.Duration, msg string) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		for {
			line, err := stream.ReadString('\n')
			if err != nil {
				return
			}
			if line != "\n" && !strings.HasPrefix(line, ":") {
				got <- line
				return
			}
		}
	}()
	select {
	case line := <-got:
		t.Errorf("%s: got %q", msg, line)
	case <-time.After(wait):
	}
}
