package store

import (
	"context"
	"fmt"
)

// CountOpenSpaces counts the spaces not cancelled and not yet over, for the spaces_open metric.
func (s *Store) CountOpenSpaces(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, `
		SELECT count(*) FROM space
		WHERE status = 'open' AND starts_at + make_interval(mins => minutes) > now()`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count open spaces: %w", err)
	}
	return n, nil
}
