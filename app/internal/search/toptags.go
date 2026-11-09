package search

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// TagCount is how many open posts carry a tag.
type TagCount struct {
	Tag   string
	Count int
}

// TagCounter reports the most common tags on open posts.
type TagCounter interface {
	TopTags(ctx context.Context, now time.Time, size int) ([]TagCount, error)
}

// TopTags is a terms aggregation over the tags of open, unexpired posts: tags and counts only, so
// nothing about who posted can come back.
func (c *Client) TopTags(ctx context.Context, now time.Time, size int) ([]TagCount, error) {
	body, err := json.Marshal(map[string]any{
		"size": 0,
		"query": map[string]any{"bool": map[string]any{"filter": []any{
			map[string]any{"term": map[string]any{"type": TypePost}},
			map[string]any{"range": map[string]any{"expires_at": map[string]any{"gt": now.Format(time.RFC3339)}}},
		}}},
		"aggs": map[string]any{"tags": map[string]any{"terms": map[string]any{"field": "tags", "size": size}}},
	})
	if err != nil {
		return nil, fmt.Errorf("encode top tags: %w", err)
	}
	res, err := c.es.Search(
		c.es.Search.WithIndex(c.index),
		c.es.Search.WithBody(bytes.NewReader(body)),
		c.es.Search.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("top tags: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil // nothing has been indexed yet
	}
	if res.IsError() {
		return nil, fmt.Errorf("top tags: %w", responseError(res))
	}
	var parsed struct {
		Aggregations struct {
			Tags struct {
				Buckets []struct {
					Key      string `json:"key"`
					DocCount int    `json:"doc_count"`
				} `json:"buckets"`
			} `json:"tags"`
		} `json:"aggregations"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode top tags: %w", err)
	}
	out := make([]TagCount, 0, len(parsed.Aggregations.Tags.Buckets))
	for _, b := range parsed.Aggregations.Tags.Buckets {
		out = append(out, TagCount{Tag: b.Key, Count: b.DocCount})
	}
	return out, nil
}
