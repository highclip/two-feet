package search_test

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
)

func TestD26_TopTagsCountsOpenPostsOnly(t *testing.T) {
	c := liveClient(t)
	ctx := context.Background()
	now := time.Now()
	later := now.Add(time.Hour)
	earlier := now.Add(-time.Hour)

	docs := []search.Doc{
		{Type: search.TypePost, ID: "p1", Text: "collector drops spans", Tags: []string{"otel", "k8s"}, ExpiresAt: &later},
		{Type: search.TypePost, ID: "p2", Text: "collector pipelines", Tags: []string{"otel"}, ExpiresAt: &later},
		{Type: search.TypePost, ID: "gone", Text: "an expired post", Tags: []string{"expired-tag"}, ExpiresAt: &earlier},
		{Type: search.TypePerson, ID: "ana", Text: "otel person", Tags: []string{"person-tag"}},
		{Type: search.TypeSpace, ID: "s1", Text: "a space", Tags: []string{"space-tag"}, StartsAt: &later},
	}
	for _, d := range docs {
		require.NoError(t, c.Index(ctx, d))
	}

	got, err := c.TopTags(ctx, now, 8)

	require.NoError(t, err)
	assert.Equal(t, []search.TagCount{{Tag: "otel", Count: 2}, {Tag: "k8s", Count: 1}}, got)
}

func TestD26_TopTagsIsEmptyBeforeAnythingIsIndexed(t *testing.T) {
	c, err := search.NewClient(search.Config{URL: elasticURL(), Index: "twofeet_never_created_index"})
	require.NoError(t, err)

	got, err := c.TopTags(context.Background(), time.Now(), 8)

	require.NoError(t, err)
	assert.Empty(t, got)
}
