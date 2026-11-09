package search_test

import (
	"context"
	"crypto/rand"
	"fmt"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/demo"
	"github.com/highclip/two-feet/app/internal/search"
	"github.com/highclip/two-feet/app/internal/store"
	"github.com/highclip/two-feet/app/internal/store/storetest"
)

func newAsker(t *testing.T, st *store.Store, clerkID, name string) store.Person {
	t.Helper()
	ctx := context.Background()
	p, err := st.EnsurePerson(ctx, clerkID, store.RoleAttendee, "")
	require.NoError(t, err)
	p, err = st.CompleteOnboarding(ctx, p.ID, name, []string{})
	require.NoError(t, err)
	return p
}

func TestS6_SeededDemoDataMatchesTheCollectorSpaceAPersonAndDash0(t *testing.T) {
	c := liveClient(t)
	st := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, st.Seed(ctx, "cnh"))
	queue := search.NewQueue(c, quietLog(), search.QueueOptions{})
	t.Cleanup(func() { queue.Close(context.Background()) })
	require.NoError(t, demo.Load(ctx, st, queue, quietLog(), "cnh", time.Now()))

	asker := newAsker(t, st, "user_asker", "Sam")
	const text = "our OTel collector drops spans under load on k8s"
	post, err := st.CreatePost(ctx, asker.ID, store.PostStuck, text, search.ExtractTags(text))
	require.NoError(t, err)

	got, err := search.NewMatcher(st, c).ForPost(ctx, asker, post, time.Now())

	require.NoError(t, err)
	require.NotEmpty(t, got.Spaces)
	require.NotEmpty(t, got.People)
	require.NotEmpty(t, got.Sponsors)
	assert.Contains(t, got.Spaces[0].Space.Title, "Collector tuning")
	assert.Equal(t, "Priya (sample)", got.People[0].Person.Name)
	assert.Equal(t, "Dash0", got.Sponsors[0].Sponsor.Name)
	for _, m := range slices.Concat(got.Spaces, got.People, got.Sponsors) {
		assert.NotEmpty(t, m.Reasons, m.TargetID)
		assert.LessOrEqual(t, len(m.Reasons), 3, m.TargetID)
	}
}

func TestD25_AQueryUnrelatedToTheDemoDataFindsNothing(t *testing.T) {
	c := liveClient(t)
	st := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, st.Seed(ctx, "cnh"))
	queue := search.NewQueue(c, quietLog(), search.QueueOptions{})
	t.Cleanup(func() { queue.Close(context.Background()) })
	require.NoError(t, demo.Load(ctx, st, queue, quietLog(), "cnh", time.Now()))
	asker := newAsker(t, st, "user_unrelated", "Sam")
	matcher := search.NewMatcher(st, c)

	nonsense, err := matcher.Find(ctx, asker, "zzzqqq xylophone", time.Now())
	require.NoError(t, err)
	assert.True(t, nonsense.Empty(), "%+v", nonsense)

	collector, err := matcher.Find(ctx, asker, "collector", time.Now())
	require.NoError(t, err)
	for _, m := range collector.Sponsors {
		assert.NotContains(t, []string{"Elastic", "Sonar", "Radar"}, m.Sponsor.Name)
	}
}

func randomUUID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 16)
	_, err := rand.Read(b)
	require.NoError(t, err)
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func TestS6_MatchRequestReturnsUnder800msAtP95With300IndexedDocuments(t *testing.T) {
	c := liveClient(t)
	st := storetest.Tx(t)
	ctx := context.Background()
	require.NoError(t, st.Seed(ctx, "cnh"))
	asker := newAsker(t, st, "user_speed", "Sam")

	// Thirty different texts, so loading 300 documents costs thirty ELSER runs instead of 300.
	queue := search.NewQueue(c, quietLog(), search.QueueOptions{Workers: 8, Capacity: 400})
	t.Cleanup(func() { queue.Close(context.Background()) })
	expires := time.Now().Add(time.Hour)
	types := []string{search.TypePost, search.TypePerson, search.TypeSponsor, search.TypeSpace}
	for i := range 300 {
		starts := time.Now().Add(20 * time.Minute)
		d := search.Doc{
			Type: types[i%len(types)], ID: randomUUID(t), OwnerID: randomUUID(t),
			Text:      fmt.Sprintf("topic %d: tuning the OTel collector batch processor on kubernetes", i%30),
			Tags:      []string{"otel-collector", "kubernetes"},
			ExpiresAt: &expires,
		}
		if d.Type == search.TypeSpace {
			d.StartsAt = &starts
		}
		require.NoError(t, queue.Index(ctx, d))
	}
	flushCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	require.NoError(t, queue.Flush(flushCtx))
	require.False(t, queue.Failed(), "the 300 documents must all index")

	matcher := search.NewMatcher(st, c)
	const rounds = 40
	took := make([]time.Duration, 0, rounds)
	for i := range rounds {
		// A different question every round and every run, so ELSER's inference cache can't flatter
		// the numbers.
		text := fmt.Sprintf("%s question %d: why does our collector drop spans under load", randomUUID(t)[:8], i)
		start := time.Now()
		_, err := matcher.Find(ctx, asker, text, time.Now())
		took = append(took, time.Since(start))
		require.NoError(t, err)
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	p95 := took[len(took)*95/100]
	t.Logf("p50 %s, p95 %s", took[len(took)/2], p95)
	assert.Less(t, p95, 800*time.Millisecond)
}

// A sponsor scores far below the posts and people around it, so it must not be cut by a shared
// result window before it is ranked against the other sponsors.
func TestS6_ASponsorStillMatchesAmongManyStrongerDocuments(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	const text = "our OTel collector drops spans under load on k8s"
	expires := time.Now().Add(time.Hour)
	for i := range 60 {
		require.NoError(t, c.Index(ctx, search.Doc{
			Type: search.TypePost, ID: fmt.Sprintf("post-%d", i), OwnerID: "someone", Text: text, ExpiresAt: &expires,
		}))
	}
	require.NoError(t, c.Index(ctx, search.SponsorDoc("dash0", "Dash0", "OpenTelemetry-native observability: traces, metrics and logs", "")))

	hits, err := c.SearchEach(ctx, search.Query{Text: text}, []string{search.TypePost, search.TypeSponsor}, 3)

	require.NoError(t, err)
	byType := map[string][]string{}
	for _, h := range hits {
		byType[h.Type] = append(byType[h.Type], h.ID)
	}
	assert.Len(t, byType[search.TypePost], 3)
	assert.Equal(t, []string{"dash0"}, byType[search.TypeSponsor])
}
