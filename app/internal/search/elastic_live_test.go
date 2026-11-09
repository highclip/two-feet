package search_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
)

// coldStart is how long the local cluster may take to load ELSER the first time. Everything timed
// in these tests starts after it.
const coldStart = 5 * time.Minute

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func elasticURL() string {
	if url := os.Getenv("ELASTIC_URL"); url != "" {
		return url
	}
	return "http://localhost:9200"
}

// liveClient returns a client on a throwaway index in the local Elasticsearch from
// infra/local/compose.yaml, with ELSER already answering.
func liveClient(t *testing.T) *search.Client {
	t.Helper()
	suffix := make([]byte, 6)
	_, err := rand.Read(suffix)
	require.NoError(t, err)
	c, err := search.NewClient(search.Config{
		URL:    elasticURL(),
		APIKey: os.Getenv("ELASTIC_API_KEY"),
		Index:  os.Getenv("ELASTIC_INDEX_PREFIX") + "_test_" + hex.EncodeToString(suffix),
	})
	require.NoError(t, err)
	t.Cleanup(func() { assert.NoError(t, c.DropIndex(context.Background())) })

	ctx := context.Background()
	warm := search.Doc{Type: search.TypeSponsor, ID: "warm-up", Text: "warming up the inference model"}
	require.Eventually(t, func() bool {
		attempt, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		err := c.Index(attempt, warm)
		if err != nil {
			t.Logf("waiting for Elasticsearch: %v", err)
		}
		return err == nil
	}, coldStart, 2*time.Second, "Elasticsearch with ELSER must be running (infra/local/compose.yaml)")
	require.NoError(t, c.Delete(ctx, search.TypeSponsor, "warm-up"))
	return c
}

func hitIDs(hits []search.Hit) []string {
	ids := make([]string, 0, len(hits))
	for _, h := range hits {
		ids = append(ids, h.ID)
	}
	return ids
}

func TestS5_SavedPostIsSearchableWithinThreeSeconds(t *testing.T) {
	c := liveClient(t)
	q := search.NewQueue(c, quietLog(), search.QueueOptions{})
	t.Cleanup(func() { q.Close(context.Background()) })
	ctx := context.Background()
	expires := time.Now().Add(3 * time.Hour)

	start := time.Now()
	require.NoError(t, q.Index(ctx, search.Doc{
		Type: search.TypePost, ID: "post-1", OwnerID: "someone",
		Text:      "our OTel collector drops spans under load on k8s",
		Tags:      []string{"otel-collector", "kubernetes"},
		ExpiresAt: &expires,
	}))

	assert.Eventually(t, func() bool {
		hits, err := c.Search(ctx, search.Query{
			Text: "telemetry pipeline losing traces", Type: search.TypePost, Size: 3,
		})
		return err == nil && len(hits) == 1 && hits[0].ID == "post-1"
	}, 3*time.Second, 100*time.Millisecond, "not searchable within 3 seconds of saving")
	t.Logf("searchable after %s", time.Since(start))
}

func TestS5_PostPastItsExpiresAtNoLongerAppearsInMatches(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	now := time.Now()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	text := "tuning the OTel collector batch processor"
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypePost, ID: "expired", Text: text, ExpiresAt: &past}))
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypePost, ID: "live", Text: text, ExpiresAt: &future}))

	hits, err := c.Search(ctx, search.Query{Text: text, Type: search.TypePost, Size: 3})

	require.NoError(t, err)
	assert.Equal(t, []string{"live"}, hitIDs(hits))
}

func TestS7_EditedSolvesShowInSearchWithinFiveSeconds(t *testing.T) {
	c := liveClient(t)
	q := search.NewQueue(c, quietLog(), search.QueueOptions{})
	t.Cleanup(func() { q.Close(context.Background()) })
	ctx := context.Background()
	const before, after = "Dash0: managed dashboards for small teams", "Dash0: tail sampling for OTel collector pipelines"
	query := search.Query{Text: "tail sampling for collector pipelines", Type: search.TypeSponsor, Size: 3}
	require.NoError(t, c.Index(ctx, search.SponsorDoc("dash0", "Dash0", "managed dashboards for small teams", "")))
	require.Eventually(t, func() bool {
		hits, err := c.Search(ctx, query)
		return err == nil && len(hits) == 1 && hits[0].Text == before
	}, 30*time.Second, 100*time.Millisecond, "first version never became searchable")

	require.NoError(t, q.Index(ctx, search.SponsorDoc("dash0", "Dash0", "tail sampling for OTel collector pipelines", "")))

	assert.Eventually(t, func() bool {
		hits, err := c.Search(ctx, query)
		return err == nil && len(hits) == 1 && hits[0].Text == after
	}, 5*time.Second, 100*time.Millisecond, "edit not searchable within 5 seconds")
}

func TestS7_SponsorHitCarriesTheBoothZone(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	require.NoError(t, c.Index(ctx, search.SponsorDoc("dash0", "Dash0", "OTel collector pipelines", "main")))
	require.NoError(t, c.Index(ctx, search.SponsorDoc("elastic", "Elastic", "OTel collector pipelines", "")))

	var hits []search.Hit
	require.Eventually(t, func() bool {
		var err error
		hits, err = c.Search(ctx, search.Query{Text: "OTel collector pipelines", Type: search.TypeSponsor, Size: 3})
		return err == nil && len(hits) == 2
	}, 30*time.Second, 100*time.Millisecond, "sponsors never became searchable")

	zones := map[string]string{}
	for _, h := range hits {
		zones[h.ID] = h.ZoneID
	}
	assert.Equal(t, map[string]string{"dash0": "main", "elastic": ""}, zones)
}

func TestSearchCombinesTagOverlapAndLeavesOutTheAsker(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	tags := []string{"otel-collector"}
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypePost, ID: "mine", OwnerID: "me", Text: "my collector question", Tags: tags}))
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypePost, ID: "tagged", OwnerID: "them", Text: "completely unrelated words about sourdough", Tags: tags}))

	hits, err := c.Search(ctx, search.Query{Text: "collector", Tags: tags, Type: search.TypePost, ExcludeOwner: "me", Size: 3})

	require.NoError(t, err)
	require.Equal(t, []string{"tagged"}, hitIDs(hits))
	assert.Equal(t, "completely unrelated words about sourdough", hits[0].Text)
	assert.Equal(t, tags, hits[0].Tags)
}

func TestSearchWindowsSpacesToTheNextNinetyMinutes(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	now := time.Now()
	soon, later := now.Add(30*time.Minute), now.Add(3*time.Hour)
	end := now.Add(4 * time.Hour)
	text := "Collector tuning session"
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypeSpace, ID: "soon", Text: text, StartsAt: &soon, ExpiresAt: &end}))
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypeSpace, ID: "later", Text: text, StartsAt: &later, ExpiresAt: &end}))

	hits, err := c.Search(ctx, search.Query{Text: text, Type: search.TypeSpace, Size: 3})

	require.NoError(t, err)
	assert.Equal(t, []string{"soon"}, hitIDs(hits))
}

func TestKeysListsEveryDocumentAcrossPages(t *testing.T) {
	c := liveClient(t)
	c.SetKeysPageSize(2)
	ctx := context.Background()
	want := []search.DocKey{
		{Type: search.TypePerson, ID: "p1"},
		{Type: search.TypePost, ID: "a"},
		{Type: search.TypePost, ID: "b"},
		{Type: search.TypePost, ID: "c"},
		{Type: search.TypeSponsor, ID: "s1"},
	}
	for _, k := range want {
		require.NoError(t, c.Index(ctx, search.Doc{Type: k.Type, ID: k.ID, Text: "some words"}))
	}

	got, err := c.Keys(ctx)

	require.NoError(t, err)
	assert.ElementsMatch(t, want, got)
}

func TestDeleteRemovesTheDocumentAndToleratesMissingOnes(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	require.NoError(t, c.Index(ctx, search.Doc{Type: search.TypePerson, ID: "p1", Text: "otel kubernetes"}))
	require.NoError(t, c.Delete(ctx, search.TypePerson, "p1"))
	require.NoError(t, c.Delete(ctx, search.TypePerson, "never-existed"))

	hits, err := c.Search(ctx, search.Query{Text: "otel kubernetes", Type: search.TypePerson, Size: 3})

	require.NoError(t, err)
	assert.Empty(t, hits)
}
