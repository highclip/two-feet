package search_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/highclip/two-feet/app/internal/search"
)

// fakeHN is a local stand-in for the Hacker News search API, so these tests stay off the network.
func fakeHN(t *testing.T, handle http.HandlerFunc) *search.HackerNews {
	t.Helper()
	srv := httptest.NewServer(handle)
	t.Cleanup(srv.Close)
	return search.NewHackerNews(srv.URL)
}

func TestS12_HackerNewsIsAskedForRecentStoriesAndOnlyTitlesAndPointsAreKept(t *testing.T) {
	var mu sync.Mutex
	var queries []string
	var filters []string
	source := fakeHN(t, func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/search", r.URL.Path)
		assert.Equal(t, "story", r.URL.Query().Get("tags"))
		mu.Lock()
		queries = append(queries, r.URL.Query().Get("query"))
		filters = append(filters, r.URL.Query().Get("numericFilters"))
		mu.Unlock()
		_, _ = w.Write([]byte(`{"hits":[{"title":"Kubernetes news","points":12,"author":"somebody","url":"https://example.com"},{"title":null,"points":null}]}`))
	})

	stories, err := source.Stories(context.Background())

	require.NoError(t, err)
	require.NotEmpty(t, stories)
	assert.Contains(t, queries, "kubernetes")
	assert.Contains(t, queries, "opentelemetry")
	for _, filter := range filters {
		require.True(t, strings.HasPrefix(filter, "created_at_i>"), filter)
		since, err := strconv.ParseInt(strings.TrimPrefix(filter, "created_at_i>"), 10, 64)
		require.NoError(t, err)
		assert.InDelta(t, time.Now().Add(-7*24*time.Hour).Unix(), since, 60)
	}
	assert.Equal(t, search.Story{Title: "Kubernetes news", Source: "Hacker News", Points: 12}, stories[0])
	for _, s := range stories {
		assert.NotContains(t, s.Title, "somebody")
	}
}

func TestS12_HackerNewsKeepsWhatCameBackWhenOneSearchFails(t *testing.T) {
	source := fakeHN(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("query") == "kubernetes" {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"hits":[{"title":"Docker news"}]}`))
	})

	stories, err := source.Stories(context.Background())

	require.NoError(t, err)
	assert.NotEmpty(t, stories)
}

func TestS12_HackerNewsFailsWhenEverySearchFails(t *testing.T) {
	for name, handle := range map[string]http.HandlerFunc{
		"server error": func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "down", http.StatusBadGateway) },
		"not json":     func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) },
	} {
		t.Run(name, func(t *testing.T) {
			_, err := fakeHN(t, handle).Stories(context.Background())

			require.Error(t, err)
		})
	}
}

func TestS12_HackerNewsGivesUpWhenItsContextDoes(t *testing.T) {
	release := make(chan struct{})
	source := fakeHN(t, func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := source.Stories(ctx)

	require.Error(t, err)
	assert.Less(t, time.Since(started), 3*time.Second)
}

func TestS12_TheRealSiteIsOnlyTheDefault(t *testing.T) {
	assert.Equal(t, "https://hn.algolia.com", search.HackerNewsURL)
	assert.Equal(t, search.HackerNewsURL, search.NewHackerNews("").BaseURL(), "an unset override means the public API")
}
