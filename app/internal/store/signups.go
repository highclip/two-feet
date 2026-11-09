package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// JoinCutoff is how long after its start a space still takes new people (D5).
const JoinCutoff = 10 * time.Minute

var (
	// ErrJoinClosed means the space started more than JoinCutoff ago.
	ErrJoinClosed = errors.New("joining has closed")
	// ErrSpaceOver means the space has finished.
	ErrSpaceOver = errors.New("space has finished")
	// ErrHostCannotLeave means the person hosts this space; they cancel it instead.
	ErrHostCannotLeave = errors.New("the host can't leave their own space")
	// ErrSeatTaken means an Undo came after someone else took the seat.
	ErrSeatTaken = errors.New("that seat has been taken")
	// ErrNotLeft means there is no leave to undo.
	ErrNotLeft = errors.New("not left")
)

// OverlapError refuses a join while the person is going to another space at the same time.
type OverlapError struct {
	SpaceID  string
	Slug     string
	Title    string
	StartsAt time.Time
	Hosting  bool
}

func (e *OverlapError) Error() string {
	return "already going to " + e.SpaceID
}

// JoinInput is one attempt to join. LeaveSpaceID, when set, is the overlapping space the person
// agreed to give up, left in the same transaction.
type JoinInput struct {
	SpaceID      string
	PersonID     string
	LeaveSpaceID string
	Now          time.Time
}

// JoinResult says where a join left the person. Changed is false for a repeat of a join that had
// already happened. Promoted is who moved in on LeftSpaceID, if the person gave up a seat there.
type JoinResult struct {
	Status        string
	WaitlistPlace int
	Changed       bool
	LeftSpaceID   string
	Promoted      []string
}

// LeaveResult is who moved in on the seat. Changed is false for someone who wasn't signed up.
type LeaveResult struct {
	Changed  bool
	WasGoing bool
	Promoted []string
}

// Signup is one person's place in one space. Status is empty when they aren't signed up.
type Signup struct {
	Status        string
	WaitlistPlace int
}

// lockedSpace is what a signup decision needs from the row it locked.
type lockedSpace struct {
	id       string
	hostID   string
	status   string
	startsAt time.Time
	minutes  int
	capacity int
}

func (sp lockedSpace) endsAt() time.Time {
	return sp.startsAt.Add(time.Duration(sp.minutes) * time.Minute)
}

func overlaps(a, b lockedSpace) bool {
	return a.startsAt.Before(b.endsAt()) && a.endsAt().After(b.startsAt)
}

func lockSpace(ctx context.Context, tx pgx.Tx, id string) (lockedSpace, error) {
	sp := lockedSpace{id: id}
	err := tx.QueryRow(ctx, `
		SELECT host_id::text, status, starts_at, minutes, capacity FROM space WHERE id = $1 FOR UPDATE`, id,
	).Scan(&sp.hostID, &sp.status, &sp.startsAt, &sp.minutes, &sp.capacity)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedSpace{}, ErrNotFound
	}
	if err != nil {
		return lockedSpace{}, fmt.Errorf("lock space %s: %w", id, err)
	}
	return sp, nil
}

// lockPerson makes one person's signups take turns, so two overlapping joins can't both pass the
// overlap check. It doesn't block the foreign-key checks of other people's writes.
func lockPerson(ctx context.Context, tx pgx.Tx, personID string) error {
	var id string
	err := tx.QueryRow(ctx, `SELECT id::text FROM person WHERE id = $1 FOR NO KEY UPDATE`, personID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock person %s: %w", personID, err)
	}
	return nil
}

// lockJoinSpaces locks the target and, for a switch, the space being given up, always in id order so
// two people switching opposite ways can't deadlock.
func lockJoinSpaces(ctx context.Context, tx pgx.Tx, targetID, leaveID string) (target, leaving lockedSpace, err error) {
	if leaveID == "" || leaveID == targetID {
		target, err = lockSpace(ctx, tx, targetID)
		return target, lockedSpace{}, err
	}
	first, second := targetID, leaveID
	if second < first {
		first, second = second, first
	}
	a, err := lockSpace(ctx, tx, first)
	if err != nil {
		return lockedSpace{}, lockedSpace{}, err
	}
	b, err := lockSpace(ctx, tx, second)
	if err != nil {
		return lockedSpace{}, lockedSpace{}, err
	}
	if first == targetID {
		return a, b, nil
	}
	return b, a, nil
}

// currentSignup reads a person's row, reporting an empty Signup when they have none or have left.
func currentSignup(ctx context.Context, tx DB, spaceID, personID string) (Signup, error) {
	var status string
	var position *int
	err := tx.QueryRow(ctx, `SELECT status, position FROM signup WHERE space_id = $1 AND person_id = $2`,
		spaceID, personID).Scan(&status, &position)
	if errors.Is(err, pgx.ErrNoRows) {
		return Signup{}, nil
	}
	if err != nil {
		return Signup{}, fmt.Errorf("read signup: %w", err)
	}
	switch {
	case status == SignupWaitlist && position != nil:
		place, err := placeInLine(ctx, tx, spaceID, *position)
		if err != nil {
			return Signup{}, err
		}
		return Signup{Status: status, WaitlistPlace: place}, nil
	case status == SignupLeft:
		return Signup{}, nil
	}
	return Signup{Status: status}, nil
}

func placeInLine(ctx context.Context, tx DB, spaceID string, position int) (int, error) {
	var place int
	err := tx.QueryRow(ctx, `
		SELECT count(*) FROM signup WHERE space_id = $1 AND status = 'waitlist' AND position <= $2`,
		spaceID, position).Scan(&place)
	if err != nil {
		return 0, fmt.Errorf("place in line: %w", err)
	}
	return place, nil
}

func seatCounts(ctx context.Context, tx pgx.Tx, spaceID string) (going, waiting int, err error) {
	err = tx.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE status = 'going'), count(*) FILTER (WHERE status = 'waitlist')
		FROM signup WHERE space_id = $1`, spaceID).Scan(&going, &waiting)
	if err != nil {
		return 0, 0, fmt.Errorf("count seats: %w", err)
	}
	return going, waiting, nil
}

// overlappingGoing finds an open space this person is going to at the same time as sp.
func overlappingGoing(ctx context.Context, tx pgx.Tx, sp lockedSpace, personID, excludingID string) (*OverlapError, error) {
	var found OverlapError
	err := tx.QueryRow(ctx, `
		SELECT other.id::text, other.slug, other.title, other.starts_at, other.host_id = $1
		FROM signup g
		JOIN space other ON other.id = g.space_id
		WHERE g.person_id = $1 AND g.status = 'going' AND other.status = 'open'
		  AND other.id <> $2 AND other.id IS DISTINCT FROM NULLIF($3, '')::uuid
		  AND other.starts_at < $4 AND other.starts_at + other.minutes * interval '1 minute' > $5
		ORDER BY other.starts_at
		LIMIT 1`, personID, sp.id, excludingID, sp.endsAt(), sp.startsAt,
	).Scan(&found.SpaceID, &found.Slug, &found.Title, &found.StartsAt, &found.Hosting)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil //nolint:nilnil // no overlap is the common answer, not an error
	}
	if err != nil {
		return nil, fmt.Errorf("check overlap: %w", err)
	}
	return &found, nil
}

// promote moves the first waitlisted people into any open seats and returns who moved. position goes
// back to null, which is how UndoLeave tells a seat from a place in line.
func promote(ctx context.Context, tx pgx.Tx, sp lockedSpace) ([]string, error) {
	going, waiting, err := seatCounts(ctx, tx, sp.id)
	if err != nil {
		return nil, err
	}
	free := sp.capacity - going
	if free <= 0 || waiting == 0 {
		return nil, nil
	}
	rows, err := tx.Query(ctx, `
		WITH next AS (
			SELECT person_id FROM signup
			WHERE space_id = $1 AND status = 'waitlist'
			ORDER BY position, created_at
			LIMIT $2
		)
		UPDATE signup s SET status = 'going', position = NULL
		FROM next
		WHERE s.space_id = $1 AND s.person_id = next.person_id
		RETURNING s.person_id::text`, sp.id, free)
	if err != nil {
		return nil, fmt.Errorf("promote waitlist: %w", err)
	}
	promoted, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("promote waitlist: %w", err)
	}
	return promoted, nil
}

// markLeft takes someone out of a space and fills the seat. It reports false when they weren't in.
func markLeft(ctx context.Context, tx pgx.Tx, sp lockedSpace, personID string) (LeaveResult, error) {
	var status string
	err := tx.QueryRow(ctx, `SELECT status FROM signup WHERE space_id = $1 AND person_id = $2`, sp.id, personID).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) || status == SignupLeft {
		return LeaveResult{}, nil
	}
	if err != nil {
		return LeaveResult{}, fmt.Errorf("read signup: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE signup SET status = 'left' WHERE space_id = $1 AND person_id = $2`, sp.id, personID); err != nil {
		return LeaveResult{}, fmt.Errorf("mark left: %w", err)
	}
	res := LeaveResult{Changed: true, WasGoing: status == SignupGoing}
	if res.WasGoing {
		if res.Promoted, err = promote(ctx, tx, sp); err != nil {
			return LeaveResult{}, err
		}
	}
	return res, nil
}

// JoinSpace signs someone up, or puts them on the waitlist. The space lock is what keeps going at or
// under capacity however many people join at once, and it makes line order the order of joining.
func (s *Store) JoinSpace(ctx context.Context, in JoinInput) (JoinResult, error) {
	var out JoinResult
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		out = JoinResult{}
		if err := lockPerson(ctx, tx, in.PersonID); err != nil {
			return err
		}
		sp, leaving, err := lockJoinSpaces(ctx, tx, in.SpaceID, in.LeaveSpaceID)
		if err != nil {
			return err
		}
		if sp.status != SpaceOpen {
			return ErrSpaceCancelled
		}
		if in.Now.After(sp.startsAt.Add(JoinCutoff)) {
			return ErrJoinClosed
		}
		existing, err := currentSignup(ctx, tx, sp.id, in.PersonID)
		if err != nil {
			return err
		}
		if existing.Status != "" {
			out = JoinResult{Status: existing.Status, WaitlistPlace: existing.WaitlistPlace}
			return nil
		}

		// Only an open space that really clashes can be given up: leave is a form field, and the space
		// may have been cancelled or moved since the prompt was shown.
		if leaving.id != "" && (leaving.status != SpaceOpen || !overlaps(leaving, sp)) {
			leaving = lockedSpace{}
		}
		if clash, err := overlappingGoing(ctx, tx, sp, in.PersonID, leaving.id); err != nil {
			return err
		} else if clash != nil {
			return clash
		}
		if leaving.id != "" {
			if leaving.hostID == in.PersonID {
				return ErrHostCannotLeave
			}
			left, err := markLeft(ctx, tx, leaving, in.PersonID)
			if err != nil {
				return err
			}
			if left.Changed {
				out.LeftSpaceID = leaving.id
				out.Promoted = left.Promoted
			}
		}

		going, waiting, err := seatCounts(ctx, tx, sp.id)
		if err != nil {
			return err
		}
		status, position := SignupWaitlist, new(int)
		if going < sp.capacity && waiting == 0 {
			status, position = SignupGoing, nil
		} else if err := tx.QueryRow(ctx, `
			SELECT coalesce(max(position), 0) + 1 FROM signup WHERE space_id = $1`, sp.id).Scan(position); err != nil {
			return fmt.Errorf("next position: %w", err)
		}
		// clock_timestamp, not now(): now() is when this transaction began, before it waited its turn.
		if _, err := tx.Exec(ctx, `
			INSERT INTO signup (space_id, person_id, status, position, created_at)
			VALUES ($1, $2, $3, $4, clock_timestamp())
			ON CONFLICT (space_id, person_id) DO UPDATE
			SET status = EXCLUDED.status, position = EXCLUDED.position, created_at = EXCLUDED.created_at`,
			sp.id, in.PersonID, status, position); err != nil {
			return fmt.Errorf("save signup: %w", err)
		}
		out.Status, out.Changed = status, true
		if position != nil {
			out.WaitlistPlace, err = placeInLine(ctx, tx, sp.id, *position)
			return err
		}
		return nil
	})
	if err != nil {
		return JoinResult{}, fmt.Errorf("join space %s: %w", in.SpaceID, err)
	}
	return out, nil
}

// LeaveSpace takes someone out of a space and moves the next person in. The row stays as 'left' with
// its created_at and position, so UndoLeave can put them back where they were.
func (s *Store) LeaveSpace(ctx context.Context, spaceID, personID string, now time.Time) (LeaveResult, error) {
	var out LeaveResult
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockPerson(ctx, tx, personID); err != nil {
			return err
		}
		sp, err := lockSpace(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		if sp.status != SpaceOpen {
			return ErrSpaceCancelled
		}
		if !now.Before(sp.endsAt()) {
			return ErrSpaceOver
		}
		if sp.hostID == personID {
			return ErrHostCannotLeave
		}
		out, err = markLeft(ctx, tx, sp, personID)
		return err
	})
	if err != nil {
		return LeaveResult{}, fmt.Errorf("leave space %s: %w", spaceID, err)
	}
	return out, nil
}

// UndoLeave puts someone back in their old seat or place in line. A seat can only come back if it's
// still free: someone else may have taken it since.
func (s *Store) UndoLeave(ctx context.Context, spaceID, personID string, now time.Time) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		if err := lockPerson(ctx, tx, personID); err != nil {
			return err
		}
		sp, err := lockSpace(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		if sp.status != SpaceOpen {
			return ErrSpaceCancelled
		}
		if !now.Before(sp.endsAt()) {
			return ErrSpaceOver
		}
		var status string
		var position *int
		err = tx.QueryRow(ctx, `SELECT status, position FROM signup WHERE space_id = $1 AND person_id = $2`,
			spaceID, personID).Scan(&status, &position)
		if errors.Is(err, pgx.ErrNoRows) || status != SignupLeft {
			return ErrNotLeft
		}
		if err != nil {
			return fmt.Errorf("read signup: %w", err)
		}
		restored := SignupWaitlist
		if position == nil {
			going, waiting, err := seatCounts(ctx, tx, spaceID)
			if err != nil {
				return err
			}
			if going >= sp.capacity || waiting > 0 {
				return ErrSeatTaken
			}
			if clash, err := overlappingGoing(ctx, tx, sp, personID, ""); err != nil {
				return err
			} else if clash != nil {
				return clash
			}
			restored = SignupGoing
		}
		if _, err := tx.Exec(ctx, `UPDATE signup SET status = $3 WHERE space_id = $1 AND person_id = $2`,
			spaceID, personID, restored); err != nil {
			return fmt.Errorf("restore signup: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("undo leave %s: %w", spaceID, err)
	}
	return nil
}

// FillOpenSeats moves waitlisted people in after a capacity change, and returns who moved.
func (s *Store) FillOpenSeats(ctx context.Context, spaceID string, now time.Time) ([]string, error) {
	var promoted []string
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		sp, err := lockSpace(ctx, tx, spaceID)
		if err != nil {
			return err
		}
		if sp.status != SpaceOpen || !now.Before(sp.endsAt()) {
			return nil
		}
		promoted, err = promote(ctx, tx, sp)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("fill seats in %s: %w", spaceID, err)
	}
	return promoted, nil
}

// SignupOf returns one person's place in one space.
func (s *Store) SignupOf(ctx context.Context, spaceID, personID string) (Signup, error) {
	got, err := currentSignup(ctx, s.db, spaceID, personID)
	if err != nil {
		return Signup{}, fmt.Errorf("signup of %s in %s: %w", personID, spaceID, err)
	}
	return got, nil
}

// Joinable reports whether the space still takes new people at now.
func (sp Space) Joinable(now time.Time) bool {
	return sp.Status == SpaceOpen && !now.After(sp.StartsAt.Add(JoinCutoff))
}
