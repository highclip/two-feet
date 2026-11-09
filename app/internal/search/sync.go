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

// All writes every document Postgres says should be in the index.
func (s *Sync) All(ctx context.Context) error {
	now := time.Now()
	sponsors, err := s.store.SponsorsForIndex(ctx)
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	for _, sp := range sponsors {
		s.indexDoc(ctx, SponsorDoc(sp.ID, sp.Name, sp.Solves, sp.ZoneID))
	}
	spaces, err := s.store.OpenSpacesForIndex(ctx, now)
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	for _, sp := range spaces {
		s.indexDoc(ctx, SpaceDoc(sp.ID, sp.Title, sp.Blurb, sp.HostID, sp.ZoneID, sp.StartsAt, sp.Minutes))
	}
	posts, err := s.store.OpenPosts(ctx, now)
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	for _, p := range posts {
		s.indexDoc(ctx, PostDoc(p))
	}
	people, err := s.store.DiscoverablePeople(ctx)
	if err != nil {
		return fmt.Errorf("reindex: %w", err)
	}
	for _, p := range people {
		s.indexPerson(ctx, p, now)
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
