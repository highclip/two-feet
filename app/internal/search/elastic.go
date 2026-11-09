package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/elastic/go-elasticsearch/v8"
	"github.com/elastic/go-elasticsearch/v8/esapi"
)

const (
	// elserEndpoint is Elasticsearch's preconfigured ELSER inference endpoint.
	elserEndpoint = ".elser-2-elasticsearch"

	defaultResultSize = 3
	rankWindow        = 50
	rankConstant      = 60
	maxQueryRunes     = 500

	defaultKeysPageSize = 1000

	// spaceHorizon is how far ahead a space can start and still match.
	spaceHorizon = 90 * time.Minute
)

// body keeps a plain copy of the text, because older clusters return a semantic_text field with its
// embeddings attached.
const indexDefinition = `{
  "settings": {"index": {"refresh_interval": "1s"}},
  "mappings": {
    "dynamic": "strict",
    "properties": {
      "type":       {"type": "keyword"},
      "id":         {"type": "keyword"},
      "text":       {"type": "semantic_text", "inference_id": "` + elserEndpoint + `"},
      "body":       {"type": "keyword", "index": false, "doc_values": false},
      "tags":       {"type": "keyword"},
      "owner_id":   {"type": "keyword"},
      "zone_id":    {"type": "keyword"},
      "starts_at":  {"type": "date"},
      "expires_at": {"type": "date"}
    }
  }
}`

// Config says where the index is.
type Config struct {
	URL    string
	APIKey string
	Index  string
	// Transport is optional, so telemetry can wrap the HTTP calls.
	Transport http.RoundTripper
}

// Client talks to Elasticsearch. It makes one attempt per call; Queue does the retrying.
type Client struct {
	es    *elasticsearch.Client
	index string

	keysPageSize int

	mu      sync.Mutex
	created bool
}

// NewClient returns a Client. It makes no network calls, so the app starts with Elasticsearch down.
func NewClient(cfg Config) (*Client, error) {
	es, err := elasticsearch.NewClient(elasticsearch.Config{
		Addresses:    []string{cfg.URL},
		APIKey:       cfg.APIKey,
		Transport:    cfg.Transport,
		DisableRetry: true,
	})
	if err != nil {
		return nil, fmt.Errorf("create elasticsearch client: %w", err)
	}
	return &Client{es: es, index: cfg.Index}, nil
}

// wireDoc is a document as stored.
type wireDoc struct {
	Type      string     `json:"type"`
	ID        string     `json:"id"`
	Text      string     `json:"text,omitempty"`
	Body      string     `json:"body,omitempty"`
	Tags      []string   `json:"tags"`
	OwnerID   string     `json:"owner_id,omitempty"`
	ZoneID    string     `json:"zone_id,omitempty"`
	StartsAt  *time.Time `json:"starts_at,omitempty"`
	ExpiresAt *time.Time `json:"expires_at,omitempty"`
}

func documentID(docType, id string) string { return docType + ":" + id }

// responseError describes a failed response by status and error type only, because the reason text
// can quote the document and errors end up in logs.
func responseError(res *esapi.Response) error {
	var parsed struct {
		Error struct {
			Type string `json:"type"`
		} `json:"error"`
	}
	_ = json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&parsed)
	return fmt.Errorf("elasticsearch status %d (%s)", res.StatusCode, parsed.Error.Type)
}

// ensureIndex creates the index once, so a write can never auto-create it with the wrong mapping.
func (c *Client) ensureIndex(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.created {
		return nil
	}
	res, err := c.es.Indices.Create(c.index,
		c.es.Indices.Create.WithBody(bytes.NewReader([]byte(indexDefinition))),
		c.es.Indices.Create.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("create index: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() {
		failure := responseError(res)
		if res.StatusCode != http.StatusBadRequest || !bytes.Contains([]byte(failure.Error()), []byte("resource_already_exists")) {
			return fmt.Errorf("create index: %w", failure)
		}
	}
	c.created = true
	return nil
}

// Ready reports whether Elasticsearch answers and the index exists.
func (c *Client) Ready(ctx context.Context) error {
	res, err := c.es.Ping(c.es.Ping.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("ping elasticsearch: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() {
		return fmt.Errorf("ping elasticsearch: status %d", res.StatusCode)
	}
	return c.ensureIndex(ctx)
}

// Index writes a document and makes it searchable before returning.
func (c *Client) Index(ctx context.Context, d Doc) error {
	if err := c.ensureIndex(ctx); err != nil {
		return err
	}
	tags := d.Tags
	if tags == nil {
		tags = []string{}
	}
	payload, err := json.Marshal(wireDoc{
		Type: d.Type, ID: d.ID, Text: d.Text, Body: d.Text, Tags: tags,
		OwnerID: d.OwnerID, ZoneID: d.ZoneID, StartsAt: d.StartsAt, ExpiresAt: d.ExpiresAt,
	})
	if err != nil {
		return fmt.Errorf("encode %s %s: %w", d.Type, d.ID, err)
	}
	res, err := c.es.Index(c.index, bytes.NewReader(payload),
		c.es.Index.WithDocumentID(documentID(d.Type, d.ID)),
		c.es.Index.WithRefresh("true"),
		c.es.Index.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("index %s %s: %w", d.Type, d.ID, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() {
		return fmt.Errorf("index %s %s: %w", d.Type, d.ID, responseError(res))
	}
	return nil
}

// Delete removes a document. A document that isn't there counts as removed.
func (c *Client) Delete(ctx context.Context, docType, id string) error {
	res, err := c.es.Delete(c.index, documentID(docType, id),
		c.es.Delete.WithRefresh("true"),
		c.es.Delete.WithContext(ctx))
	if err != nil {
		return fmt.Errorf("delete %s %s: %w", docType, id, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.IsError() && res.StatusCode != http.StatusNotFound {
		return fmt.Errorf("delete %s %s: %w", docType, id, responseError(res))
	}
	return nil
}

// Keys lists every document in the index by type and id, a page at a time. Paging sorts on the two
// keyword fields so it needs no scroll state on the cluster.
func (c *Client) Keys(ctx context.Context) ([]DocKey, error) {
	size := c.keysPageSize
	if size <= 0 {
		size = defaultKeysPageSize
	}
	var keys []DocKey
	var after []any
	for {
		page, last, err := c.keysPage(ctx, size, after)
		if err != nil {
			return nil, err
		}
		keys = append(keys, page...)
		if len(page) < size {
			return keys, nil
		}
		after = last
	}
}

func (c *Client) keysPage(ctx context.Context, size int, after []any) (keys []DocKey, last []any, err error) {
	query := map[string]any{
		"size":    size,
		"_source": []string{"type", "id"},
		"query":   map[string]any{"match_all": map[string]any{}},
		"sort":    []any{map[string]any{"type": "asc"}, map[string]any{"id": "asc"}},
	}
	if after != nil {
		query["search_after"] = after
	}
	body, err := json.Marshal(query)
	if err != nil {
		return nil, nil, fmt.Errorf("encode key listing: %w", err)
	}
	res, err := c.es.Search(
		c.es.Search.WithIndex(c.index),
		c.es.Search.WithBody(bytes.NewReader(body)),
		c.es.Search.WithContext(ctx))
	if err != nil {
		return nil, nil, fmt.Errorf("list keys: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil, nil // nothing has been indexed yet
	}
	if res.IsError() {
		return nil, nil, fmt.Errorf("list keys: %w", responseError(res))
	}
	var parsed struct {
		Hits struct {
			Hits []struct {
				Source struct {
					Type string `json:"type"`
					ID   string `json:"id"`
				} `json:"_source"`
				Sort []any `json:"sort"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(res.Body).Decode(&parsed); err != nil {
		return nil, nil, fmt.Errorf("decode key listing: %w", err)
	}
	keys = make([]DocKey, 0, len(parsed.Hits.Hits))
	for _, h := range parsed.Hits.Hits {
		keys = append(keys, DocKey{Type: h.Source.Type, ID: h.Source.ID})
		last = h.Sort
	}
	return keys, last, nil
}

// Query asks for the documents closest to some text. It is the minimal search S6 builds matching on.
type Query struct {
	Text         string
	Tags         []string
	Type         string // empty for every type
	ExcludeOwner string // leaves out the asker's own documents
	Size         int    // defaults to 3
	Now          time.Time
}

// Hit is one document found.
type Hit struct {
	Type      string
	ID        string
	Text      string
	Tags      []string
	OwnerID   string
	ZoneID    string
	StartsAt  *time.Time
	ExpiresAt *time.Time
	Score     float64
}

// searchFilter applies to both halves of the hybrid query. Expired posts and finished spaces are
// left out here rather than relying on the sweep having removed them yet.
func searchFilter(q Query, now time.Time) map[string]any {
	mustNot := []any{
		map[string]any{"range": map[string]any{"expires_at": map[string]any{"lte": now.Format(time.RFC3339)}}},
		map[string]any{"range": map[string]any{"starts_at": map[string]any{"gt": now.Add(spaceHorizon).Format(time.RFC3339)}}},
	}
	if q.ExcludeOwner != "" {
		mustNot = append(mustNot, map[string]any{"term": map[string]any{"owner_id": q.ExcludeOwner}})
	}
	boolQuery := map[string]any{"must_not": mustNot}
	if q.Type != "" {
		boolQuery["filter"] = []any{map[string]any{"term": map[string]any{"type": q.Type}}}
	}
	return map[string]any{"bool": boolQuery}
}

// limitRunes cuts s to at most n characters.
func limitRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// searchBody builds a hybrid query: ELSER on the text and tag overlap, fused with reciprocal rank
// fusion. User text only ever appears as a JSON value.
func searchBody(q Query, now time.Time, size int) ([]byte, error) {
	tags := q.Tags
	if tags == nil {
		tags = []string{}
	}
	filter := searchFilter(q, now)
	return json.Marshal(map[string]any{
		"size":    size,
		"_source": []string{"type", "id", "body", "tags", "owner_id", "zone_id", "starts_at", "expires_at"},
		"retriever": map[string]any{"rrf": map[string]any{
			"rank_window_size": max(rankWindow, size),
			"rank_constant":    rankConstant,
			"retrievers": []any{
				map[string]any{"standard": map[string]any{
					"query":  map[string]any{"semantic": map[string]any{"field": "text", "query": limitRunes(q.Text, maxQueryRunes)}},
					"filter": filter,
				}},
				map[string]any{"standard": map[string]any{
					"query":  map[string]any{"terms": map[string]any{"tags": tags}},
					"filter": filter,
				}},
			},
		}},
	})
}

// Search runs the hybrid query and returns the top documents.
func (c *Client) Search(ctx context.Context, q Query) ([]Hit, error) {
	if q.Text == "" {
		return nil, errors.New("search: query text is empty")
	}
	now := q.Now
	if now.IsZero() {
		now = time.Now()
	}
	size := q.Size
	if size <= 0 {
		size = defaultResultSize
	}
	body, err := searchBody(q, now, size)
	if err != nil {
		return nil, fmt.Errorf("encode search: %w", err)
	}
	res, err := c.es.Search(
		c.es.Search.WithIndex(c.index),
		c.es.Search.WithBody(bytes.NewReader(body)),
		c.es.Search.WithContext(ctx))
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode == http.StatusNotFound {
		return nil, nil // nothing has been indexed yet
	}
	if res.IsError() {
		return nil, fmt.Errorf("search: %w", responseError(res))
	}
	return decodeHits(res.Body)
}

func decodeHits(r io.Reader) ([]Hit, error) {
	var parsed struct {
		Hits struct {
			Hits []struct {
				Score  float64 `json:"_score"`
				Source wireDoc `json:"_source"`
			} `json:"hits"`
		} `json:"hits"`
	}
	if err := json.NewDecoder(r).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decode search response: %w", err)
	}
	hits := make([]Hit, 0, len(parsed.Hits.Hits))
	for _, h := range parsed.Hits.Hits {
		s := h.Source
		hits = append(hits, Hit{
			Type: s.Type, ID: s.ID, Text: s.Body, Tags: s.Tags, OwnerID: s.OwnerID, ZoneID: s.ZoneID,
			StartsAt: s.StartsAt, ExpiresAt: s.ExpiresAt, Score: h.Score,
		})
	}
	return hits, nil
}
