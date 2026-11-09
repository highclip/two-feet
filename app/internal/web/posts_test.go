package web_test

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
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

// recorder is an Indexer that remembers what it was asked to do.
type recorder struct {
	mu      sync.Mutex
	docs    map[string]search.Doc
	deleted []string
}

func (r *recorder) Index(_ context.Context, d search.Doc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.docs[d.Type+":"+d.ID] = d
	return nil
}

func (r *recorder) Delete(_ context.Context, docType, id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.docs, docType+":"+id)
	r.deleted = append(r.deleted, docType+":"+id)
	return nil
}

func (r *recorder) doc(key string) (search.Doc, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	d, ok := r.docs[key]
	return d, ok
}

func newFixtureWith(t *testing.T, indexer search.Indexer, meta map[string]auth.Metadata) *fixture {
	t.Helper()
	s := storetest.Tx(t)
	require.NoError(t, s.Seed(context.Background(), "cnh"))
	hub := live.NewHub()
	t.Cleanup(hub.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &fixture{store: s, hub: hub, srv: web.New(web.Deps{
		Store:     s,
		Hub:       hub,
		Search:    indexer,
		Limiter:   web.NewLimiter(),
		Auth:      auth.New(s, fakeClerk{meta: meta}, log),
		Log:       log,
		Version:   "test-version",
		EventSlug: "cnh",
		ClerkKey:  "pk_test_" + base64.RawStdEncoding.EncodeToString([]byte("clerk.example.com$")),
		Ready:     func(context.Context) error { return nil },
	}).Handler()}
}

func postForm(kind, text string) url.Values { return url.Values{"kind": {kind}, "text": {text}} }

// createPost posts as someone and returns the post page's path.
func (f *fixture) createPost(t *testing.T, who, text string) string {
	t.Helper()
	w := f.post("/e/cnh/posts", postForm("stuck", text), as(who))
	require.Equal(t, http.StatusSeeOther, w.Code, w.Body.String())
	return w.Header().Get("Location")
}

func postIDFrom(path string) string { return path[strings.LastIndex(path, "/")+1:] }

func TestS5_AttendeePostingTenTo280CharsIsSavedAndIndexed(t *testing.T) {
	cases := []struct {
		name, text string
		wantCode   int
	}{
		{"ten chars", "abcdefghij", http.StatusSeeOther},
		{"280 multibyte chars", strings.Repeat("é", 280), http.StatusSeeOther},
		{"nine chars", "abcdefghi", http.StatusUnprocessableEntity},
		{"281 chars", strings.Repeat("a", 281), http.StatusUnprocessableEntity},
		{"only spaces", strings.Repeat(" ", 30), http.StatusUnprocessableEntity},
		{"control characters", "abcdefghij\x07klm", http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := &recorder{docs: map[string]search.Doc{}}
			f := newFixtureWith(t, idx, nil)
			person := f.onboard(t, "user_poster", "Jun")

			w := f.post("/e/cnh/posts", postForm("stuck", tc.text), as("user_poster"))

			require.Equal(t, tc.wantCode, w.Code)
			if tc.wantCode != http.StatusSeeOther {
				assert.Contains(t, w.Body.String(), `role="alert"`)
				return
			}
			id := postIDFrom(w.Header().Get("Location"))
			saved, err := f.store.PostByID(context.Background(), id)
			require.NoError(t, err)
			assert.Equal(t, tc.text, saved.Text)
			assert.Equal(t, person.ID, saved.AuthorID)
			assert.Equal(t, store.PostOpen, saved.Status)
			assert.WithinDuration(t, saved.CreatedAt.Add(3*time.Hour), saved.ExpiresAt, time.Second)

			doc, ok := idx.doc("post:" + id)
			require.True(t, ok, "post goes to the index")
			assert.Equal(t, tc.text, doc.Text)
			assert.Equal(t, person.ID, doc.OwnerID)
			require.NotNil(t, doc.ExpiresAt)
			assert.True(t, saved.ExpiresAt.Equal(*doc.ExpiresAt))
		})
	}
}

func TestS5_PostKeepsItsExtractedTags(t *testing.T) {
	f := newFixtureWith(t, &recorder{docs: map[string]search.Doc{}}, nil)
	f.onboard(t, "user_poster", "Jun")

	path := f.createPost(t, "user_poster", "our OTel collector drops spans on k8s #Sampling")

	saved, err := f.store.PostByID(context.Background(), postIDFrom(path))
	require.NoError(t, err)
	assert.Contains(t, saved.Tags, "otel-collector")
	assert.Contains(t, saved.Tags, "kubernetes")
	assert.Contains(t, saved.Tags, "sampling")

	denver, err := time.LoadLocation("America/Denver")
	require.NoError(t, err)
	assert.Contains(t, f.get(path, as("user_poster")).Body.String(),
		"You · "+saved.CreatedAt.In(denver).Format("3:04 PM"), "the time it was posted, in the venue's timezone")
}

func TestS5_PostContainingHTMLOrScriptShowsAsText(t *testing.T) {
	f := newFixtureWith(t, search.Noop{}, nil)
	f.onboard(t, "user_a", "Ana")
	f.onboard(t, "user_b", "Ben")
	nasty := `<script>alert(1)</script><img src=x onerror=alert(2)> "quoted"`

	page := f.createPost(t, "user_a", nasty)

	for name, body := range map[string]string{
		"own post page":              f.get(page, as("user_a")).Body.String(),
		"someone else's placeholder": f.get("/e/cnh", as("user_b")).Body.String(),
		"the composer page":          f.get("/e/cnh/posts/new", as("user_b")).Body.String(),
	} {
		assert.NotContains(t, body, "<script>alert", name)
		assert.NotContains(t, body, "<img src=x", name)
	}
	assert.Contains(t, f.get(page, as("user_a")).Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;")

	refused := f.post("/e/cnh/posts", postForm("stuck", "<b>x</b>"), as("user_a"))
	require.Equal(t, http.StatusUnprocessableEntity, refused.Code)
	assert.NotContains(t, refused.Body.String(), "<b>x</b>", "a refused post is echoed back escaped")
}

var retryClock = regexp.MustCompile(`post again at (\d{1,2}:\d{2} [AP]M)`)

func TestS5_SixthPostInTenMinutesIsRefusedWithWhenTheyCanPostAgain(t *testing.T) {
	idx := &recorder{docs: map[string]search.Doc{}}
	f := newFixtureWith(t, idx, nil)
	f.onboard(t, "user_busy", "Jun")
	f.onboard(t, "user_other", "Ben")
	for i := range 5 {
		f.createPost(t, "user_busy", fmt.Sprintf("post number %d of the five allowed", i))
	}

	sixth := f.post("/e/cnh/posts", postForm("curious", "the sixth post that goes too far"), as("user_busy"))

	require.Equal(t, http.StatusTooManyRequests, sixth.Code)
	assert.NotEmpty(t, sixth.Header().Get("Retry-After"))
	body := sixth.Body.String()
	assert.Contains(t, body, "the sixth post that goes too far", "what they typed stays in the box")

	match := retryClock.FindStringSubmatch(body)
	require.NotNil(t, match, "says when: %s", body)
	denver, err := time.LoadLocation("America/Denver")
	require.NoError(t, err)
	inTen := time.Now().Add(10 * time.Minute).In(denver)
	assert.Contains(t, []string{
		inTen.Format("3:04 PM"), inTen.Add(time.Minute).Format("3:04 PM"),
	}, match[1])

	f.createPost(t, "user_other", "someone else still has their own allowance")
}

func TestS5_InvalidPostsDoNotUseUpTheAllowance(t *testing.T) {
	f := newFixtureWith(t, search.Noop{}, nil)
	f.onboard(t, "user_busy", "Jun")
	for range 6 {
		require.Equal(t, http.StatusUnprocessableEntity,
			f.post("/e/cnh/posts", postForm("stuck", "short"), as("user_busy")).Code)
	}
	f.createPost(t, "user_busy", "this one is long enough to count")
}

func TestS5_ElasticsearchUnreachableThePostStillSavesAndTheAttendeeSeesNoError(t *testing.T) {
	client, err := search.NewClient(search.Config{URL: "http://127.0.0.1:1", Index: "unreachable"})
	require.NoError(t, err)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	queue := search.NewQueue(client, log, search.QueueOptions{Backoff: time.Millisecond})
	t.Cleanup(func() { queue.Close(context.Background()) })
	f := newFixtureWith(t, queue, nil)
	f.onboard(t, "user_poster", "Jun")

	start := time.Now()
	w := f.post("/e/cnh/posts", postForm("stuck", "elasticsearch has gone for lunch"), as("user_poster"))

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Less(t, time.Since(start), 2*time.Second, "the request doesn't wait on retries")
	id := postIDFrom(w.Header().Get("Location"))
	saved, err := f.store.PostByID(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, "elasticsearch has gone for lunch", saved.Text)

	page := f.get(w.Header().Get("Location"), as("user_poster"))
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "elasticsearch has gone for lunch")
}

func TestS5_PostPastItsExpiresAtLeavesTheIndexAndThePersonDocument(t *testing.T) {
	idx := &recorder{docs: map[string]search.Doc{}}
	f := newFixtureWith(t, idx, nil)
	person := f.onboard(t, "user_poster", "Jun")
	id := postIDFrom(f.createPost(t, "user_poster", "something that will not last forever"))
	sync := search.NewSync(f.store, idx, slog.New(slog.NewTextHandler(io.Discard, nil)))

	require.NoError(t, sync.ExpirePosts(context.Background(), time.Now().Add(4*time.Hour)))

	saved, err := f.store.PostByID(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, store.PostExpired, saved.Status)
	_, stillThere := idx.doc("post:" + id)
	assert.False(t, stillThere)
	assert.Contains(t, idx.deleted, "post:"+id)
	personDoc, ok := idx.doc("person:" + person.ID)
	if ok {
		assert.NotContains(t, personDoc.Text, "will not last")
	}
}

func TestS5_OnlyAttendeesAndSponsorRepsCanPost(t *testing.T) {
	f := newFixtureWith(t, search.Noop{}, map[string]auth.Metadata{
		"user_org": {Role: store.RoleOrganizer},
		"user_rep": {Role: store.RoleSponsorRep, Sponsor: "Dash0"},
	})
	f.onboard(t, "user_org", "Olive")
	f.onboard(t, "user_rep", "Rae")
	text := "a perfectly reasonable thing to say"

	assert.Equal(t, http.StatusForbidden, f.post("/e/cnh/posts", postForm("stuck", text), as("user_org")).Code)
	assert.Equal(t, http.StatusSeeOther, f.post("/e/cnh/posts", postForm("offer", text), as("user_rep")).Code)
	assert.Equal(t, http.StatusUnauthorized, f.post("/e/cnh/posts", postForm("stuck", text)).Code)
	assert.NotContains(t, f.get("/e/cnh", as("user_org")).Body.String(), `action="/e/cnh/posts"`)
}

func TestPostNeedsAKnownKindAndAFinishedOnboarding(t *testing.T) {
	f := newFixtureWith(t, search.Noop{}, nil)
	f.onboard(t, "user_poster", "Jun")
	text := "a perfectly reasonable thing to say"

	assert.Equal(t, http.StatusUnprocessableEntity,
		f.post("/e/cnh/posts", postForm("shouting", text), as("user_poster")).Code)

	f.get("/e/cnh", as("user_nameless"))
	nameless := f.post("/e/cnh/posts", postForm("stuck", text), as("user_nameless"))
	assert.Equal(t, http.StatusSeeOther, nameless.Code)
	assert.Contains(t, nameless.Header().Get("Location"), "/onboarding")
}

func TestHomeShowsTheComposerWithRealPostsAsPlaceholders(t *testing.T) {
	f := newFixtureWith(t, search.Noop{}, nil)
	f.onboard(t, "user_a", "Ana")
	f.onboard(t, "user_b", "Ben")
	empty := f.get("/e/cnh", as("user_b")).Body.String()
	assert.Contains(t, empty, `action="/e/cnh/posts"`)
	assert.Contains(t, empty, `value="stuck"`)
	assert.Contains(t, empty, `value="curious"`)
	assert.Contains(t, empty, `value="offer"`)

	f.createPost(t, "user_a", "rotating placeholders from the room")

	assert.Contains(t, f.get("/e/cnh", as("user_b")).Body.String(), "rotating placeholders from the room")
}

func TestOnlyTheAuthorSeesOrResolvesAPost(t *testing.T) {
	idx := &recorder{docs: map[string]search.Doc{}}
	f := newFixtureWith(t, idx, nil)
	f.onboard(t, "user_a", "Ana")
	f.onboard(t, "user_b", "Ben")
	page := f.createPost(t, "user_a", "a problem only Ana should close")
	id := postIDFrom(page)

	assert.Equal(t, http.StatusNotFound, f.get(page, as("user_b")).Code)
	assert.Equal(t, http.StatusNotFound, f.get("/e/cnh/posts/not-a-uuid", as("user_a")).Code)
	assert.Equal(t, http.StatusForbidden, f.post(page+"/resolve", nil, as("user_b")).Code)
	saved, err := f.store.PostByID(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, store.PostOpen, saved.Status)

	resolved := f.post(page+"/resolve", nil, as("user_a"))

	assert.Equal(t, http.StatusSeeOther, resolved.Code)
	saved, err = f.store.PostByID(context.Background(), id)
	require.NoError(t, err)
	assert.Equal(t, store.PostResolved, saved.Status)
	assert.Contains(t, idx.deleted, "post:"+id)
}

func TestPersonDocumentFollowsOnboardingPostsAndDiscoverable(t *testing.T) {
	idx := &recorder{docs: map[string]search.Doc{}}
	f := newFixtureWith(t, idx, nil)
	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/onboarding",
		url.Values{"display_name": {"Jun"}, "tags": {"otel, go"}}, as("user_jun")).Code)
	jun, err := f.store.PersonByClerkID(context.Background(), "user_jun")
	require.NoError(t, err)
	key := "person:" + jun.ID

	doc, ok := idx.doc(key)
	require.True(t, ok, "indexed on onboarding")
	assert.Equal(t, []string{"otel", "go"}, doc.Tags)
	assert.Equal(t, jun.ID, doc.OwnerID)
	assert.Empty(t, doc.ZoneID)

	page := f.createPost(t, "user_jun", "rebuilding collector pipelines today")
	doc, _ = idx.doc(key)
	assert.Contains(t, doc.Text, "otel")
	assert.Contains(t, doc.Text, "rebuilding collector pipelines today")

	require.Equal(t, http.StatusSeeOther, f.post(page+"/resolve", nil, as("user_jun")).Code)
	doc, _ = idx.doc(key)
	assert.NotContains(t, doc.Text, "rebuilding collector pipelines today")

	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/discoverable",
		url.Values{"discoverable": {"false"}}, as("user_jun")).Code)
	_, ok = idx.doc(key)
	assert.False(t, ok, "not discoverable means no person document")

	require.Equal(t, http.StatusSeeOther, f.post("/e/cnh/discoverable",
		url.Values{"discoverable": {"true"}}, as("user_jun")).Code)
	_, ok = idx.doc(key)
	assert.True(t, ok)
}

func TestPostAndPersonDocumentsNeverCarryAZone(t *testing.T) {
	idx := &recorder{docs: map[string]search.Doc{}}
	f := newFixtureWith(t, idx, nil)
	person := f.onboard(t, "user_poster", "Jun")
	storetest.Exec(t, f.store, `UPDATE person SET zone_id = 'kitchen', zone_seen_at = now() WHERE id = $1`, person.ID)

	id := postIDFrom(f.createPost(t, "user_poster", "somewhere in the kitchen probably"))

	post, _ := idx.doc("post:" + id)
	assert.Empty(t, post.ZoneID)
	personDoc, _ := idx.doc("person:" + person.ID)
	assert.Empty(t, personDoc.ZoneID)
}
