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
	s, tx := storetest.TxDB(t)
	ctx := context.Background()
	require.NoError(t, s.Seed(ctx, "cnh"))
	index := &memoryIndex{docs: map[string]search.Doc{}}
	sync := search.NewSync(s, index, quietLog())

	author := seededPerson(t, s, "user_author", "Ana")
	hidden := seededPerson(t, s, "user_hidden", "Ben")
	require.NoError(t, s.SetDiscoverable(ctx, hidden.ID, false))
	post, err := s.CreatePost(ctx, author.ID, store.PostStuck, "indexing every open post", []string{"indexing"})
	require.NoError(t, err)
	var spaceID string
	require.NoError(t, tx.QueryRow(ctx, `
		INSERT INTO space (slug, host_id, title, spot_id, starts_at, minutes, capacity)
		SELECT 'test-slug-collector', $1, 'Collector tuning', id, $2, 30, 8 FROM spot WHERE name = 'Long table'
		RETURNING id::text`, author.ID, time.Now().Add(20*time.Minute)).Scan(&spaceID))

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
