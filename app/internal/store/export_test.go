package store

import (
	"context"
	"fmt"
)

// ExecForTest runs a statement on the test's transaction.
func (s *Store) ExecForTest(ctx context.Context, sql string) error {
	_, err := s.db.Exec(ctx, sql)
	return err
}

// CountForTest counts the rows in a table named by the test itself.
func (s *Store) CountForTest(ctx context.Context, table string) (int, error) {
	var n int
	err := s.db.QueryRow(ctx, fmt.Sprintf("SELECT count(*) FROM %q", table)).Scan(&n)
	return n, err
}

// SpotQRSlugs maps spot name to qr_slug.
func (s *Store) SpotQRSlugs(ctx context.Context) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `SELECT name, qr_slug FROM spot`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, slug string
		if err := rows.Scan(&name, &slug); err != nil {
			return nil, err
		}
		out[name] = slug
	}
	return out, rows.Err()
}
