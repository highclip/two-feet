package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Booth is where to find a sponsor. WalkMinutes is nil when the walk can't be known: the person has
// no fresh check-in, or no route connects the zones.
type Booth struct {
	SpotName    string
	Where       string
	ZoneID      string
	ZoneName    string
	WalkMinutes *int
}

const sponsorColumns = `id::text, name, solves, booth_spot_id::text`

func scanSponsor(row pgx.Row) (Sponsor, error) {
	var sp Sponsor
	err := row.Scan(&sp.ID, &sp.Name, &sp.Solves, &sp.BoothSpotID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Sponsor{}, ErrNotFound
	}
	return sp, err
}

// SponsorByID returns a sponsor, or ErrNotFound.
func (s *Store) SponsorByID(ctx context.Context, id string) (Sponsor, error) {
	sp, err := scanSponsor(s.db.QueryRow(ctx, `SELECT `+sponsorColumns+` FROM sponsor WHERE id = $1`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Sponsor{}, fmt.Errorf("get sponsor %s: %w", id, err)
	}
	return sp, err
}

// UpdateSponsorSolves saves a sponsor's "solves" text and returns the updated sponsor, or ErrNotFound.
func (s *Store) UpdateSponsorSolves(ctx context.Context, id, solves string) (Sponsor, error) {
	sp, err := scanSponsor(s.db.QueryRow(ctx,
		`UPDATE sponsor SET solves = $2 WHERE id = $1 RETURNING `+sponsorColumns, id, solves))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Sponsor{}, fmt.Errorf("update sponsor %s: %w", id, err)
	}
	return sp, err
}

// SetSponsorBooth points a sponsor at a spot, both by name as they appear in the seed. It returns
// ErrNotFound if either name matches nothing.
func (s *Store) SetSponsorBooth(ctx context.Context, sponsorName, spotName string) error {
	tag, err := s.db.Exec(ctx, `
		UPDATE sponsor SET booth_spot_id = spot.id
		FROM spot WHERE sponsor.name = $1 AND spot.name = $2`, sponsorName, spotName)
	if err != nil {
		return fmt.Errorf("set booth of %s to %s: %w", sponsorName, spotName, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("set booth of %s to %s: %w", sponsorName, spotName, ErrNotFound)
	}
	return nil
}

// SponsorBooth returns a sponsor's booth and the walk to it from fromZone, which is empty when the
// person has no fresh check-in. It returns ErrNotFound for a sponsor with no booth yet.
func (s *Store) SponsorBooth(ctx context.Context, sponsorID, fromZone string) (Booth, error) {
	var b Booth
	err := s.db.QueryRow(ctx, `
		SELECT spot.name, spot."where", zone.id, zone.name
		FROM sponsor
		JOIN spot ON spot.id = sponsor.booth_spot_id
		JOIN zone ON zone.id = spot.zone_id
		WHERE sponsor.id = $1`, sponsorID,
	).Scan(&b.SpotName, &b.Where, &b.ZoneID, &b.ZoneName)
	if errors.Is(err, pgx.ErrNoRows) {
		return Booth{}, ErrNotFound
	}
	if err != nil {
		return Booth{}, fmt.Errorf("get booth of sponsor %s: %w", sponsorID, err)
	}
	if fromZone == "" {
		return b, nil
	}
	minutes, err := s.WalkMinutes(ctx, fromZone, b.ZoneID)
	if errors.Is(err, ErrNoWalk) {
		return b, nil
	}
	if err != nil {
		return Booth{}, err
	}
	b.WalkMinutes = &minutes
	return b, nil
}
