package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpacesBetween lists an event's open spaces that are running at some point between from and to,
// by start time.
func (s *Store) SpacesBetween(ctx context.Context, eventID string, from, to time.Time) ([]Space, error) {
	rows, err := s.db.Query(ctx, spaceSelect+`
		WHERE z.event_id = $1 AND sp.status = 'open'
		  AND sp.starts_at < $3 AND sp.starts_at + sp.minutes * interval '1 minute' > $2
		ORDER BY sp.starts_at`, eventID, from, to)
	if err != nil {
		return nil, fmt.Errorf("list spaces between: %w", err)
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		sp, err := scanSpace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan space: %w", err)
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list spaces between: %w", err)
	}
	return out, nil
}

// PostLine is a post with nothing that says who wrote it, for public screens.
type PostLine struct {
	Kind string
	Text string
}

// LatestPostLines returns the newest open posts, newest first.
func (s *Store) LatestPostLines(ctx context.Context, now time.Time, limit int) ([]PostLine, error) {
	rows, err := s.db.Query(ctx, `
		SELECT kind, text FROM post WHERE status = 'open' AND expires_at > $1
		ORDER BY created_at DESC LIMIT $2`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("list latest posts: %w", err)
	}
	lines, err := pgx.CollectRows(rows, pgx.RowToStructByPos[PostLine])
	if err != nil {
		return nil, fmt.Errorf("list latest posts: %w", err)
	}
	return lines, nil
}
