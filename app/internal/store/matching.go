package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// These reads turn search hits back into current rows. Postgres has the last word on matches: a
// document can outlive the row it came from by a retry or a sweep, and a person's opt-out applies
// at once.

// validIDs drops anything that isn't a uuid, so one odd document can't fail a whole query.
func validIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if isUUID(id) {
			out = append(out, id)
		}
	}
	return out
}

// SpacesByIDs returns open and cancelled spaces by id.
func (s *Store) SpacesByIDs(ctx context.Context, ids []string) (map[string]Space, error) {
	rows, err := s.db.Query(ctx, spaceSelect+` WHERE sp.id = ANY($1::uuid[])`, validIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("get spaces: %w", err)
	}
	defer rows.Close()
	out := make(map[string]Space, len(ids))
	for rows.Next() {
		sp, err := scanSpace(rows)
		if err != nil {
			return nil, fmt.Errorf("scan space: %w", err)
		}
		out[sp.ID] = sp
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get spaces: %w", err)
	}
	return out, nil
}

// SignupStatuses says which of these spaces a person is going to or waitlisted for.
func (s *Store) SignupStatuses(ctx context.Context, personID string, spaceIDs []string) (map[string]string, error) {
	rows, err := s.db.Query(ctx, `
		SELECT space_id::text, status FROM signup
		WHERE person_id = $1 AND space_id = ANY($2::uuid[]) AND status IN ('going', 'waitlist')`,
		personID, validIDs(spaceIDs))
	if err != nil {
		return nil, fmt.Errorf("get signups of %s: %w", personID, err)
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, status string
		if err := rows.Scan(&id, &status); err != nil {
			return nil, fmt.Errorf("scan signup: %w", err)
		}
		out[id] = status
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get signups of %s: %w", personID, err)
	}
	return out, nil
}

// MatchablePeople returns the people among ids who have chosen a name and can be matched.
func (s *Store) MatchablePeople(ctx context.Context, ids []string) (map[string]Person, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+personColumns+` FROM person
		WHERE id = ANY($1::uuid[]) AND discoverable AND display_name <> ''`, validIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("get matchable people: %w", err)
	}
	defer rows.Close()
	out := make(map[string]Person, len(ids))
	for rows.Next() {
		p, err := scanPerson(rows)
		if err != nil {
			return nil, fmt.Errorf("scan person: %w", err)
		}
		out[p.ID] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get matchable people: %w", err)
	}
	return out, nil
}

// OpenPostsByAuthors returns each author's open, unexpired posts, newest first.
func (s *Store) OpenPostsByAuthors(ctx context.Context, authorIDs []string, now time.Time) (map[string][]Post, error) {
	rows, err := s.db.Query(ctx, `
		SELECT `+postColumns+` FROM post
		WHERE author_id = ANY($1::uuid[]) AND status = 'open' AND expires_at > $2
		ORDER BY created_at DESC`, validIDs(authorIDs), now)
	if err != nil {
		return nil, fmt.Errorf("list posts by authors: %w", err)
	}
	posts, err := collectPosts(rows)
	if err != nil {
		return nil, fmt.Errorf("list posts by authors: %w", err)
	}
	out := make(map[string][]Post, len(authorIDs))
	for _, p := range posts {
		out[p.AuthorID] = append(out[p.AuthorID], p)
	}
	return out, nil
}

// PostWithAuthor is an open post and what may be said about who wrote it.
type PostWithAuthor struct {
	Post         Post
	AuthorName   string
	Discoverable bool
}

// OpenPostsByIDs returns the open, unexpired posts among ids.
func (s *Store) OpenPostsByIDs(ctx context.Context, ids []string, now time.Time) (map[string]PostWithAuthor, error) {
	rows, err := s.db.Query(ctx, `
		SELECT p.id::text, p.author_id::text, p.kind, p.text, p.tags, p.created_at, p.expires_at, p.status,
			a.display_name, a.discoverable
		FROM post p JOIN person a ON a.id = p.author_id
		WHERE p.id = ANY($1::uuid[]) AND p.status = 'open' AND p.expires_at > $2`, validIDs(ids), now)
	if err != nil {
		return nil, fmt.Errorf("get posts: %w", err)
	}
	defer rows.Close()
	out := make(map[string]PostWithAuthor, len(ids))
	for rows.Next() {
		var row PostWithAuthor
		p := &row.Post
		if err := rows.Scan(&p.ID, &p.AuthorID, &p.Kind, &p.Text, &p.Tags, &p.CreatedAt, &p.ExpiresAt, &p.Status,
			&row.AuthorName, &row.Discoverable); err != nil {
			return nil, fmt.Errorf("scan post: %w", err)
		}
		out[p.ID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get posts: %w", err)
	}
	return out, nil
}

// SponsorWithBooth is a sponsor and, when it has one, where its booth is.
type SponsorWithBooth struct {
	Sponsor Sponsor
	Booth   *Booth
}

// SponsorsWithBooths returns sponsors by id. A booth's WalkMinutes is left for the caller.
func (s *Store) SponsorsWithBooths(ctx context.Context, ids []string) (map[string]SponsorWithBooth, error) {
	rows, err := s.db.Query(ctx, `
		SELECT sponsor.id::text, sponsor.name, sponsor.solves, sponsor.booth_spot_id::text,
			spot.name, spot."where", zone.id, zone.name
		FROM sponsor
		LEFT JOIN spot ON spot.id = sponsor.booth_spot_id
		LEFT JOIN zone ON zone.id = spot.zone_id
		WHERE sponsor.id = ANY($1::uuid[])`, validIDs(ids))
	if err != nil {
		return nil, fmt.Errorf("get sponsors: %w", err)
	}
	defer rows.Close()
	out := make(map[string]SponsorWithBooth, len(ids))
	for rows.Next() {
		var row SponsorWithBooth
		var spot, where, zoneID, zoneName *string
		sp := &row.Sponsor
		if err := rows.Scan(&sp.ID, &sp.Name, &sp.Solves, &sp.BoothSpotID, &spot, &where, &zoneID, &zoneName); err != nil {
			return nil, fmt.Errorf("scan sponsor: %w", err)
		}
		if spot != nil && where != nil && zoneID != nil && zoneName != nil {
			row.Booth = &Booth{SpotName: *spot, Where: *where, ZoneID: *zoneID, ZoneName: *zoneName}
		}
		out[sp.ID] = row
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("get sponsors: %w", err)
	}
	return out, nil
}

// WalksFrom returns the shortest walk from a zone to every zone it can reach, itself included at 0.
func (s *Store) WalksFrom(ctx context.Context, fromZone string) (map[string]int, error) {
	rows, err := s.db.Query(ctx, `
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
		SELECT zone, min(minutes) FROM route GROUP BY zone`, fromZone)
	if err != nil {
		return nil, fmt.Errorf("walks from %s: %w", fromZone, err)
	}
	defer rows.Close()
	out := map[string]int{fromZone: 0}
	for rows.Next() {
		var zone string
		var minutes int
		if err := rows.Scan(&zone, &minutes); err != nil {
			return nil, fmt.Errorf("scan walk: %w", err)
		}
		out[zone] = minutes
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("walks from %s: %w", fromZone, err)
	}
	return out, nil
}

// Sample people carry this prefix on their Clerk id, which no real Clerk user can have.
const samplePrefix = "sample:"

// SamplePerson creates or finds one sample person for the demo seed.
func (s *Store) SamplePerson(ctx context.Context, key, displayName string, tags []string) (Person, error) {
	p, err := s.EnsurePerson(ctx, samplePrefix+key, RoleAttendee, "")
	if err != nil {
		return Person{}, err
	}
	return s.CompleteOnboarding(ctx, p.ID, displayName, tags)
}

// ClearSamples deletes every sample person. Their posts, spaces and signups go with them.
func (s *Store) ClearSamples(ctx context.Context) error {
	if _, err := s.db.Exec(ctx, `DELETE FROM person WHERE clerk_user_id LIKE $1`, samplePrefix+"%"); err != nil {
		return fmt.Errorf("clear sample people: %w", err)
	}
	return nil
}

// AddGoing marks people as going to a space, for the demo seed. It skips the capacity check that
// real joins go through, so it's only for rows the seed itself made.
func (s *Store) AddGoing(ctx context.Context, spaceID string, personIDs []string) error {
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		for _, id := range personIDs {
			if _, err := tx.Exec(ctx, `
				INSERT INTO signup (space_id, person_id, status) VALUES ($1, $2, 'going')
				ON CONFLICT (space_id, person_id) DO NOTHING`, spaceID, id); err != nil {
				return fmt.Errorf("add %s: %w", id, err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("add going to %s: %w", spaceID, err)
	}
	return nil
}
