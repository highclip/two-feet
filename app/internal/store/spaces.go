package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Space statuses, as stored in space.status. Full, happening and done are derived.
const (
	SpaceOpen      = "open"
	SpaceCancelled = "cancelled"
)

// Signup statuses, as stored in signup.status.
const (
	SignupGoing    = "going"
	SignupWaitlist = "waitlist"
	SignupLeft     = "left"
)

// ErrSpaceCancelled means the change needs an open space and this one is cancelled.
var ErrSpaceCancelled = errors.New("space is cancelled")

// ErrSpaceNotCancelled means there is nothing to restore.
var ErrSpaceNotCancelled = errors.New("space is not cancelled")

// SpotTakenError refuses a space that overlaps another at the same spot. Alternative names a free
// spot big enough for the same time, or is empty when there isn't one.
type SpotTakenError struct {
	Spot        string
	Until       time.Time
	Alternative string
}

func (e *SpotTakenError) Error() string {
	return fmt.Sprintf("%s is taken until %s", e.Spot, e.Until.Format(time.RFC3339))
}

// OverCapacityError refuses a capacity the spot can't seat.
type OverCapacityError struct {
	Spot  string
	Seats int
}

func (e *OverCapacityError) Error() string {
	return fmt.Sprintf("%s seats %d", e.Spot, e.Seats)
}

// BelowGoingError refuses a capacity smaller than the number of people already going.
type BelowGoingError struct {
	Going int
}

func (e *BelowGoingError) Error() string {
	return fmt.Sprintf("%d people are going", e.Going)
}

// Space is a space with what its pages need from the tables around it. Kind is the kind of the
// post it came from, empty when it didn't come from one.
type Space struct {
	ID         string
	Slug       string
	HostID     string
	HostName   string
	FromPostID *string
	Kind       string
	Title      string
	Blurb      string
	SpotID     string
	SpotName   string
	SpotWhere  string
	ZoneID     string
	ZoneName   string
	StartsAt   time.Time
	Minutes    int
	Capacity   int
	Status     string
	Going      int
	Waitlist   int
}

// EndsAt is when the space finishes.
func (sp Space) EndsAt() time.Time {
	return sp.StartsAt.Add(time.Duration(sp.Minutes) * time.Minute)
}

// SpaceInput is what a host fills in. Capacity 0 means everyone the spot seats.
type SpaceInput struct {
	Title      string
	Blurb      string
	SpotID     string
	FromPostID *string
	StartsAt   time.Time
	Minutes    int
	Capacity   int
}

// Attendee is someone going to a space.
type Attendee struct {
	ID   string
	Name string
}

// PostSummary is the part of a post that seeds a space.
type PostSummary struct {
	Text string
	Tags []string
}

const spaceSelect = `
	SELECT sp.id::text, sp.slug, sp.host_id::text, h.display_name, sp.from_post_id::text, coalesce(po.kind, ''),
		sp.title, sp.blurb, sp.spot_id::text, s.name, s."where", s.zone_id, z.name,
		sp.starts_at, sp.minutes, sp.capacity, sp.status,
		(SELECT count(*) FROM signup g WHERE g.space_id = sp.id AND g.status = 'going'),
		(SELECT count(*) FROM signup g WHERE g.space_id = sp.id AND g.status = 'waitlist')
	FROM space sp
	JOIN person h ON h.id = sp.host_id
	JOIN spot s ON s.id = sp.spot_id
	JOIN zone z ON z.id = s.zone_id
	LEFT JOIN post po ON po.id = sp.from_post_id`

func scanSpace(row pgx.Row) (Space, error) {
	var sp Space
	err := row.Scan(&sp.ID, &sp.Slug, &sp.HostID, &sp.HostName, &sp.FromPostID, &sp.Kind,
		&sp.Title, &sp.Blurb, &sp.SpotID, &sp.SpotName, &sp.SpotWhere, &sp.ZoneID, &sp.ZoneName,
		&sp.StartsAt, &sp.Minutes, &sp.Capacity, &sp.Status, &sp.Going, &sp.Waitlist)
	if errors.Is(err, pgx.ErrNoRows) {
		return Space{}, ErrNotFound
	}
	return sp, err
}

// Spots lists an event's spots by name.
func (s *Store) Spots(ctx context.Context, eventID string) ([]Spot, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sp.id::text, sp.zone_id, sp.name, sp."where", sp.capacity, sp.qr_slug
		FROM spot sp JOIN zone z ON z.id = sp.zone_id
		WHERE z.event_id = $1
		ORDER BY sp.name`, eventID)
	if err != nil {
		return nil, fmt.Errorf("list spots: %w", err)
	}
	defer rows.Close()
	var out []Spot
	for rows.Next() {
		var sp Spot
		if err := rows.Scan(&sp.ID, &sp.ZoneID, &sp.Name, &sp.Where, &sp.Capacity, &sp.QRSlug); err != nil {
			return nil, fmt.Errorf("scan spot: %w", err)
		}
		out = append(out, sp)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list spots: %w", err)
	}
	return out, nil
}

// SpaceBySlug returns a space in an event, or ErrNotFound.
func (s *Store) SpaceBySlug(ctx context.Context, eventID, slug string) (Space, error) {
	sp, err := scanSpace(s.db.QueryRow(ctx, spaceSelect+` WHERE sp.slug = $1 AND z.event_id = $2`, slug, eventID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Space{}, fmt.Errorf("get space by slug: %w", err)
	}
	return sp, err
}

// SpaceByID returns a space, or ErrNotFound.
func (s *Store) SpaceByID(ctx context.Context, id string) (Space, error) {
	sp, err := scanSpace(s.db.QueryRow(ctx, spaceSelect+` WHERE sp.id = $1`, id))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Space{}, fmt.Errorf("get space %s: %w", id, err)
	}
	return sp, err
}

// GoingTo lists who is going to a space, the host first and then in the order they joined.
func (s *Store) GoingTo(ctx context.Context, spaceID string) ([]Attendee, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.display_name
		FROM signup g
		JOIN person p ON p.id = g.person_id
		JOIN space sp ON sp.id = g.space_id
		WHERE g.space_id = $1 AND g.status = 'going'
		ORDER BY (p.id = sp.host_id) DESC, g.created_at, p.id`, spaceID)
	if err != nil {
		return nil, fmt.Errorf("list going to %s: %w", spaceID, err)
	}
	defer rows.Close()
	var out []Attendee
	for rows.Next() {
		var a Attendee
		if err := rows.Scan(&a.ID, &a.Name); err != nil {
			return nil, fmt.Errorf("scan attendee: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list going to %s: %w", spaceID, err)
	}
	return out, nil
}

// PostForSpace returns an open, unexpired post to start a space from, or ErrNotFound.
func (s *Store) PostForSpace(ctx context.Context, postID string, now time.Time) (PostSummary, error) {
	var p PostSummary
	err := s.db.QueryRow(ctx, `
		SELECT text, tags FROM post WHERE id = $1 AND status = 'open' AND expires_at > $2`, postID, now,
	).Scan(&p.Text, &p.Tags)
	if errors.Is(err, pgx.ErrNoRows) {
		return PostSummary{}, ErrNotFound
	}
	if err != nil {
		return PostSummary{}, fmt.Errorf("get post %s: %w", postID, err)
	}
	return p, nil
}

// CreateSpace saves a space with the host as its first "going". The lock on the spot makes two
// overlapping creates take turns, so the second one sees the first.
func (s *Store) CreateSpace(ctx context.Context, hostID string, in SpaceInput) (Space, error) {
	slug, err := newSlug()
	if err != nil {
		return Space{}, err
	}
	var id string
	err = pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		capacity, err := checkSpot(ctx, tx, nil, in)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `
			INSERT INTO space (slug, host_id, from_post_id, title, blurb, spot_id, starts_at, minutes, capacity)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
			RETURNING id::text`,
			slug, hostID, in.FromPostID, in.Title, in.Blurb, in.SpotID, in.StartsAt, in.Minutes, capacity,
		).Scan(&id)
		if err != nil {
			return fmt.Errorf("insert space: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO signup (space_id, person_id, status) VALUES ($1, $2, 'going')`, id, hostID); err != nil {
			return fmt.Errorf("add host signup: %w", err)
		}
		return nil
	})
	if err != nil {
		return Space{}, fmt.Errorf("create space: %w", err)
	}
	return s.SpaceByID(ctx, id)
}

// UpdateSpace changes an open space. It locks the space first, as signups do, so the capacity can't
// slip below the number going while it's checked.
func (s *Store) UpdateSpace(ctx context.Context, id string, in SpaceInput) (Space, error) {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockOpenSpace(ctx, tx, id); err != nil {
			return err
		}
		capacity, err := checkSpot(ctx, tx, &id, in)
		if err != nil {
			return err
		}
		var going int
		err = tx.QueryRow(ctx, `SELECT count(*) FROM signup WHERE space_id = $1 AND status = 'going'`, id).Scan(&going)
		if err != nil {
			return fmt.Errorf("count going: %w", err)
		}
		if capacity < going {
			return &BelowGoingError{Going: going}
		}
		_, err = tx.Exec(ctx, `
			UPDATE space SET title = $2, blurb = $3, spot_id = $4, starts_at = $5, minutes = $6, capacity = $7
			WHERE id = $1`, id, in.Title, in.Blurb, in.SpotID, in.StartsAt, in.Minutes, capacity)
		if err != nil {
			return fmt.Errorf("update row: %w", err)
		}
		return nil
	})
	if err != nil {
		return Space{}, fmt.Errorf("update space %s: %w", id, err)
	}
	return s.SpaceByID(ctx, id)
}

// CancelSpace cancels an open space and returns who to tell: everyone going or waitlisted. Their
// signups stay, so RestoreSpace puts them back in the same seats.
func (s *Store) CancelSpace(ctx context.Context, id string) ([]string, error) {
	var told []string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockOpenSpace(ctx, tx, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE space SET status = 'cancelled' WHERE id = $1`, id); err != nil {
			return fmt.Errorf("update row: %w", err)
		}
		var err error
		told, err = signedUp(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("cancel space %s: %w", id, err)
	}
	return told, nil
}

// RestoreSpace reopens a cancelled space if its spot is still free, and returns who to tell.
func (s *Store) RestoreSpace(ctx context.Context, id string) ([]string, error) {
	var told []string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var in SpaceInput
		var status string
		err := tx.QueryRow(ctx, `
			SELECT status, spot_id::text, starts_at, minutes, capacity FROM space WHERE id = $1 FOR UPDATE`, id,
		).Scan(&status, &in.SpotID, &in.StartsAt, &in.Minutes, &in.Capacity)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("lock space: %w", err)
		}
		if status != SpaceCancelled {
			return ErrSpaceNotCancelled
		}
		if _, err := checkSpot(ctx, tx, &id, in); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE space SET status = 'open' WHERE id = $1`, id); err != nil {
			return fmt.Errorf("update row: %w", err)
		}
		told, err = signedUp(ctx, tx, id)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("restore space %s: %w", id, err)
	}
	return told, nil
}

func lockOpenSpace(ctx context.Context, tx pgx.Tx, id string) error {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM space WHERE id = $1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock space: %w", err)
	}
	if status != SpaceOpen {
		return ErrSpaceCancelled
	}
	return nil
}

func signedUp(ctx context.Context, tx pgx.Tx, spaceID string) ([]string, error) {
	rows, err := tx.Query(ctx, `
		SELECT person_id::text FROM signup
		WHERE space_id = $1 AND status IN ('going', 'waitlist')
		ORDER BY created_at, person_id`, spaceID)
	if err != nil {
		return nil, fmt.Errorf("list signups: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list signups: %w", err)
	}
	return ids, nil
}

// checkSpot locks the spot and checks that it can hold this space at this time, ignoring the space
// itself when excluding is set. It returns the capacity to store.
func checkSpot(ctx context.Context, tx pgx.Tx, excluding *string, in SpaceInput) (int, error) {
	var name string
	var seats int
	err := tx.QueryRow(ctx, `SELECT name, capacity FROM spot WHERE id = $1 FOR UPDATE`, in.SpotID).Scan(&name, &seats)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock spot: %w", err)
	}
	capacity := in.Capacity
	if capacity == 0 {
		capacity = seats
	}
	if capacity > seats {
		return 0, &OverCapacityError{Spot: name, Seats: seats}
	}

	ends := in.StartsAt.Add(time.Duration(in.Minutes) * time.Minute)
	var until *time.Time
	err = tx.QueryRow(ctx, `
		SELECT max(starts_at + minutes * interval '1 minute') FROM space
		WHERE spot_id = $1 AND status = 'open' AND id IS DISTINCT FROM $2
		  AND starts_at < $3 AND starts_at + minutes * interval '1 minute' > $4`,
		in.SpotID, excluding, ends, in.StartsAt).Scan(&until)
	if err != nil {
		return 0, fmt.Errorf("check overlap: %w", err)
	}
	if until == nil {
		return capacity, nil
	}

	// A blank capacity means "whatever fits", so any spot can be offered instead.
	wanted := max(in.Capacity, 2)
	var alternative string
	err = tx.QueryRow(ctx, `
		SELECT other.name
		FROM spot other
		JOIN zone oz ON oz.id = other.zone_id
		WHERE oz.event_id = (SELECT z.event_id FROM spot s JOIN zone z ON z.id = s.zone_id WHERE s.id = $1)
		  AND other.id <> $1 AND other.capacity >= $2
		  AND NOT EXISTS (
			SELECT 1 FROM space busy
			WHERE busy.spot_id = other.id AND busy.status = 'open' AND busy.id IS DISTINCT FROM $5
			  AND busy.starts_at < $3 AND busy.starts_at + busy.minutes * interval '1 minute' > $4)
		ORDER BY other.name
		LIMIT 1`, in.SpotID, wanted, ends, in.StartsAt, excluding).Scan(&alternative)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find another spot: %w", err)
	}
	return 0, &SpotTakenError{Spot: name, Until: *until, Alternative: alternative}
}
