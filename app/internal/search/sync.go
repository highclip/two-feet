package search

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/highclip/two-feet/app/internal/store"
)

const (
	sweepEvery = 30 * time.Second
	// personPostLimit caps how many open posts go into a person's document.
	personPostLimit = 10
	// flushTimeout bounds how long a rebuild waits for its own writes before trying again later.
	flushTimeout = 2 * time.Minute
)

// Sync keeps the index in step with Postgres. Failures are logged and never reach the caller's
// request; Postgres is the source of truth and the index can always be rebuilt from it.
type Sync struct {
	store *store.Store
	index Indexer
	log   *slog.Logger
}

// NewSync returns a Sync that writes through index.
func NewSync(st *store.Store, index Indexer, log *slog.Logger) *Sync {
	return &Sync{store: st, index: index, log: log}
}

func (s *Sync) indexDoc(ctx context.Context, d Doc) {
	if err := s.index.Index(ctx, d); err != nil {
		s.log.Error("index document", "doc_type", d.Type, "doc_id", d.ID, "err", err)
	}
}

func (s *Sync) deleteDoc(ctx context.Context, docType, id string) {
	if err := s.index.Delete(ctx, docType, id); err != nil {
		s.log.Error("delete document", "doc_type", docType, "doc_id", id, "err", err)
	}
}

// PostOpened indexes a new post and refreshes its author's person document.
func (s *Sync) PostOpened(ctx context.Context, p store.Post) {
	s.indexDoc(ctx, PostDoc(p))
	s.Person(ctx, p.AuthorID)
}

// PostClosed removes a resolved or expired post and refreshes its author's person document.
func (s *Sync) PostClosed(ctx context.Context, p store.Post) {
	s.deleteDoc(ctx, TypePost, p.ID)
	s.Person(ctx, p.AuthorID)
}

// Person rewrites someone's document, or removes it if they shouldn't be matched.
func (s *Sync) Person(ctx context.Context, personID string) {
	p, err := s.store.PersonByID(ctx, personID)
	if err != nil {
		s.log.Error("load person to index", "person_id", personID, "err", err)
		return
	}
	s.indexPerson(ctx, p, time.Now())
}

func (s *Sync) indexPerson(ctx context.Context, p store.Person, now time.Time) {
	posts, err := s.store.OpenPostsByAuthor(ctx, p.ID, now, personPostLimit)
	if err != nil {
		s.log.Error("load posts to index", "person_id", p.ID, "err", err)
		return
	}
	if d, ok := PersonDoc(p, posts); ok {
		s.indexDoc(ctx, d)
		return
	}
	s.deleteDoc(ctx, TypePerson, p.ID)
}

// ExpirePosts closes every post past its expires_at. Search already ignores them by date; this
// takes them out of the index and out of their authors' person documents.
func (s *Sync) ExpirePosts(ctx context.Context, now time.Time) error {
	posts, err := s.store.ExpirePosts(ctx, now)
	if err != nil {
		return err
	}
	authors := map[string]bool{}
	for _, p := range posts {
		s.deleteDoc(ctx, TypePost, p.ID)
		authors[p.AuthorID] = true
	}
	for id := range authors {
		s.Person(ctx, id)
	}
	return nil
}

// wanted builds every document Postgres says should be in the index. Any read error fails the whole
// call, because a partial answer would make the documents it missed look stale.
func (s *Sync) wanted(ctx context.Context, now time.Time) ([]Doc, error) {
	var docs []Doc
	sponsors, err := s.store.SponsorsForIndex(ctx)
	if err != nil {
		return nil, err
	}
	for _, sp := range sponsors {
		docs = append(docs, SponsorDoc(sp.ID, sp.Name, sp.Solves, sp.ZoneID))
	}
	spaces, err := s.store.OpenSpacesForIndex(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, sp := range spaces {
		docs = append(docs, SpaceDoc(sp.ID, sp.Title, sp.Blurb, sp.HostID, sp.ZoneID, sp.StartsAt, sp.Minutes))
	}
	posts, err := s.store.OpenPosts(ctx, now)
	if err != nil {
		return nil, err
	}
	for _, p := range posts {
		docs = append(docs, PostDoc(p))
	}
	people, err := s.store.DiscoverablePeople(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range people {
		open, err := s.store.OpenPostsByAuthor(ctx, p.ID, now, personPostLimit)
		if err != nil {
			return nil, err
		}
		if d, ok := PersonDoc(p, open); ok {
			docs = append(docs, d)
		}
	}
	return docs, nil
}

// All makes the index match Postgres: it writes every document that should be there, then removes
// every document that shouldn't, which is what repairs a lost delete.
func (s *Sync) All(ctx context.Context) error {
	docs, err := s.wanted(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	for _, d := range docs {
		s.indexDoc(ctx, d)
	}
	if r, ok := s.index.(Reconciler); ok {
		if err := s.prune(ctx, r); err != nil {
			return fmt.Errorf("reindex: %w", err)
		}
	}
	return nil
}

// prune deletes what the index holds and Postgres doesn't want. The order is what makes it safe
// while people keep posting. Writes follow the Postgres commit, so a listed document's row is
// already committed, and reading Postgres after the listing can't miss it. Flushing first means the
// listing also catches a document this rebuild has just written from a row closed since.
func (s *Sync) prune(ctx context.Context, r Reconciler) error {
	flushCtx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()
	if err := r.Flush(flushCtx); err != nil {
		return err
	}
	held, err := r.Keys(ctx)
	if err != nil {
		return err
	}
	docs, err := s.wanted(ctx, time.Now())
	if err != nil {
		return err
	}
	keep := make(map[DocKey]bool, len(docs))
	for _, d := range docs {
		keep[DocKey{Type: d.Type, ID: d.ID}] = true
	}
	for _, k := range held {
		if !keep[k] {
			s.deleteDoc(ctx, k.Type, k.ID)
		}
	}
	return nil
}

// Tick does one round of background upkeep: it expires posts and, once Elasticsearch is answering
// again, rebuilds the index if any write has been lost.
func (s *Sync) Tick(ctx context.Context, ready func(context.Context) error, q *Queue) {
	if err := s.ExpirePosts(ctx, time.Now()); err != nil {
		s.log.Error("expire posts", "err", err)
	}
	if !q.Failed() {
		return
	}
	if err := ready(ctx); err != nil {
		s.log.Warn("search not ready", "err", err)
		return
	}
	q.clearFailed()
	if err := s.All(ctx); err != nil {
		q.markFailed()
		s.log.Error("reindex", "err", err)
	}
}

// Run does upkeep until ctx ends. The first round fills the index, which is how the seeded sponsors
// get in and how a restart catches up.
func (s *Sync) Run(ctx context.Context, ready func(context.Context) error, q *Queue) {
	q.markFailed()
	ticker := time.NewTicker(sweepEvery)
	defer ticker.Stop()
	for {
		s.Tick(ctx, ready, q)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
