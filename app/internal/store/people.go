package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Person roles, as stored in person.role. "host" and "visitor" aren't stored: a host is an attendee
// who owns a space and a visitor has no person row.
const (
	RoleAttendee   = "attendee"
	RoleSponsorRep = "sponsor_rep"
	RoleOrganizer  = "organizer"
)

// checkInTTL is how long a check-in counts as knowing where someone is (S2).
const checkInTTL = 45 * time.Minute

// Person is someone who has signed in.
type Person struct {
	ID           string
	ClerkUserID  string
	DisplayName  string
	Handle       string
	Role         string
	SponsorID    *string
	Tags         []string
	Discoverable bool
	ZoneID       *string
	ZoneSeenAt   *time.Time
}

const personColumns = `id::text, clerk_user_id, display_name, handle, role, sponsor_id::text, tags,
	discoverable, zone_id, zone_seen_at`

func scanPerson(row pgx.Row) (Person, error) {
	var p Person
	err := row.Scan(&p.ID, &p.ClerkUserID, &p.DisplayName, &p.Handle, &p.Role, &p.SponsorID, &p.Tags,
		&p.Discoverable, &p.ZoneID, &p.ZoneSeenAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Person{}, ErrNotFound
	}
	return p, err
}

// PersonByClerkID returns the person for a Clerk user, or ErrNotFound.
func (s *Store) PersonByClerkID(ctx context.Context, clerkUserID string) (Person, error) {
	p, err := scanPerson(s.db.QueryRow(ctx,
		`SELECT `+personColumns+` FROM person WHERE clerk_user_id = $1`, clerkUserID))
	if err != nil && !errors.Is(err, ErrNotFound) {
		return Person{}, fmt.Errorf("get person by clerk id: %w", err)
	}
	return p, err
}

// EnsurePerson creates the person for a Clerk user if needed and copies in the role and sponsor set
// in Clerk. A sponsor_rep whose sponsor name matches nothing is stored as an attendee, so a typo in
// the dashboard can never grant a rep's rights without a sponsor to scope them to.
func (s *Store) EnsurePerson(ctx context.Context, clerkUserID, role, sponsorName string) (Person, error) {
	if role != RoleSponsorRep && role != RoleOrganizer {
		role = RoleAttendee
	}
	var sponsorID *string
	if role == RoleSponsorRep {
		err := s.db.QueryRow(ctx, `SELECT id::text FROM sponsor WHERE name = $1`, sponsorName).Scan(&sponsorID)
		if errors.Is(err, pgx.ErrNoRows) {
			role = RoleAttendee
		} else if err != nil {
			return Person{}, fmt.Errorf("find sponsor: %w", err)
		}
	}
	p, err := scanPerson(s.db.QueryRow(ctx, `
		INSERT INTO person (clerk_user_id, role, sponsor_id) VALUES ($1, $2, $3)
		ON CONFLICT (clerk_user_id) DO UPDATE SET role = excluded.role, sponsor_id = excluded.sponsor_id
		RETURNING `+personColumns, clerkUserID, role, sponsorID))
	if err != nil {
		return Person{}, fmt.Errorf("ensure person: %w", err)
	}
	return p, nil
}

// CompleteOnboarding saves the display name and tags chosen on first sign-in.
func (s *Store) CompleteOnboarding(ctx context.Context, personID, displayName string, tags []string) (Person, error) {
	p, err := scanPerson(s.db.QueryRow(ctx, `
		UPDATE person SET display_name = $2, tags = $3 WHERE id = $1
		RETURNING `+personColumns, personID, displayName, tags))
	if err != nil {
		return Person{}, fmt.Errorf("complete onboarding for %s: %w", personID, err)
	}
	return p, nil
}

// SetDiscoverable sets whether a person can appear in other people's matches.
func (s *Store) SetDiscoverable(ctx context.Context, personID string, discoverable bool) error {
	if _, err := s.db.Exec(ctx, `UPDATE person SET discoverable = $2 WHERE id = $1`, personID, discoverable); err != nil {
		return fmt.Errorf("set discoverable for %s: %w", personID, err)
	}
	return nil
}

// FreshZone returns where a person last checked in. ok is false with no check-in or one older than
// 45 minutes, so a stale check-in counts as none everywhere.
func FreshZone(p Person, now time.Time) (zoneID string, ok bool) {
	if p.ZoneID == nil || p.ZoneSeenAt == nil || now.Sub(*p.ZoneSeenAt) > checkInTTL {
		return "", false
	}
	return *p.ZoneID, true
}
