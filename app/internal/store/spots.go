package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SpotInEvent returns the spot a printed code points at, or ErrNotFound if the code isn't one of
// this event's.
func (s *Store) SpotInEvent(ctx context.Context, eventID, qrSlug string) (Spot, error) {
	var sp Spot
	err := s.db.QueryRow(ctx, `
		SELECT s.id::text, s.zone_id, s.name, s."where", s.capacity, s.qr_slug
		FROM spot s JOIN zone z ON z.id = s.zone_id
		WHERE s.qr_slug = $1 AND z.event_id = $2`, qrSlug, eventID,
	).Scan(&sp.ID, &sp.ZoneID, &sp.Name, &sp.Where, &sp.Capacity, &sp.QRSlug)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spot{}, ErrNotFound
	}
	if err != nil {
		return Spot{}, fmt.Errorf("get spot by qr slug: %w", err)
	}
	return sp, nil
}

// SpotSchedule lists a spot's open spaces that haven't finished at from and start before until,
// soonest first.
func (s *Store) SpotSchedule(ctx context.Context, spotID string, from, until time.Time) ([]Space, error) {
	rows, err := s.db.Query(ctx, spaceSelect+`
		WHERE sp.spot_id = $1 AND sp.status = 'open'
		  AND sp.starts_at + sp.minutes * interval '1 minute' > $2 AND sp.starts_at < $3
		ORDER BY sp.starts_at`, spotID, from, until)
	if err != nil {
		return nil, fmt.Errorf("list schedule for spot %s: %w", spotID, err)
	}
	defer rows.Close()
	var out []Space
	for rows.Next() {
		sp, err := scanSpace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan schedule for spot %s: %w", spotID, err)
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list schedule for spot %s: %w", spotID, err)
	}
	return out, nil
}

// CheckIn records that a person was in a zone at a moment.
func (s *Store) CheckIn(ctx context.Context, personID, zoneID string, at time.Time) error {
	if _, err := s.db.Exec(ctx, `UPDATE person SET zone_id = $2, zone_seen_at = $3 WHERE id = $1`,
		personID, zoneID, at); err != nil {
		return fmt.Errorf("check in %s: %w", personID, err)
	}
	return nil
}

// ZoneCounts returns how many people have a fresh check-in in each of an event's zones. Zones with
// nobody in them are absent. These are raw numbers: nothing may show one below 3 (spec.security).
func (s *Store) ZoneCounts(ctx context.Context, eventID string, now time.Time) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.zone_id, count(*)
		FROM person p JOIN zone z ON z.id = p.zone_id
		WHERE z.event_id = $1 AND p.zone_seen_at >= $2
		GROUP BY p.zone_id`, eventID, now.Add(-checkInTTL))
	if err != nil {
		return nil, fmt.Errorf("count people in zones: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var zone string
		var n int
		if err := rows.Scan(&zone, &n); err != nil {
			return nil, fmt.Errorf("scan zone count: %w", err)
		}
		counts[zone] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count people in zones: %w", err)
	}
	return counts, nil
}
