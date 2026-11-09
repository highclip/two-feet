package search_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

type memoryIndex struct {
	mu   sync.Mutex
	docs map[string]search.Doc
}

func (m *memoryIndex) Index(_ context.Context, d search.Doc) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.docs[d.Type+":"+d.ID] = d
	return nil
}

func (m *memoryIndex) Delete(_ context.Context, docType, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.docs, docType+":"+id)
	return nil
}

func seededPerson(t *testing.T, s *store.Store, clerkID, name string) store.Person {
	t.Helper()
	ctx := context.Background()
	p, err := s.EnsurePerson(ctx, clerkID, store.RoleAttendee, "")
	require.NoError(t, err)
	p, err = s.CompleteOnboarding(ctx, p.ID, name, []string{"otel"})
	require.NoError(t, err)
	return p
}

func TestAllIndexesSponsorsOpenPostsUpcomingSpacesAndDiscoverablePeople(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	index := &memoryIndex{docs: map[string]search.Doc{}}
	sync := search.NewSync(s, index, quietLog())

	author := seededPerson(t, s, "user_author", "Ana")
	hidden := seededPerson(t, s, "user_hidden", "Ben")
	require.NoError(t, s.SetDiscoverable(ctx, hidden.ID, false))
	post, err := s.CreatePost(ctx, author.ID, store.PostStuck, "indexing every open post", []string{"indexing"})
	require.NoError(t, err)
	spaceID := storetest.QueryString(t, s, `
		INSERT INTO space (slug, host_id, title, spot_id, starts_at, minutes, capacity)
		SELECT 'test-slug-collector', $1, 'Collector tuning', id, $2, 30, 8 FROM spot WHERE name = 'Long table'
		RETURNING id::text`, author.ID, time.Now().Add(20*time.Minute))

	require.NoError(t, sync.All(ctx))

	postDoc, ok := index.docs["post:"+post.ID]
	require.True(t, ok)
	assert.Equal(t, author.ID, postDoc.OwnerID)
	assert.Empty(t, postDoc.ZoneID)

	spaceDoc, ok := index.docs["space:"+spaceID]
	require.True(t, ok)
	assert.Equal(t, "main", spaceDoc.ZoneID)
	assert.Equal(t, author.ID, spaceDoc.OwnerID)
	assert.Contains(t, spaceDoc.Text, "Collector tuning")
	require.NotNil(t, spaceDoc.StartsAt)
	require.NotNil(t, spaceDoc.ExpiresAt)
	assert.Equal(t, 30*time.Minute, spaceDoc.ExpiresAt.Sub(*spaceDoc.StartsAt))

	sponsors, err := s.Sponsors(ctx)
	require.NoError(t, err)
	require.NotEmpty(t, sponsors)
	sponsorDoc, ok := index.docs["sponsor:"+sponsors[0].ID]
	require.True(t, ok)
	assert.Contains(t, sponsorDoc.Text, sponsors[0].Name)
	assert.Contains(t, sponsorDoc.Text, sponsors[0].Solves)

	_, ok = index.docs["person:"+author.ID]
	assert.True(t, ok)
	_, ok = index.docs["person:"+hidden.ID]
	assert.False(t, ok, "not discoverable")
}

// listingIndex is a memoryIndex that can also say what it holds, as Elasticsearch can. onKeys runs
// as the listing is taken.
type listingIndex struct {
	memoryIndex
	onKeys func()
}

func (l *listingIndex) Keys(context.Context) ([]search.DocKey, error) {
	if l.onKeys != nil {
		l.onKeys()
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	keys := make([]search.DocKey, 0, len(l.docs))
	for _, d := range l.docs {
		keys = append(keys, search.DocKey{Type: d.Type, ID: d.ID})
	}
	return keys, nil
}

func (l *listingIndex) has(docType, id string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	_, ok := l.docs[docType+":"+id]
	return ok
}

func newListingIndex() *listingIndex {
	return &listingIndex{memoryIndex: memoryIndex{docs: map[string]search.Doc{}}}
}

func TestRebuildRemovesAResolvedPostAndAnOptedOutPersonWhoseDeletesWereLost(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	index := newListingIndex()
	queue := search.NewQueue(index, quietLog(), fast())
	rebuild := search.NewSync(s, queue, quietLog())

	author := seededPerson(t, s, "user_author", "Ana")
	optedOut := seededPerson(t, s, "user_opted_out", "Ben")
	kept, err := s.CreatePost(ctx, author.ID, store.PostStuck, "still looking for help", []string{})
	require.NoError(t, err)
	resolved, err := s.CreatePost(ctx, author.ID, store.PostStuck, "already sorted this out", []string{})
	require.NoError(t, err)

	// Both documents were written while the rows were live, and the deletes never arrived.
	expires := resolved.ExpiresAt
	require.NoError(t, index.Index(ctx, search.Doc{Type: search.TypePost, ID: resolved.ID, ExpiresAt: &expires}))
	require.NoError(t, index.Index(ctx, search.Doc{Type: search.TypePerson, ID: optedOut.ID, OwnerID: optedOut.ID}))
	_, err = s.ResolvePost(ctx, resolved.ID, author.ID)
	require.NoError(t, err)
	require.NoError(t, s.SetDiscoverable(ctx, optedOut.ID, false))

	require.NoError(t, rebuild.All(ctx))
	queue.Close(ctx)

	assert.False(t, index.has(search.TypePost, resolved.ID), "a resolved post leaves the index")
	assert.False(t, index.has(search.TypePerson, optedOut.ID), "someone who opted out leaves the index")
	assert.True(t, index.has(search.TypePost, kept.ID), "open posts stay")
	assert.True(t, index.has(search.TypePerson, author.ID), "discoverable people stay")
}

func TestRebuildKeepsADocumentCommittedWhileTheListingWasTaken(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	author := seededPerson(t, s, "user_author", "Ana")
	index := newListingIndex()
	queue := search.NewQueue(index, quietLog(), fast())
	rebuild := search.NewSync(s, queue, quietLog())

	var fresh store.Post
	index.onKeys = func() {
		var err error
		fresh, err = s.CreatePost(ctx, author.ID, store.PostStuck, "posted during the rebuild", []string{})
		require.NoError(t, err)
		require.NoError(t, index.Index(ctx, search.PostDoc(fresh)))
	}

	require.NoError(t, rebuild.All(ctx))
	queue.Close(ctx)

	assert.True(t, index.has(search.TypePost, fresh.ID), "Postgres is read after the listing, so new posts survive")
}

func TestRebuildDeletesNothingWhenPostgresCannotBeRead(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	index := newListingIndex()
	queue := search.NewQueue(index, quietLog(), fast())
	rebuild := search.NewSync(s, queue, quietLog())
	require.NoError(t, index.Index(ctx, search.Doc{Type: search.TypePost, ID: "unknown"}))

	cancelled, cancel := context.WithCancel(ctx)
	cancel()

	require.Error(t, rebuild.All(cancelled))
	queue.Close(ctx)

	assert.True(t, index.has(search.TypePost, "unknown"), "an unreadable Postgres says nothing about what is stale")
}

type stubHealth struct{ err error }

func (h stubHealth) check(context.Context) error { return h.err }

func TestTickReindexesOnlyOnceAfterALostWriteAndElasticsearchIsBack(t *testing.T) {
	s := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	failing := &flaky{failures: 4}
	queue := search.NewQueue(failing, quietLog(), fast())
	sync := search.NewSync(s, queue, quietLog())
	require.NoError(t, queue.Index(ctx, search.Doc{ID: "lost"}))
	waitFor(t, queue.Failed)

	sync.Tick(ctx, stubHealth{errors.New("still down")}.check, queue)
	assert.True(t, queue.Failed(), "stays owed while Elasticsearch is down")

	before := len(failing.seen())
	sync.Tick(ctx, stubHealth{}.check, queue)
	assert.False(t, queue.Failed())
	waitFor(t, func() bool { return len(failing.seen()) > before })
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	require.Eventually(t, cond, 2*time.Second, 2*time.Millisecond)
}
