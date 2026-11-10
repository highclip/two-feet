package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FreeSpotAt returns the roomiest spot with nothing booked for that whole time, and its zone's name,
// or ErrNotFound when every spot is taken. It's a suggestion: creating the space still checks.
func (s *Store) FreeSpotAt(ctx context.Context, eventID string, starts time.Time, minutes int) (Spot, string, error) {
	ends := starts.Add(time.Duration(minutes) * time.Minute)
	var sp Spot
	var zone string
	err := s.db.QueryRow(ctx, `
		SELECT sp.id::text, sp.zone_id, sp.name, sp."where", sp.capacity, sp.qr_slug, z.name
		FROM spot sp JOIN zone z ON z.id = sp.zone_id
		WHERE z.event_id = $1
		  AND NOT EXISTS (
			SELECT 1 FROM space busy
			WHERE busy.spot_id = sp.id AND busy.status = 'open'
			  AND busy.starts_at < $3 AND busy.starts_at + busy.minutes * interval '1 minute' > $2)
		ORDER BY sp.capacity DESC, sp.name
		LIMIT 1`, eventID, starts, ends).
		Scan(&sp.ID, &sp.ZoneID, &sp.Name, &sp.Where, &sp.Capacity, &sp.QRSlug, &zone)
	if errors.Is(err, pgx.ErrNoRows) {
		return Spot{}, "", ErrNotFound
	}
	if err != nil {
		return Spot{}, "", fmt.Errorf("find a free spot: %w", err)
	}
	return sp, zone, nil
}
