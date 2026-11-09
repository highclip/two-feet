// Package search indexes posts, spaces, sponsors and people in Elasticsearch and finds them again.
package search

import (
	"context"
	"time"
)

// Document types, as stored in the index's type field.
const (
	TypePost   = "post"
	TypeSpace  = "space"
	TypePerson = "person"
)

// Doc is one searchable document: an open post, an upcoming space, a sponsor or a discoverable
// person. OwnerID lets matching leave out an asker's own posts and spaces. Only spaces and sponsors
// ever carry a ZoneID, so the index can't say where a person is.
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

// DocKey names one document in the index.
type DocKey struct {
	Type string
	ID   string
}

// KeyLister reports every document an index holds.
type KeyLister interface {
	Keys(ctx context.Context) ([]DocKey, error)
}

// Reconciler is an Indexer a rebuild can also remove stale documents through. Flush returns once
// the writes already handed over have been made, so Keys sees them.
type Reconciler interface {
	KeyLister
	Flush(ctx context.Context) error
}

// Noop is an Indexer that does nothing.
type Noop struct{}

// Index does nothing.
func (Noop) Index(context.Context, Doc) error { return nil }

// Delete does nothing.
func (Noop) Delete(context.Context, string, string) error { return nil }
