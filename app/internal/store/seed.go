package store

import (
	"context"
	"crypto/rand"
	_ "embed" // seed_data.json
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// seedData holds the venue layout and sponsors. Edit it and re-run the seed to change the venue.
//
//go:embed seed_data.json
var seedData []byte

type seedFile struct {
	Event struct {
		Name     string    `json:"name"`
		Timezone string    `json:"timezone"`
		StartsAt time.Time `json:"starts_at"`
		EndsAt   time.Time `json:"ends_at"`
	} `json:"event"`
	Zones []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"zones"`
	Walks []struct {
		A       string `json:"a"`
		B       string `json:"b"`
		Minutes int    `json:"minutes"`
	} `json:"walks"`
	Spots []struct {
		Name     string `json:"name"`
		Zone     string `json:"zone"`
		Where    string `json:"where"`
		Capacity int    `json:"capacity"`
	} `json:"spots"`
	Sponsors []struct {
		Name   string `json:"name"`
		Solves string `json:"solves"`
		Booth  string `json:"booth"` // optional: the name of a spot in Spots
	} `json:"sponsors"`
}

// Seed loads the event, venue and sponsors, and can run any number of times. Spots upsert by name,
// so a re-run applies edits and keeps the qr_slug already printed on a sign. Sponsors are only
// inserted: after that the solves text belongs to the sponsor's rep (S7). A sponsor's optional
// "booth" names a spot in the file and is applied on every run, so mapping booths is a data edit.
func (s *Store) Seed(ctx context.Context, eventSlug string) error {
	var data seedFile
	if err := json.Unmarshal(seedData, &data); err != nil {
		return fmt.Errorf("parse seed data: %w", err)
	}
	err := pgx.BeginFunc(ctx, s.db, func(tx pgx.Tx) error {
		var eventID string
		err := tx.QueryRow(ctx, `
			INSERT INTO event (slug, name, timezone, starts_at, ends_at)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (slug) DO UPDATE
			SET name = excluded.name, timezone = excluded.timezone,
			    starts_at = excluded.starts_at, ends_at = excluded.ends_at
			RETURNING id::text`,
			eventSlug, data.Event.Name, data.Event.Timezone, data.Event.StartsAt, data.Event.EndsAt,
		).Scan(&eventID)
		if err != nil {
			return fmt.Errorf("upsert event: %w", err)
		}

		for _, z := range data.Zones {
			_, err := tx.Exec(ctx, `
				INSERT INTO zone (id, event_id, name) VALUES ($1, $2, $3)
				ON CONFLICT (id) DO UPDATE SET event_id = excluded.event_id, name = excluded.name`,
				z.ID, eventID, z.Name)
			if err != nil {
				return fmt.Errorf("upsert zone %s: %w", z.ID, err)
			}
		}

		// Nothing references a walk, so the file simply replaces them.
		if _, err := tx.Exec(ctx, `
			DELETE FROM zone_walk WHERE a IN (SELECT id FROM zone WHERE event_id = $1)`, eventID); err != nil {
			return fmt.Errorf("clear walks: %w", err)
		}
		for _, w := range data.Walks {
			a, b := w.A, w.B
			if a > b {
				a, b = b, a
			}
			if _, err := tx.Exec(ctx, `INSERT INTO zone_walk (a, b, minutes) VALUES ($1, $2, $3)`, a, b, w.Minutes); err != nil {
				return fmt.Errorf("insert walk %s-%s: %w", a, b, err)
			}
		}

		for _, sp := range data.Spots {
			slug, err := newSlug()
			if err != nil {
				return err
			}
			_, err = tx.Exec(ctx, `
				INSERT INTO spot (zone_id, name, "where", capacity, qr_slug)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (name) DO UPDATE
				SET zone_id = excluded.zone_id, "where" = excluded."where", capacity = excluded.capacity`,
				sp.Zone, sp.Name, sp.Where, sp.Capacity, slug)
			if err != nil {
				return fmt.Errorf("upsert spot %s: %w", sp.Name, err)
			}
		}

		for _, sp := range data.Sponsors {
			_, err := tx.Exec(ctx, `
				INSERT INTO sponsor (name, solves) VALUES ($1, $2)
				ON CONFLICT (name) DO NOTHING`, sp.Name, sp.Solves)
			if err != nil {
				return fmt.Errorf("insert sponsor %s: %w", sp.Name, err)
			}
			if sp.Booth != "" {
				if err := New(tx).SetSponsorBooth(ctx, sp.Name, sp.Booth); err != nil {
					return err
				}
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("seed: %w", err)
	}
	return nil
}

// newSlug returns 20 unguessable characters for use in a URL.
func newSlug() (string, error) {
	const alphabet = "abcdefghijklmnopqrstuvwxyz234567" // 32 symbols, so a byte mask stays uniform.
	b := make([]byte, 20)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("random slug: %w", err)
	}
	for i := range b {
		b[i] = alphabet[b[i]&31]
	}
	return string(b), nil
}
