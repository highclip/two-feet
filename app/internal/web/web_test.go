package web_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
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

// fakeClerk accepts "token-<clerk user id>" and stands in for the Clerk API. The real sign-in runs
// in Playwright.
type fakeClerk struct{ meta map[string]auth.Metadata }

func (f fakeClerk) VerifySession(_ context.Context, token string) (string, error) {
	id, ok := strings.CutPrefix(token, "token-")
	if !ok {
		return "", auth.ErrInvalidSession
	}
	return id, nil
}

func (f fakeClerk) UserMetadata(_ context.Context, id string) (auth.Metadata, error) {
	return f.meta[id], nil
}

type fixture struct {
	store *store.Store
	hub   *live.Hub
	srv   http.Handler
	index *spaceIndexer
	tags  *fakeTags
	meta  map[string]auth.Metadata
	at    time.Time // what the spot pages take to be now; zero means the real time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s := storetest.Tx(t)
	require.NoError(t, s.Seed(context.Background(), "cnh"))
	hub := live.NewHub()
	t.Cleanup(hub.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	index := &spaceIndexer{}
	tags := &fakeTags{}
	meta := map[string]auth.Metadata{}
	f := &fixture{store: s, hub: hub, index: index, tags: tags, meta: meta}
	f.srv = web.New(web.Deps{
		Store:     s,
		Hub:       hub,
		Search:    index,
		Tags:      tags,
		Limiter:   web.NewLimiter(),
		Auth:      auth.New(s, fakeClerk{meta: meta}, log),
		Log:       log,
		Version:   "test-version",
		EventSlug: "cnh",
		PublicURL: "https://twofeet.test",
		ClerkKey:  "pk_test_" + base64.RawStdEncoding.EncodeToString([]byte("clerk.example.com$")),
		Ready:     func(context.Context) error { return nil },
		Now: func() time.Time {
			if f.at.IsZero() {
				return time.Now()
			}
			return f.at
		},
	}).Handler()
	return f
}

type requestOpt func(*http.Request)

func as(clerkUserID string) requestOpt {
	return func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "__session", Value: "token-" + clerkUserID}) }
}

func header(k, v string) requestOpt { return func(r *http.Request) { r.Header.Set(k, v) } }

func (f *fixture) get(target string, opts ...requestOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

func (f *fixture) post(target string, form url.Values, opts ...requestOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	f.srv.ServeHTTP(w, r)
	return w
}

func (f *fixture) onboard(t *testing.T, clerkUserID, name string) store.Person {
	t.Helper()
	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/onboarding",
		url.Values{"display_name": {name}}, as(clerkUserID)).Code)
	p, err := f.store.PersonByClerkID(context.Background(), clerkUserID)
	require.NoError(t, err)
	return p
}

func TestS1_SignInLandsOnOnboardingAndCreatesPersonRow(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	_, err := f.store.PersonByClerkID(ctx, "user_new")
	require.ErrorIs(t, err, store.ErrNotFound)

	w := f.get("/e/cnh", as("user_new"))

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/e/cnh/onboarding", w.Header().Get("Location"))
	p, err := f.store.PersonByClerkID(ctx, "user_new")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAttendee, p.Role)

	page := f.get("/e/cnh/onboarding", as("user_new"))
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), `name="display_name"`)
}

func TestS1_NewPersonSubmittingNameAndTagsLandsOnHome(t *testing.T) {
	f := newFixture(t)
	f.get("/e/cnh", as("user_new"))

	w := f.post("/e/cnh/onboarding",
		url.Values{"display_name": {"  Priya  "}, "tags": {"OTel, k8s , otel, go"}}, as("user_new"))

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "/e/cnh", w.Header().Get("Location"))
	p, err := f.store.PersonByClerkID(context.Background(), "user_new")
	require.NoError(t, err)
	assert.Equal(t, "Priya", p.DisplayName)
	assert.Equal(t, []string{"otel", "k8s", "go"}, p.Tags)

	home := f.get("/e/cnh", as("user_new"))
	assert.Equal(t, http.StatusOK, home.Code)
	assert.Contains(t, home.Body.String(), "Priya")
}

func TestS1_OnboardingAcceptsOneToFortyCharsAndUpToThreeTags(t *testing.T) {
	cases := []struct {
		name, displayName, tags string
		wantCode                int
	}{
		{"one char", "J", "", http.StatusSeeOther},
		{"forty multibyte chars", strings.Repeat("é", 40), "", http.StatusSeeOther},
		{"three tags", "Jun", "a,b,c", http.StatusSeeOther},
		{"empty name", "   ", "", http.StatusUnprocessableEntity},
		{"forty-one chars", strings.Repeat("a", 41), "", http.StatusUnprocessableEntity},
		{"control characters", "Jun\x07", "", http.StatusUnprocessableEntity},
		{"four tags", "Jun", "a,b,c,d", http.StatusUnprocessableEntity},
		{"overlong tag", "Jun", strings.Repeat("t", 33), http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			w := f.post("/e/cnh/onboarding",
				url.Values{"display_name": {tc.displayName}, "tags": {tc.tags}}, as("user_case"))
			assert.Equal(t, tc.wantCode, w.Code)
		})
	}
}

func TestS1_NoValidSessionGets401ForHtmxAndAPIOrRedirectForPages(t *testing.T) {
	f := newFixture(t)
	sessions := map[string][]requestOpt{
		"no session":      nil,
		"invalid session": {func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "__session", Value: "forged"}) }},
	}
	for name, session := range sessions {
		t.Run(name, func(t *testing.T) {
			page := f.get("/e/cnh/onboarding?return=%2Fe%2Fcnh", session...)
			assert.Equal(t, http.StatusSeeOther, page.Code)
			assert.Equal(t, auth.SignInURL("/e/cnh/onboarding?return=%2Fe%2Fcnh"), page.Header().Get("Location"))

			htmx := f.get("/e/cnh/onboarding", append([]requestOpt{header("HX-Request", "true")}, session...)...)
			assert.Equal(t, http.StatusUnauthorized, htmx.Code)

			api := f.get("/e/cnh/onboarding", append([]requestOpt{header("Accept", "application/json")}, session...)...)
			assert.Equal(t, http.StatusUnauthorized, api.Code)

			form := f.post("/e/cnh/onboarding", url.Values{"display_name": {"Mallory"}}, session...)
			assert.Equal(t, http.StatusUnauthorized, form.Code)
		})
	}
}

func openStream(t *testing.T, srv *httptest.Server, path string, cookie *http.Cookie) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+path, nil)
	require.NoError(t, err)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	res, err := srv.Client().Do(req)
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Body.Close() })
	require.Equal(t, http.StatusOK, res.StatusCode)
	assert.Equal(t, "text/event-stream", res.Header.Get("Content-Type"))
	assert.Equal(t, "no-cache", res.Header.Get("Cache-Control"))
	assert.Equal(t, "no", res.Header.Get("X-Accel-Buffering"))

	stream := bufio.NewReader(res.Body)
	first, err := stream.ReadString('\n') // written after subscribing, so publishing is now safe
	require.NoError(t, err)
	require.Contains(t, first, "retry:")
	return stream
}

func TestS1_LiveClientReceivesPublishedFragmentWithinTwoSeconds(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close) // after the stream's own cleanup: Close waits for open requests
	stream := openStream(t, srv, live.URL("cnh", live.EventTopic), nil)

	start := time.Now()
	f.hub.Publish(live.EventTopic, live.Fragment{Event: "feed", HTML: []byte("<p>one</p>\n<p>two</p>")})

	got := make(chan string, 1)
	go func() {
		var b strings.Builder
		for {
			line, err := stream.ReadString('\n')
			if err != nil {
				return
			}
			b.WriteString(line)
			if strings.Contains(b.String(), "<p>two</p>\n\n") {
				got <- b.String()
				return
			}
		}
	}()
	select {
	case body := <-got:
		assert.Less(t, time.Since(start), 2*time.Second)
		assert.Contains(t, body, "event: feed\ndata: <p>one</p>\ndata: <p>two</p>\n\n")
	case <-time.After(2 * time.Second):
		t.Fatal("no fragment within 2 seconds")
	}
}

func TestS1_SomeoneElsesPersonTopicIsRefused(t *testing.T) {
	f := newFixture(t)
	owner := f.onboard(t, "user_owner", "Owner")
	f.onboard(t, "user_other", "Other")
	path := live.URL("cnh", live.PersonTopic(owner.ID))

	assert.Equal(t, http.StatusForbidden, f.get(path).Code, "visitor")
	assert.Equal(t, http.StatusForbidden, f.get(path, as("user_other")).Code, "another attendee")
	mixed := live.URL("cnh", live.EventTopic, live.PersonTopic(owner.ID))
	assert.Equal(t, http.StatusForbidden, f.get(mixed, as("user_other")).Code, "hidden among allowed topics")

	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close) // after the stream's own cleanup: Close waits for open requests
	openStream(t, srv, path, &http.Cookie{Name: "__session", Value: "token-user_owner"})
}

func TestLiveRefusesBadTopicsAndUnknownEvents(t *testing.T) {
	f := newFixture(t)
	assert.Equal(t, http.StatusBadRequest, f.get("/e/cnh/live").Code)
	assert.Equal(t, http.StatusBadRequest, f.get("/e/cnh/live?topics=nonsense").Code)
	assert.Equal(t, http.StatusBadRequest, f.get("/e/cnh/live?topics=spot:").Code)
	assert.Equal(t, http.StatusBadRequest, f.get("/e/cnh/live?topics=spot:a%0Ab").Code)
	many := strings.TrimSuffix(strings.Repeat("event,", 17), ",")
	assert.Equal(t, http.StatusBadRequest, f.get("/e/cnh/live?topics="+many).Code)
	assert.Equal(t, http.StatusNotFound, f.get("/e/nope/live?topics=event").Code)
}

func TestLiveStreamEndsWhenHubCloses(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.srv)
	t.Cleanup(srv.Close) // after the stream's own cleanup: Close waits for open requests
	stream := openStream(t, srv, live.URL("cnh", live.EventTopic), nil)

	f.hub.Close()
	done := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, stream)
		done <- err
	}()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("stream stayed open after the hub closed")
	}
}

func TestEventPageDispatch(t *testing.T) {
	f := newFixture(t)

	landing := f.get("/e/cnh")
	assert.Equal(t, http.StatusOK, landing.Code)
	assert.Contains(t, landing.Body.String(), auth.SignInURL("/e/cnh"))

	assert.Equal(t, http.StatusNotFound, f.get("/e/unknown").Code)

	unknownCode := f.get("/e/cnh?spot=nope")
	assert.Equal(t, http.StatusNotFound, unknownCode.Code)
	assert.Contains(t, unknownCode.Body.String(), "That code isn")
}

func TestOnboardingCarriesTheReturnPath(t *testing.T) {
	f := newFixture(t)

	first := f.get("/e/cnh?spot=abc", as("user_new"))
	require.Equal(t, http.StatusSeeOther, first.Code)
	require.Equal(t, "/e/cnh/onboarding?return=%2Fe%2Fcnh%3Fspot%3Dabc", first.Header().Get("Location"))

	page := f.get(first.Header().Get("Location"), as("user_new"))
	assert.Contains(t, page.Body.String(), `value="/e/cnh?spot=abc"`)

	done := f.post("/e/cnh/onboarding",
		url.Values{"display_name": {"Jun"}, "return": {"/e/cnh?spot=abc"}}, as("user_new"))
	assert.Equal(t, "/e/cnh?spot=abc", done.Header().Get("Location"))

	offsite := f.post("/e/cnh/onboarding",
		url.Values{"display_name": {"Jun"}, "return": {"//evil.example"}}, as("user_new"))
	assert.Equal(t, "/e/cnh", offsite.Header().Get("Location"))
}

func TestSignInPageIgnoresOffsiteReturn(t *testing.T) {
	f := newFixture(t)
	page := f.get("/sign-in?return=/e/cnh")
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), `data-return="/e/cnh"`)
	offsite := f.get("/sign-in?return=https://evil.example/")
	assert.Contains(t, offsite.Body.String(), `data-return="/"`)
}

func TestRoleAndSponsorComeFromClerkMetadata(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	provider := fakeClerk{meta: map[string]auth.Metadata{
		"user_org": {Role: "organizer"},
		"user_rep": {Role: "sponsor_rep", Sponsor: "Dash0"},
		"user_odd": {Role: "superadmin"},
	}}
	a := auth.New(s, provider, log)
	handler := a.Middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	for _, id := range []string{"user_org", "user_rep", "user_odd"} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: "__session", Value: "token-" + id})
		handler.ServeHTTP(httptest.NewRecorder(), r)
	}
	org, err := s.PersonByClerkID(ctx, "user_org")
	require.NoError(t, err)
	assert.Equal(t, store.RoleOrganizer, org.Role)
	rep, err := s.PersonByClerkID(ctx, "user_rep")
	require.NoError(t, err)
	assert.Equal(t, store.RoleSponsorRep, rep.Role)
	assert.NotNil(t, rep.SponsorID)
	odd, err := s.PersonByClerkID(ctx, "user_odd")
	require.NoError(t, err)
	assert.Equal(t, store.RoleAttendee, odd.Role)
}

func TestHomeToggleHidesAndShowsMeInMatches(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	p := f.onboard(t, "user_toggle", "Jun")
	require.True(t, p.Discoverable)
	home := f.get("/e/cnh", as("user_toggle"))
	assert.Contains(t, home.Body.String(), "Don't show me in matches")

	hide := f.post("/e/cnh/discoverable", url.Values{"discoverable": {"false"}}, as("user_toggle"))
	assert.Equal(t, http.StatusSeeOther, hide.Code)
	p, err := f.store.PersonByClerkID(ctx, "user_toggle")
	require.NoError(t, err)
	assert.False(t, p.Discoverable)

	show := f.post("/e/cnh/discoverable", url.Values{"discoverable": {"true"}}, as("user_toggle"))
	assert.Equal(t, http.StatusSeeOther, show.Code)
	p, err = f.store.PersonByClerkID(ctx, "user_toggle")
	require.NoError(t, err)
	assert.True(t, p.Discoverable)

	assert.Equal(t, http.StatusUnauthorized,
		f.post("/e/cnh/discoverable", url.Values{"discoverable": {"false"}}).Code)
}

func TestHealthAndReadiness(t *testing.T) {
	f := newFixture(t)
	health := f.get("/healthz")
	assert.Equal(t, http.StatusOK, health.Code)
	assert.Equal(t, "ok test-version\n", health.Body.String())
	assert.Equal(t, http.StatusOK, f.get("/readyz").Code)
}

func TestCrossOriginPostIsRefused(t *testing.T) {
	f := newFixture(t)
	w := f.post("/e/cnh/onboarding", url.Values{"display_name": {"Jun"}}, as("user_new"),
		header("Sec-Fetch-Site", "cross-site"))
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestStaticAssetsAreServedFromTheBinary(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/static/tokens.css", "/static/app.css", "/static/app.js", "/static/htmx.min.js", "/static/sse.js", "/static/live.js"} {
		assert.Equal(t, http.StatusOK, f.get(path).Code, path)
	}
}
