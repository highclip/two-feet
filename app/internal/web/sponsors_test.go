package web_test

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"log/slog"
	"net/http"
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
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
	"github.com/highclip/two-feet/app/internal/web"
	"github.com/highclip/two-feet/app/internal/web/views"
)

// recordingIndexer remembers what was indexed and can be told to fail like an unreachable cluster.
type recordingIndexer struct {
	mu   sync.Mutex
	docs []search.Doc
	err  error
}

func (r *recordingIndexer) Index(_ context.Context, d search.Doc) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.err != nil {
		return r.err
	}
	r.docs = append(r.docs, d)
	return nil
}

func (r *recordingIndexer) Delete(context.Context, string, string) error { return nil }

func (r *recordingIndexer) indexed() []search.Doc {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]search.Doc(nil), r.docs...)
}

type sponsorFixture struct {
	*fixture
	indexer *recordingIndexer
	dash0   string
	elastic string
}

const (
	repDash0   = "user_rep_dash0"
	repElastic = "user_rep_elastic"
	attendee   = "user_attendee"
	organizer  = "user_organizer"
)

func newSponsorFixture(t *testing.T) *sponsorFixture {
	t.Helper()
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	hub := live.NewHub()
	t.Cleanup(hub.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	indexer := &recordingIndexer{}
	provider := fakeClerk{meta: map[string]auth.Metadata{
		repDash0:   {Role: store.RoleSponsorRep, Sponsor: "Dash0"},
		repElastic: {Role: store.RoleSponsorRep, Sponsor: "Elastic"},
		organizer:  {Role: store.RoleOrganizer},
	}}
	f := &sponsorFixture{
		fixture: &fixture{store: s, hub: hub, srv: web.New(web.Deps{
			Store:     s,
			Hub:       hub,
			Search:    indexer,
			Limiter:   web.NewLimiter(),
			Auth:      auth.New(s, provider, log),
			Log:       log,
			Version:   "test-version",
			EventSlug: "cnh",
			ClerkKey:  "pk_test_" + base64.RawStdEncoding.EncodeToString([]byte("clerk.example.com$")),
			Ready:     func(context.Context) error { return nil },
		}).Handler()},
		indexer: indexer,
	}
	sponsors, err := s.Sponsors(ctx)
	require.NoError(t, err)
	for _, sp := range sponsors {
		switch sp.Name {
		case "Dash0":
			f.dash0 = sp.ID
		case "Elastic":
			f.elastic = sp.ID
		}
	}
	require.NotEmpty(t, f.dash0)
	require.NotEmpty(t, f.elastic)
	return f
}

func editPath(id string) string { return "/e/cnh/sponsors/" + id + "/edit" }
func savePath(id string) string { return "/e/cnh/sponsors/" + id }

func (f *sponsorFixture) solves(t *testing.T, id string) string {
	t.Helper()
	sp, err := f.store.SponsorByID(context.Background(), id)
	require.NoError(t, err)
	return sp.Solves
}

func TestS7_RepEditingSolvesIsReindexedWithinFiveSeconds(t *testing.T) {
	f := newSponsorFixture(t)
	f.onboard(t, repDash0, "Rae")
	const text = "Tail sampling and OTel collector pipelines that hold up under load"

	start := time.Now()
	w := f.post(savePath(f.dash0), url.Values{"solves": {text}}, as(repDash0))

	require.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, text, f.solves(t, f.dash0))
	assert.Less(t, time.Since(start), 5*time.Second)
	docs := f.indexer.indexed()
	require.Len(t, docs, 1)
	assert.Equal(t, search.TypeSponsor, docs[0].Type)
	assert.Equal(t, f.dash0, docs[0].ID)
	assert.Contains(t, docs[0].Text, "Dash0")
	assert.Contains(t, docs[0].Text, text)

	page := f.get(w.Header().Get("Location"), as(repDash0))
	assert.Equal(t, http.StatusOK, page.Code)
	assert.Contains(t, page.Body.String(), "Saved")
	assert.Contains(t, page.Body.String(), text)
}

func TestS7_RepEditingAnotherSponsorGets403(t *testing.T) {
	f := newSponsorFixture(t)
	f.onboard(t, repDash0, "Rae")
	before := f.solves(t, f.elastic)

	save := f.post(savePath(f.elastic), url.Values{"solves": {"Hijacked by another sponsor"}}, as(repDash0))
	form := f.get(editPath(f.elastic), as(repDash0))

	assert.Equal(t, http.StatusForbidden, save.Code)
	assert.Equal(t, http.StatusForbidden, form.Code)
	assert.Equal(t, before, f.solves(t, f.elastic))
	assert.Empty(t, f.indexer.indexed())
}

func TestS7_SponsorMatchShowsBoothSpotAndWalkTime(t *testing.T) {
	f := newSponsorFixture(t)
	ctx := context.Background()
	require.NoError(t, f.store.SetSponsorBooth(ctx, "Dash0", "Long table"))
	f.onboard(t, repDash0, "Rae")

	// The indexed document carries the booth's zone, so a match can work out the walk.
	require.Equal(t, http.StatusSeeOther,
		f.post(savePath(f.dash0), url.Values{"solves": {"OTel collector pipelines"}}, as(repDash0)).Code)
	docs := f.indexer.indexed()
	require.Len(t, docs, 1)
	assert.Equal(t, "main", docs[0].ZoneID)

	booth, err := f.store.SponsorBooth(ctx, f.dash0, "kitchen")
	require.NoError(t, err)
	assert.Equal(t, "Long table", booth.SpotName)
	assert.Equal(t, "1 min walk", walkOf(t, booth))
	assert.Equal(t, "Long table · 1 min walk", views.BoothLine(booth))
}

func walkOf(t *testing.T, b store.Booth) string {
	t.Helper()
	require.NotNil(t, b.WalkMinutes)
	return views.WalkLabel(*b.WalkMinutes)
}

func TestBoothLineTellsOnlyWhatIsKnown(t *testing.T) {
	zero, two := 0, 2
	assert.Equal(t, "Dash0 booth · 2 min walk", views.BoothLine(store.Booth{SpotName: "Dash0 booth", WalkMinutes: &two}))
	assert.Equal(t, "Dash0 booth · Right here", views.BoothLine(store.Booth{SpotName: "Dash0 booth", WalkMinutes: &zero}))
	assert.Equal(t, "Dash0 booth", views.BoothLine(store.Booth{SpotName: "Dash0 booth"}))
	assert.Empty(t, views.BoothLine(store.Booth{}))
}

func TestOnlySponsorRepsCanEditSponsors(t *testing.T) {
	f := newSponsorFixture(t)
	f.onboard(t, attendee, "Ada")
	f.onboard(t, organizer, "Org")
	before := f.solves(t, f.dash0)

	for _, who := range []string{attendee, organizer} {
		assert.Equal(t, http.StatusForbidden, f.get(editPath(f.dash0), as(who)).Code, who)
		assert.Equal(t, http.StatusForbidden,
			f.post(savePath(f.dash0), url.Values{"solves": {"Not my sponsor at all"}}, as(who)).Code, who)
	}
	assert.Equal(t, http.StatusUnauthorized, f.post(savePath(f.dash0), url.Values{"solves": {"Nobody signed in"}}).Code)
	assert.Equal(t, http.StatusSeeOther, f.get(editPath(f.dash0)).Code)
	assert.Equal(t, before, f.solves(t, f.dash0))
}

func TestSolvesIsValidatedAsPlainLimitedText(t *testing.T) {
	cases := []struct {
		name, input, want string
		code              int
	}{
		{"empty", "   ", "", http.StatusUnprocessableEntity},
		{"too long", strings.Repeat("a", 201), "", http.StatusUnprocessableEntity},
		{"control character", "Good text\x07here", "", http.StatusUnprocessableEntity},
		{"longest allowed in multibyte", strings.Repeat("é", 200), strings.Repeat("é", 200), http.StatusSeeOther},
		{"newlines fold to spaces", " Traces,\r\n metrics  and logs ", "Traces, metrics and logs", http.StatusSeeOther},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSponsorFixture(t)
			f.onboard(t, repDash0, "Rae")
			before := f.solves(t, f.dash0)

			w := f.post(savePath(f.dash0), url.Values{"solves": {tc.input}}, as(repDash0))

			assert.Equal(t, tc.code, w.Code)
			if tc.code == http.StatusSeeOther {
				assert.Equal(t, tc.want, f.solves(t, f.dash0))
				return
			}
			assert.Equal(t, before, f.solves(t, f.dash0))
			assert.Empty(t, f.indexer.indexed())
			assert.Contains(t, w.Body.String(), `role="alert"`)
		})
	}
}

func TestSolvesWithHTMLIsEscapedOnTheForm(t *testing.T) {
	f := newSponsorFixture(t)
	f.onboard(t, repDash0, "Rae")
	require.Equal(t, http.StatusSeeOther,
		f.post(savePath(f.dash0), url.Values{"solves": {`<script>alert(1)</script>`}}, as(repDash0)).Code)

	page := f.get(editPath(f.dash0), as(repDash0)).Body.String()

	assert.NotContains(t, page, "<script>alert(1)")
	assert.Contains(t, page, "&lt;script&gt;alert(1)")
}

func TestSponsorEditStillSavesWhenSearchIsDown(t *testing.T) {
	f := newSponsorFixture(t)
	f.onboard(t, repDash0, "Rae")
	f.indexer.err = errors.New("connection refused")

	w := f.post(savePath(f.dash0), url.Values{"solves": {"Still saved without a cluster"}}, as(repDash0))

	assert.Equal(t, http.StatusSeeOther, w.Code)
	assert.Equal(t, "Still saved without a cluster", f.solves(t, f.dash0))
}

func TestHomeLinksARepToTheirOwnSponsorOnly(t *testing.T) {
	f := newSponsorFixture(t)
	f.onboard(t, repDash0, "Rae")
	f.onboard(t, attendee, "Ada")

	assert.Contains(t, f.get("/e/cnh", as(repDash0)).Body.String(), editPath(f.dash0))
	assert.NotContains(t, f.get("/e/cnh", as(attendee)).Body.String(), "/sponsors/")
}
