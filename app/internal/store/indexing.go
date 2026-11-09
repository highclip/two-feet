package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpaceIndexRow is what the search index needs to know about a space.
type SpaceIndexRow struct {
	ID       string
	Title    string
	Blurb    string
	HostID   string
	ZoneID   string
	StartsAt time.Time
	Minutes  int
}

// SponsorIndexRow is what the search index needs to know about a sponsor. ZoneID is empty for a
// sponsor with no booth spot yet.
type SponsorIndexRow struct {
	ID     string
	Name   string
	Solves string
	ZoneID string
}

// PersonByID returns a person, or ErrNotFound.
func (s *Store) PersonByID(ctx context.Context, id string) (Person, error) {
	if !isUUID(id) {
		return Person{}, ErrNotFound
	}
	p, err := scanPerson(s.db.QueryRow(ctx, `SELECT `+personColumns+` FROM person WHERE id = $1`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Person{}, fmt.Errorf("get person %s: %w", id, err)
	}
	return p, err
}

// DiscoverablePeople lists everyone who has finished onboarding and can appear in matches.
func (s *Store) DiscoverablePeople(ctx context.Context) ([]Person, error) {
	rows, err := s.db.Query(ctx, `SELECT `+personColumns+` FROM person WHERE discoverable AND display_name <> ''`)
	if err != nil {
		return nil, fmt.Errorf("list discoverable people: %w", err)
	}
	defer rows.Close()
	var out []Person
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list discoverable people: %w", err)
	}
	return out, nil
}

// OpenSpacesForIndex lists the open spaces that haven't ended yet.
func (s *Store) OpenSpacesForIndex(ctx context.Context, now time.Time) ([]SpaceIndexRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sp.id::text, sp.title, sp.blurb, sp.host_id::text, spot.zone_id, sp.starts_at, sp.minutes
		FROM space sp JOIN spot ON spot.id = sp.spot_id
		WHERE sp.status = 'open' AND sp.starts_at + sp.minutes * interval '1 minute' > $1`, now)
	if err != nil {
		return nil, fmt.Errorf("list spaces to index: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SpaceIndexRow])
	if err != nil {
		return nil, fmt.Errorf("list spaces to index: %w", err)
	}
	return out, nil
}

// SponsorsForIndex lists every sponsor with the zone of its booth, if it has one.
func (s *Store) SponsorsForIndex(ctx context.Context) ([]SponsorIndexRow, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sponsor.id::text, sponsor.name, sponsor.solves, coalesce(spot.zone_id, '')
		FROM sponsor LEFT JOIN spot ON spot.id = sponsor.booth_spot_id`)
	if err != nil {
		return nil, fmt.Errorf("list sponsors to index: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[SponsorIndexRow])
	if err != nil {
		return nil, fmt.Errorf("list sponsors to index: %w", err)
	}
	return out, nil
}
