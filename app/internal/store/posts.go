package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Post kinds, as stored in post.kind.
const (
	PostStuck   = "stuck"
	PostCurious = "curious"
	PostOffer   = "offer"
)

// Post statuses, as stored in post.status.
const (
	PostOpen     = "open"
	PostResolved = "resolved"
	PostExpired  = "expired"
)

// Post is one line someone is stuck on, curious about or can help with.
type Post struct {
	ID        string
	AuthorID  string
	Kind      string
	Text      string
	Tags      []string
	CreatedAt time.Time
	ExpiresAt time.Time
	Status    string
}

const postColumns = `id::text, author_id::text, kind, text, tags, created_at, expires_at, status`

func scanPost(row pgx.Row) (Post, error) {
	var p Post
	err := row.Scan(&p.ID, &p.AuthorID, &p.Kind, &p.Text, &p.Tags, &p.CreatedAt, &p.ExpiresAt, &p.Status)
	if errors.Is(err, pgx.ErrNoRows) {
		return Post{}, ErrNotFound
	}
	return p, err
}

func collectPosts(rows pgx.Rows) ([]Post, error) {
	defer rows.Close()
	var out []Post
	for rows.Next() {
		p, err := scanPost(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// isUUID reports whether s has the shape Postgres accepts for a uuid, so that a mangled id from a
// URL is simply not found instead of a query error.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, c := range s {
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case '0' <= c && c <= '9', 'a' <= c && c <= 'f', 'A' <= c && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// CreatePost saves an open post. It expires three hours after it's created, by the column default.
func (s *Store) CreatePost(ctx context.Context, authorID, kind, text string, tags []string) (Post, error) {
	p, err := scanPost(s.db.QueryRow(ctx, `
		INSERT INTO post (author_id, kind, text, tags) VALUES ($1, $2, $3, $4)
		RETURNING `+postColumns, authorID, kind, text, tags))
	if err != nil {
		return Post{}, fmt.Errorf("create post for %s: %w", authorID, err)
	}
	return p, nil
}

// PostByID returns a post, or ErrNotFound.
func (s *Store) PostByID(ctx context.Context, id string) (Post, error) {
	if !isUUID(id) {
		return Post{}, ErrNotFound
	}
	p, err := scanPost(s.db.QueryRow(ctx, `SELECT `+postColumns+` FROM post WHERE id = $1`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Post{}, fmt.Errorf("get post %s: %w", id, err)
	}
	return p, err
}

// ResolvePost closes an author's open post. It returns ErrNotFound when the post isn't theirs or is
// no longer open.
func (s *Store) ResolvePost(ctx context.Context, id, authorID string) (Post, error) {
	if !isUUID(id) {
		return Post{}, ErrNotFound
	}
	p, err := scanPost(s.db.QueryRow(ctx, `
		UPDATE post SET status = 'resolved' WHERE id = $1 AND author_id = $2 AND status = 'open'
		RETURNING `+postColumns, id, authorID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Post{}, fmt.Errorf("resolve post %s: %w", id, err)
	}
	return p, err
}

// ExpirePosts marks every open post past its expires_at as expired and returns them.
func (s *Store) ExpirePosts(ctx context.Context, now time.Time) ([]Post, error) {
	rows, err := s.db.Query(ctx, `
		UPDATE post SET status = 'expired' WHERE status = 'open' AND expires_at <= $1
		RETURNING `+postColumns, now)
	if err != nil {
		return nil, fmt.Errorf("expire posts: %w", err)
	}
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, fmt.Errorf("expire posts: %w", err)
	}
	return posts, nil
}

// OpenPosts lists every post that is open and unexpired, newest first.
func (s *Store) OpenPosts(ctx context.Context, now time.Time) ([]Post, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+postColumns+` FROM post WHERE status = 'open' AND expires_at > $1
		ORDER BY created_at DESC`, now)
	if err != nil {
		return nil, fmt.Errorf("list open posts: %w", err)
	}
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, fmt.Errorf("list open posts: %w", err)
	}
	return posts, nil
}

// OpenPostsByAuthor lists a person's newest open, unexpired posts.
func (s *Store) OpenPostsByAuthor(ctx context.Context, authorID string, now time.Time, limit int) ([]Post, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+postColumns+` FROM post WHERE author_id = $1 AND status = 'open' AND expires_at > $2
		ORDER BY created_at DESC LIMIT $3`, authorID, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list open posts by %s: %w", authorID, err)
	}
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, fmt.Errorf("list open posts by %s: %w", authorID, err)
	}
	return posts, nil
}

// RecentPostTexts returns the words of the newest open posts, with no author, for the composer's
// rotating placeholder.
func (s *Store) RecentPostTexts(ctx context.Context, now time.Time, limit int) ([]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT text FROM post WHERE status = 'open' AND expires_at > $1
		ORDER BY created_at DESC LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list recent post texts: %w", err)
	}
	texts, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list recent post texts: %w", err)
	}
	return texts, nil
}
