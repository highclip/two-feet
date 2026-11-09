// Package search indexes posts, spaces and sponsors in Elasticsearch. S5 supplies the real indexer.
package search

import (
	"context"
	"time"
)

// Doc is one searchable document: an open post, an upcoming space or a sponsor. OwnerID lets
// matching leave out an asker's own posts and spaces.
type Doc struct {
	Type      string
	ID        string
	Text      string
	Tags      []string
	OwnerID   string
	ZoneID    string
	StartsAt  *time.Time
	ExpiresAt *time.Time
}

// Indexer writes documents. Call it after the Postgres commit; a failure never fails the request.
type Indexer interface {
	Index(ctx context.Context, d Doc) error
	Delete(ctx context.Context, docType, id string) error
}

// Noop is an Indexer that does nothing.
type Noop struct{}

// Index does nothing.
func (Noop) Index(context.Context, Doc) error { return nil }

// Delete does nothing.
func (Noop) Delete(context.Context, string, string) error { return nil }
