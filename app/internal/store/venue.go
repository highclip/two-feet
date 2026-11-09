package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNoWalk means there is no walking route between two zones.
var ErrNoWalk = errors.New("no walk between zones")

// Event is one open-space event.
type Event struct {
	ID       string
	Slug     string
	Name     string
	Timezone string
	StartsAt time.Time
	EndsAt   time.Time
}

// Spot is a place with a QR code taped to it.
type Spot struct {
	ID       string
	ZoneID   string
	Name     string
	Where    string
	Capacity int
	QRSlug   string
}

// Sponsor is a seeded sponsor. BoothSpotID stays nil until the venue mapping is decided.
type Sponsor struct {
	ID          string
	Name        string
	Solves      string
	BoothSpotID *string
}

// EventBySlug returns an event, or ErrNotFound.
func (s *Store) EventBySlug(ctx context.Context, slug string) (Event, error) {
	var e Event
	err := s.db.QueryRow(ctx, `
		SELECT id::text, slug, name, timezone, starts_at, ends_at FROM event WHERE slug = $1`, slug,
	).Scan(&e.ID, &e.Slug, &e.Name, &e.Timezone, &e.StartsAt, &e.EndsAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrNotFound
	}
	if err != nil {
		return Event{}, fmt.Errorf("get event %s: %w", slug, err)
	}
	return e, nil
}

// SpotByQRSlug returns the spot a printed code points at, or ErrNotFound.
func (s *Store) SpotByQRSlug(ctx context.Context, qrSlug string) (Spot, error) {
	var sp Spot
	err := s.db.QueryRow(ctx, `
		SELECT id::text, zone_id, name, "where", capacity, qr_slug FROM spot WHERE qr_slug = $1`, qrSlug,
	).Scan(&sp.ID, &sp.ZoneID, &sp.Name, &sp.Where, &sp.Capacity, &sp.QRSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spot{}, ErrNotFound
	}
	if err != nil {
		return Spot{}, fmt.Errorf("get spot by qr slug: %w", err)
	}
	return sp, nil
}

// Sponsors lists every sponsor by name.
func (s *Store) Sponsors(ctx context.Context) ([]Sponsor, error) {
	rows, err := s.db.Query(ctx, `SELECT id::text, name, solves, booth_spot_id::text FROM sponsor ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("list sponsors: %w", err)
	}
	defer rows.Close()
	var out []Sponsor
	for rows.Next() {
		var sp Sponsor
		if err := rows.Scan(&sp.ID, &sp.Name, &sp.Solves, &sp.BoothSpotID); err != nil {
			return nil, fmt.Errorf("scan sponsor: %w", err)
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sponsors: %w", err)
	}
	return out, nil
}

// WalkMinutes returns the shortest walk between two zones over zone_walk, and 0 inside one zone.
// It returns ErrNoWalk when no route connects them.
func (s *Store) WalkMinutes(ctx context.Context, fromZone, toZone string) (int, error) {
	if fromZone == toZone {
		return 0, nil
	}
	var minutes *int
	err := s.db.QueryRow(ctx, `
		WITH RECURSIVE edge AS (
			SELECT a AS src, b AS dst, minutes FROM zone_walk
			UNION ALL
			SELECT b, a, minutes FROM zone_walk
		), route AS (
			SELECT dst AS zone, minutes, ARRAY[$1::text, dst] AS seen
			FROM edge WHERE src = $1
			UNION ALL
			SELECT e.dst, r.minutes + e.minutes, r.seen || e.dst
			FROM route r JOIN edge e ON e.src = r.zone
			WHERE NOT e.dst = ANY (r.seen)
		)
		SELECT min(minutes) FROM route WHERE zone = $2`, fromZone, toZone).Scan(&minutes)
	if err != nil {
		return 0, fmt.Errorf("walk from %s to %s: %w", fromZone, toZone, err)
	}
	if minutes == nil {
		return 0, fmt.Errorf("walk from %s to %s: %w", fromZone, toZone, ErrNoWalk)
	}
	return *minutes, nil
}
