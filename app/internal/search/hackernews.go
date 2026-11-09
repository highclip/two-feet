package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// HackerNewsURL is the public Hacker News search API. It needs no key.
const HackerNewsURL = "https://hn.algolia.com"

const (
	hackerNewsSource   = "Hacker News"
	hackerNewsWindow   = 7 * 24 * time.Hour
	hackerNewsMaxBytes = 2 << 20
)

// hackerNewsTerms are the cloud native words to ask about, one search each: the API matches every
// word of a query, so one query for all of them would find almost nothing.
var hackerNewsTerms = []string{
	"kubernetes", "opentelemetry", "docker", "terraform", "prometheus", "ebpf", "postgres", "kafka",
}

// HackerNews fetches recent cloud native stories.
type HackerNews struct {
	base   string
	client *http.Client
}

// NewHackerNews returns a source for baseURL, or for the public API when baseURL is empty.
func NewHackerNews(baseURL string) *HackerNews {
	if baseURL == "" {
		baseURL = HackerNewsURL
	}
	return &HackerNews{base: strings.TrimRight(baseURL, "/"), client: &http.Client{}}
}

// Stories runs every search at once, within ctx, and keeps what came back. It fails only when every
// search did.
func (h *HackerNews) Stories(ctx context.Context) ([]Story, error) {
	since := time.Now().Add(-hackerNewsWindow).Unix()
	results := make([][]Story, len(hackerNewsTerms))
	failures := make([]error, len(hackerNewsTerms))
	var wg sync.WaitGroup
	for i, term := range hackerNewsTerms {
		wg.Go(func() {
			results[i], failures[i] = h.search(ctx, term, since)
		})
	}
	wg.Wait()
	if !slices.Contains(failures, nil) {
		return nil, fmt.Errorf("search hacker news: %w", errors.Join(failures...))
	}
	return slices.Concat(results...), nil
}

func (h *HackerNews) search(ctx context.Context, term string, since int64) ([]Story, error) {
	query := url.Values{
		"query":          {term},
		"tags":           {"story"},
		"numericFilters": {"created_at_i>" + strconv.FormatInt(since, 10)},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.base+"/api/v1/search?"+query.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %s: %w", term, err)
	}
	res, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", term, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("get %s: status %d", term, res.StatusCode)
	}
	var parsed struct {
		Hits []struct {
			Title  string `json:"title"`
			Points int    `json:"points"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, hackerNewsMaxBytes)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode %s: %w", term, err)
	}
	stories := make([]Story, 0, len(parsed.Hits))
	for _, hit := range parsed.Hits {
		if hit.Title != "" {
			stories = append(stories, Story{Title: hit.Title, Source: hackerNewsSource, Points: hit.Points})
		}
	}
	return stories, nil
}
