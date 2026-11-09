package web_test

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
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
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
	"github.com/highclip/two-feet/app/internal/web"
)

// fakeFinder hands back whatever a test says Elasticsearch found, including things the index
// shouldn't still hold, which is what proves the checks against Postgres.
type fakeFinder struct {
	hits    []search.Hit
	err     error
	queries []search.Query
}

// SearchEach keeps the first each hits of every type, as the real finder does.
func (f *fakeFinder) SearchEach(_ context.Context, q search.Query, _ []string, each int) ([]search.Hit, error) {
	f.queries = append(f.queries, q)
	seen := map[string]int{}
	var kept []search.Hit
	for _, hit := range f.hits {
		if seen[hit.Type]++; seen[hit.Type] <= each {
			kept = append(kept, hit)
		}
	}
	return kept, f.err
}

func newMatchFixture(t *testing.T, finder search.Finder) *fixture {
	t.Helper()
	s := storetest.Tx(t)
	require.NoError(t, s.Seed(context.Background(), "cnh"))
	hub := live.NewHub()
	t.Cleanup(hub.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	meta := map[string]auth.Metadata{}
	return &fixture{store: s, hub: hub, index: &spaceIndexer{}, meta: meta, srv: web.New(web.Deps{
		Store:     s,
		Hub:       hub,
		Search:    &spaceIndexer{},
		Finder:    finder,
		Limiter:   web.NewLimiter(),
		Auth:      auth.New(s, fakeClerk{meta: meta}, log),
		Log:       log,
		Version:   "test-version",
		EventSlug: "cnh",
		ClerkKey:  "pk_test_" + base64.RawStdEncoding.EncodeToString([]byte("clerk.example.com$")),
		Ready:     func(context.Context) error { return nil },
	}).Handler()}
}

const question = "our OTel collector drops spans under load on k8s"

func (f *fixture) askerPost(t *testing.T, who, name string) (store.Person, string) {
	t.Helper()
	person := f.onboard(t, who, name)
	return person, postIDFrom(f.createPost(t, who, question))
}

func (f *fixture) makeSpace(t *testing.T, hostID, spotName, title string, startsIn time.Duration, capacity int) store.Space {
	t.Helper()
	sp, err := f.store.CreateSpace(context.Background(), hostID, store.SpaceInput{
		Title: title, SpotID: f.spotID(t, spotName), StartsAt: time.Now().Add(startsIn), Minutes: 30, Capacity: capacity,
	})
	require.NoError(t, err)
	return sp
}

func (f *fixture) seededSponsorID(t *testing.T, name string) string {
	t.Helper()
	return storetest.QueryString(t, f.store, `SELECT id::text FROM sponsor WHERE name = $1`, name)
}

func (f *fixture) setBooth(t *testing.T, sponsor, spot string) {
	t.Helper()
	require.NoError(t, f.store.SetSponsorBooth(context.Background(), sponsor, spot))
}

func (f *fixture) checkIn(t *testing.T, personID, zone string) {
	t.Helper()
	storetest.Exec(t, f.store, `UPDATE person SET zone_id = $2, zone_seen_at = now() WHERE id = $1`, personID, zone)
}

func (f *fixture) matchesHTML(t *testing.T, who, postID string) string {
	t.Helper()
	w := f.get("/e/cnh/posts/"+postID+"/matches", as(who), header("HX-Request", "true"))
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	return w.Body.String()
}

// cards splits a matches fragment into its cards, by their data-key.
func cards(html string) map[string]string {
	out := map[string]string{}
	parts := regexp.MustCompile(`<article class="match`).Split(html, -1)
	key := regexp.MustCompile(`data-key="([^"]+)"`)
	for _, part := range parts[1:] {
		if m := key.FindStringSubmatch(part); m != nil {
			out[m[1]] = part
		}
	}
	return out
}

func reasons(card string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`<li class="tag reason">([^<]*)</li>`).FindAllStringSubmatch(card, -1) {
		out = append(out, m[1])
	}
	return out
}

func spaceHit(sp store.Space) search.Hit {
	return search.Hit{Type: search.TypeSpace, ID: sp.ID, Text: sp.Title, Tags: search.ExtractTags(sp.Title), OwnerID: sp.HostID}
}

func personHit(p store.Person) search.Hit {
	return search.Hit{Type: search.TypePerson, ID: p.ID, OwnerID: p.ID, Tags: p.Tags}
}

func TestS6_AnyMatchShowsOneToThreeReasonsAndNotJustARawScore(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	priya := f.onboard(t, "user_priya", "Priya")
	f.createPost(t, "user_priya", "Rebuilding our OTel collector pipelines with tail sampling")
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning: dropping spans under load", 6*time.Minute, 8)
	dash0 := f.seededSponsorID(t, "Dash0")
	finder.hits = []search.Hit{
		spaceHit(sp), personHit(priya),
		{Type: search.TypeSponsor, ID: dash0, Text: "Dash0: traces"},
	}

	got := cards(f.matchesHTML(t, "user_asker", postID))

	require.Len(t, got, 3)
	score := regexp.MustCompile(`^[0-9.]+$`)
	for key, card := range got {
		rs := reasons(card)
		assert.NotEmpty(t, rs, key)
		assert.LessOrEqual(t, len(rs), 3, key)
		for _, r := range rs {
			assert.False(t, score.MatchString(r), "%s shows a raw score: %q", key, r)
		}
	}
}

func TestS6_APersonWhoIsNotDiscoverableNeverAppearsAsAMatch(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	hidden := f.onboard(t, "user_hidden", "Hidden Person")
	require.NoError(t, f.store.SetDiscoverable(context.Background(), hidden.ID, false))
	finder.hits = []search.Hit{personHit(hidden)} // a stale document the index still holds

	html := f.matchesHTML(t, "user_asker", postID)

	assert.NotContains(t, html, "Hidden Person")
	assert.Empty(t, cards(html))
}

func TestS6_StaleHitsAtTheTopDoNotCrowdOutPeopleRankedBelowThem(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	for i := range 3 {
		hidden := f.onboard(t, fmt.Sprintf("user_hidden_%d", i), "Hidden Person")
		require.NoError(t, f.store.SetDiscoverable(context.Background(), hidden.ID, false))
		finder.hits = append(finder.hits, personHit(hidden)) // stale documents holding the top slots
	}
	finder.hits = append(finder.hits, personHit(f.onboard(t, "user_priya", "Priya")))
	f.createPost(t, "user_priya", "Rebuilding our OTel collector pipelines with tail sampling")

	html := f.matchesHTML(t, "user_asker", postID)

	assert.NotContains(t, html, "Hidden Person")
	assert.Contains(t, html, "Priya")
	assert.Len(t, cards(html), 1)
}

func TestS6_EachKindStillStopsAtThreeMatchesAfterOverFetching(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	for i := range 5 {
		who := fmt.Sprintf("user_person_%d", i)
		person := f.onboard(t, who, fmt.Sprintf("Person %d", i))
		f.createPost(t, who, "Rebuilding our OTel collector pipelines with tail sampling")
		finder.hits = append(finder.hits, personHit(person))
	}

	got := cards(f.matchesHTML(t, "user_asker", postID))

	assert.Len(t, got, 3)
}

func TestS6_AnAttendeesOwnPostsAndSpacesNeverAppearInTheirOwnMatches(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker, postID := f.askerPost(t, "user_asker", "Sam")
	mine := f.makeSpace(t, asker.ID, "Long table", "My own collector space", 6*time.Minute, 8)
	finder.hits = []search.Hit{
		spaceHit(mine),
		personHit(asker),
		{Type: search.TypePost, ID: postID, OwnerID: asker.ID, Text: question},
	}

	html := f.matchesHTML(t, "user_asker", postID)

	assert.Empty(t, cards(html))
	require.NotEmpty(t, finder.queries)
	assert.Equal(t, asker.ID, finder.queries[0].ExcludeOwner, "the query itself leaves the asker out too")
}

func TestS7_ASponsorMatchShowsTheBoothSpotAndWalkTime(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker, postID := f.askerPost(t, "user_asker", "Sam")
	f.checkIn(t, asker.ID, "kitchen")
	f.setBooth(t, "Dash0", "Long table")
	finder.hits = []search.Hit{{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Dash0"), Text: "Dash0: traces"}}

	card := cards(f.matchesHTML(t, "user_asker", postID))["sponsor:"+f.seededSponsorID(t, "Dash0")]

	assert.Contains(t, card, "Long table · 1 min walk")
	assert.Contains(t, card, "Show me the booth")
}

func TestS6_ASponsorWithNoBoothYetStillMatchesWithoutOffering(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	id := f.seededSponsorID(t, "Dash0")
	storetest.Exec(t, f.store, `UPDATE sponsor SET booth_spot_id = NULL WHERE id = $1`, id)
	finder.hits = []search.Hit{{Type: search.TypeSponsor, ID: id, Text: "Dash0: traces"}}

	card := cards(f.matchesHTML(t, "user_asker", postID))["sponsor:"+id]

	require.NotEmpty(t, card)
	assert.NotContains(t, card, "Show me the booth")
	assert.NotEmpty(t, reasons(card))
}

func TestS6_ASpaceMatchHasLogisticsAndAJoinAction(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker, postID := f.askerPost(t, "user_asker", "Sam")
	f.checkIn(t, asker.ID, "kitchen")
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	finder.hits = []search.Hit{spaceHit(sp)}

	card := cards(f.matchesHTML(t, "user_asker", postID))["space:"+sp.ID]

	assert.Contains(t, card, "1 min walk")
	assert.Contains(t, card, "starts in 6 min")
	assert.Contains(t, card, "1 of 8 going")
	assert.Contains(t, card, `action="/e/cnh/spaces/`+sp.Slug+`/join"`)
}

func TestS6_ASpaceInTheSameZoneReadsRightHere(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker, postID := f.askerPost(t, "user_asker", "Sam")
	f.checkIn(t, asker.ID, "main")
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	finder.hits = []search.Hit{spaceHit(sp)}

	assert.Contains(t, cards(f.matchesHTML(t, "user_asker", postID))["space:"+sp.ID], "Right here")
}

func TestS6_AStaleCheckInGivesNoWalkTime(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker, postID := f.askerPost(t, "user_asker", "Sam")
	storetest.Exec(t, f.store, `UPDATE person SET zone_id = 'kitchen', zone_seen_at = now() - interval '46 minutes' WHERE id = $1`, asker.ID)
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	finder.hits = []search.Hit{spaceHit(sp)}

	assert.NotContains(t, cards(f.matchesHTML(t, "user_asker", postID))["space:"+sp.ID], "min walk")
}

func TestS6_APersonMatchCarriesNoZoneOrWalkTime(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker, postID := f.askerPost(t, "user_asker", "Sam")
	f.checkIn(t, asker.ID, "kitchen")
	priya := f.onboard(t, "user_priya", "Priya")
	f.checkIn(t, priya.ID, "main")
	f.createPost(t, "user_priya", "Rebuilding our OTel collector pipelines with tail sampling")
	finder.hits = []search.Hit{personHit(priya)}

	html := f.matchesHTML(t, "user_asker", postID)
	card := cards(html)["person:"+priya.ID]

	require.NotEmpty(t, card)
	assert.Contains(t, card, "Priya")
	for _, leak := range []string{"Main room", "min walk", "Right here", "close by"} {
		assert.NotContains(t, html, leak, "D29: nothing about a person may say where they are")
	}
}

func TestS6_AHitPostgresNoLongerWantsIsDropped(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	host := f.onboard(t, "user_host", "Hana")
	cancelled := f.makeSpace(t, host.ID, "Long table", "Cancelled space", 6*time.Minute, 8)
	_, err := f.store.CancelSpace(context.Background(), cancelled.ID)
	require.NoError(t, err)
	tooFar := f.makeSpace(t, host.ID, "Corner couches", "Moved to much later", 2*time.Hour, 6)
	finder.hits = []search.Hit{spaceHit(cancelled), spaceHit(tooFar),
		{Type: search.TypeSponsor, ID: "not-a-uuid"},
		{Type: search.TypeSpace, ID: "00000000-0000-0000-0000-000000000000"}}

	assert.Empty(t, cards(f.matchesHTML(t, "user_asker", postID)))
}

func TestS6_ASpaceStartedOverTenMinutesAgoOffersNoJoin(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", -11*time.Minute, 8)
	finder.hits = []search.Hit{spaceHit(sp)}

	card := cards(f.matchesHTML(t, "user_asker", postID))["space:"+sp.ID]

	require.NotEmpty(t, card)
	assert.Contains(t, card, "Happening now")
	assert.NotContains(t, card, "/join")
}

func TestS6_AWaitlistMakesAMatchCardOfferTheWaitlist(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	host := f.onboard(t, "user_host", "Hana")
	waiting := f.onboard(t, "user_waiting", "Wren")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	storetest.Exec(t, f.store, `INSERT INTO signup (space_id, person_id, status) VALUES ($1, $2, 'waitlist')`, sp.ID, waiting.ID)
	finder.hits = []search.Hit{spaceHit(sp)}

	assert.Contains(t, cards(f.matchesHTML(t, "user_asker", postID))["space:"+sp.ID], "Join the waitlist")
}

func TestS6_AMatchCardsSeatCountCatchesUpAfterAGap(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	finder.hits = []search.Hit{spaceHit(sp)}
	url := "/e/cnh/spaces/" + sp.Slug + "/seat-count"

	card := cards(f.matchesHTML(t, "user_asker", postID))["space:"+sp.ID]
	assert.Contains(t, card, `hx-get="`+url+`"`)
	assert.Contains(t, card, `hx-trigger="sse:space-`+sp.ID+`"`)

	w := f.get(url)
	require.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "1 of 8 going")
	assert.NotContains(t, w.Body.String(), "Hana", "D17: counts only, for anyone")
}

func TestS6_NoMatchesNamesOneNextActionWithAButton(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	_, postID := f.askerPost(t, "user_asker", "Sam")

	html := f.matchesHTML(t, "user_asker", postID)

	assert.Contains(t, html, "No one's on this yet. Start a space and let it find people.")
	assert.Contains(t, html, `href="/e/cnh/spaces/new?post=`+postID+`"`)
}

func TestS6_ElasticsearchDownLeavesThePostAndNoError(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{err: errors.New("connection refused")})
	_, postID := f.askerPost(t, "user_asker", "Sam")

	w := f.get("/e/cnh/posts/"+postID+"/matches", as("user_asker"), header("HX-Request", "true"))

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "Still looking")
	assert.NotContains(t, w.Body.String(), "connection refused")
}

func TestS6_OnlyThePostsAuthorCanSeeItsMatches(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	_, postID := f.askerPost(t, "user_asker", "Sam")
	f.onboard(t, "user_other", "Other")

	w := f.get("/e/cnh/posts/"+postID+"/matches", as("user_other"), header("HX-Request", "true"))

	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestS6_ARequestWithNoSessionGets401ForMatches(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	_, postID := f.askerPost(t, "user_asker", "Sam")

	w := f.get("/e/cnh/posts/"+postID+"/matches", header("HX-Request", "true"))

	assert.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestS6_APostPageShowsAnOutlineWhileMatchesLoad(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	_, postID := f.askerPost(t, "user_asker", "Sam")

	body := f.get("/e/cnh/posts/"+postID, as("user_asker")).Body.String()

	assert.Contains(t, body, `hx-get="/e/cnh/posts/`+postID+`/matches"`)
	assert.Contains(t, body, "note-outline")
}

func TestS6_NewMatchesAreOfferedAndNeverPushedIntoTheList(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	dash0 := f.seededSponsorID(t, "Dash0")
	radar := f.seededSponsorID(t, "Radar")
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	finder.hits = []search.Hit{
		{Type: search.TypeSponsor, ID: dash0, Text: "Dash0"},
		{Type: search.TypeSponsor, ID: radar, Text: "Radar"},
		spaceHit(sp),
	}
	base := "/e/cnh/posts/" + postID + "/matches/new?seen="

	body := f.get(base+url.QueryEscape("sponsor:"+dash0), as("user_asker"), header("HX-Request", "true")).Body.String()
	assert.Contains(t, body, "2 new matches")
	assert.NotContains(t, body, "<article")

	all := url.QueryEscape("sponsor:" + dash0 + ",sponsor:" + radar + ",space:" + sp.ID)
	assert.Empty(t, strings.TrimSpace(f.get(base+all, as("user_asker"), header("HX-Request", "true")).Body.String()))
}

func searchTarget(q string) string { return "/e/cnh/search?q=" + url.QueryEscape(q) }

func TestD25_SearchGroupsPeoplePostsSpacesAndSponsors(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	f.onboard(t, "user_asker", "Sam")
	priya := f.onboard(t, "user_priya", "Priya")
	post := postIDFrom(f.createPost(t, "user_priya", "Rebuilding our OTel collector pipelines"))
	host := f.onboard(t, "user_host", "Hana")
	sp := f.makeSpace(t, host.ID, "Long table", "Collector tuning", 6*time.Minute, 8)
	dash0 := f.seededSponsorID(t, "Dash0")
	// The e2e suite edits this sponsor's text and the seed keeps a rep's edits, so state it here.
	storetest.Exec(t, f.store, `UPDATE sponsor SET solves = $2 WHERE id = $1`, dash0, "OpenTelemetry-native observability: traces, metrics and logs")
	finder.hits = []search.Hit{
		spaceHit(sp), personHit(priya),
		{Type: search.TypeSponsor, ID: dash0, Text: "Dash0"},
		{Type: search.TypePost, ID: post, OwnerID: priya.ID, Text: "Rebuilding our OTel collector pipelines"},
	}

	w := f.get(searchTarget("otel collector"), as("user_asker"))

	require.Equal(t, http.StatusOK, w.Code)
	got := cards(w.Body.String())
	assert.Contains(t, got, "space:"+sp.ID)
	assert.Contains(t, got, "person:"+priya.ID)
	assert.Contains(t, got, "sponsor:"+dash0)
	assert.Contains(t, got, "post:"+post)
	for _, heading := range []string{"Spaces", "People", "Posts", "Sponsors"} {
		assert.Contains(t, w.Body.String(), heading)
	}
}

func TestD25_SearchIsForAttendeesAndSponsorRepsOnly(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	f.meta["user_org"] = auth.Metadata{Role: store.RoleOrganizer}
	f.onboard(t, "user_org", "Olive")

	assert.Equal(t, http.StatusForbidden, f.get(searchTarget("collector"), as("user_org")).Code)

	visitor := f.get(searchTarget("collector"))
	assert.Equal(t, http.StatusSeeOther, visitor.Code)
	assert.Contains(t, visitor.Header().Get("Location"), "/sign-in")
}

func TestD25_SearchLeavesOutTheSearcherAndHidesAnOptedOutAuthorsName(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker := f.onboard(t, "user_asker", "Sam")
	mine := postIDFrom(f.createPost(t, "user_asker", "my own post about collectors"))
	hidden := f.onboard(t, "user_hidden", "Hidden Person")
	theirs := postIDFrom(f.createPost(t, "user_hidden", "a post about the collector"))
	require.NoError(t, f.store.SetDiscoverable(context.Background(), hidden.ID, false))
	finder.hits = []search.Hit{
		{Type: search.TypePost, ID: mine, OwnerID: asker.ID, Text: "my own post about collectors"},
		{Type: search.TypePost, ID: theirs, OwnerID: hidden.ID, Text: "a post about the collector"},
		personHit(asker),
	}

	html := f.get(searchTarget("collector"), as("user_asker")).Body.String()
	got := cards(html)

	assert.NotContains(t, got, "post:"+mine)
	assert.NotContains(t, got, "person:"+asker.ID)
	require.Contains(t, got, "post:"+theirs)
	assert.NotContains(t, html, "Hidden Person")
}

func TestD25_ASearchCarriesNoAuthorZone(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	asker := f.onboard(t, "user_asker", "Sam")
	f.checkIn(t, asker.ID, "kitchen")
	priya := f.onboard(t, "user_priya", "Priya")
	f.checkIn(t, priya.ID, "main")
	post := postIDFrom(f.createPost(t, "user_priya", "Rebuilding our OTel collector pipelines"))
	finder.hits = []search.Hit{{Type: search.TypePost, ID: post, OwnerID: priya.ID, Text: "Rebuilding"}}

	html := f.get(searchTarget("collector"), as("user_asker")).Body.String()

	assert.NotContains(t, html, "Main room")
	assert.NotContains(t, html, "min walk")
}

func TestD25_QueryTextIsLengthLimited(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	f.onboard(t, "user_asker", "Sam")

	f.get(searchTarget(strings.Repeat("é", 5000)), as("user_asker"))

	require.Len(t, finder.queries, 1)
	assert.LessOrEqual(t, len([]rune(finder.queries[0].Text)), 200)
}

func TestD25_NothingFoundOffersOneNextAction(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	f.onboard(t, "user_asker", "Sam")

	body := f.get(searchTarget("anything at all"), as("user_asker")).Body.String()

	assert.Contains(t, body, "Start a space")
	assert.Contains(t, body, `href="/e/cnh/spaces/new"`)
}

func TestD25_HitsThatShareNothingWithTheQueryShowTheEmptyState(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	f.onboard(t, "user_asker", "Sam")
	finder.hits = []search.Hit{
		{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Temporal"), Text: "Temporal"},
		{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Sonar"), Text: "Sonar"},
	}

	body := f.get(searchTarget("zzzqqq xylophone"), as("user_asker")).Body.String()

	assert.Empty(t, cards(body))
	assert.NotContains(t, body, "Close to what you wrote")
	assert.Contains(t, body, "No one's on this yet. Start a space and let it find people.")
	assert.Contains(t, body, `href="/e/cnh/spaces/new"`)
}

func TestS6_HitsThatShareNothingWithThePostShowTheEmptyState(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	finder.hits = []search.Hit{{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Temporal"), Text: "Temporal"}}

	html := f.matchesHTML(t, "user_asker", postID)

	assert.Empty(t, cards(html))
	assert.Contains(t, html, "No one's on this yet. Start a space and let it find people.")
}

func TestS6_AHitWithNoReasonDoesNotTakeASlotFromOneBelowIt(t *testing.T) {
	finder := &fakeFinder{}
	f := newMatchFixture(t, finder)
	_, postID := f.askerPost(t, "user_asker", "Sam")
	dash0 := f.seededSponsorID(t, "Dash0")
	finder.hits = []search.Hit{
		{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Temporal"), Text: "Temporal"},
		{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Sonar"), Text: "Sonar"},
		{Type: search.TypeSponsor, ID: f.seededSponsorID(t, "Tailscale"), Text: "Tailscale"},
		{Type: search.TypeSponsor, ID: dash0, Text: "Dash0"},
	}

	got := cards(f.matchesHTML(t, "user_asker", postID))

	assert.Len(t, got, 1)
	assert.Contains(t, got, "sponsor:"+dash0)
}

func TestD25_TheHomeScreenHasOneSearchBox(t *testing.T) {
	f := newMatchFixture(t, &fakeFinder{})
	f.onboard(t, "user_asker", "Sam")

	body := f.get("/e/cnh", as("user_asker")).Body.String()

	assert.Equal(t, 1, strings.Count(body, `role="search"`))
	assert.Contains(t, body, `action="/e/cnh/search"`)
}
