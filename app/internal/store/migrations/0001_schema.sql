-- The whole schema from spec.entities, so parallel slices build on the same tables.
-- Later changes use a per-slice number range (S3 0300-0399, S4 0400-0499, and so on).

-- +goose Up
CREATE TABLE event (
  id        uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug      text NOT NULL UNIQUE,
  name      text NOT NULL,
  timezone  text NOT NULL,
  starts_at timestamptz NOT NULL,
  ends_at   timestamptz NOT NULL
);

CREATE TABLE zone (
  id       text PRIMARY KEY,
  event_id uuid NOT NULL REFERENCES event(id) ON DELETE CASCADE,
  name     text NOT NULL
);
CREATE INDEX zone_event_id_idx ON zone (event_id);

-- Undirected: each pair is stored once with a < b.
CREATE TABLE zone_walk (
  a       text NOT NULL REFERENCES zone(id) ON DELETE CASCADE,
  b       text NOT NULL REFERENCES zone(id) ON DELETE CASCADE,
  minutes int  NOT NULL CHECK (minutes > 0),
  PRIMARY KEY (a, b),
  CHECK (a < b)
);

CREATE TABLE spot (
  id       uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  zone_id  text NOT NULL REFERENCES zone(id),
  name     text NOT NULL UNIQUE,
  "where"  text NOT NULL DEFAULT '' CHECK (char_length("where") <= 80),
  capacity int  NOT NULL CHECK (capacity > 0),
  qr_slug  text NOT NULL UNIQUE CHECK (char_length(qr_slug) >= 16)
);
CREATE INDEX spot_zone_id_idx ON spot (zone_id);

CREATE TABLE sponsor (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  name          text NOT NULL UNIQUE,
  solves        text NOT NULL,
  booth_spot_id uuid REFERENCES spot(id) ON DELETE SET NULL
);

-- display_name is empty until onboarding: the row exists from the first signed-in request.
CREATE TABLE person (
  id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  clerk_user_id text NOT NULL UNIQUE,
  display_name  text NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 40),
  handle        text NOT NULL DEFAULT '',
  role          text NOT NULL DEFAULT 'attendee' CHECK (role IN ('attendee', 'sponsor_rep', 'organizer')),
  sponsor_id    uuid REFERENCES sponsor(id) ON DELETE SET NULL,
  tags          text[] NOT NULL DEFAULT '{}' CHECK (coalesce(array_length(tags, 1), 0) <= 8),
  discoverable  boolean NOT NULL DEFAULT true,
  zone_id       text REFERENCES zone(id) ON DELETE SET NULL,
  zone_seen_at  timestamptz
);
CREATE INDEX person_zone_id_idx ON person (zone_id);
CREATE INDEX person_sponsor_id_idx ON person (sponsor_id);

CREATE TABLE post (
  id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  author_id  uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  kind       text NOT NULL CHECK (kind IN ('stuck', 'curious', 'offer')),
  text       text NOT NULL CHECK (char_length(text) BETWEEN 10 AND 280),
  tags       text[] NOT NULL DEFAULT '{}',
  created_at timestamptz NOT NULL DEFAULT now(),
  expires_at timestamptz NOT NULL DEFAULT now() + interval '3 hours',
  status     text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'resolved', 'expired'))
);
CREATE INDEX post_author_id_idx ON post (author_id);
CREATE INDEX post_status_expires_at_idx ON post (status, expires_at);

CREATE TABLE space (
  id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
  slug         text NOT NULL UNIQUE,
  host_id      uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  from_post_id uuid REFERENCES post(id) ON DELETE SET NULL,
  title        text NOT NULL CHECK (char_length(title) BETWEEN 5 AND 80),
  blurb        text NOT NULL DEFAULT '' CHECK (char_length(blurb) <= 280),
  spot_id      uuid NOT NULL REFERENCES spot(id),
  starts_at    timestamptz NOT NULL,
  minutes      int  NOT NULL CHECK (minutes IN (15, 30, 45)),
  capacity     int  NOT NULL CHECK (capacity >= 2),
  status       text NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'cancelled'))
);
CREATE INDEX space_host_id_idx ON space (host_id);
CREATE INDEX space_spot_id_starts_at_idx ON space (spot_id, starts_at);
CREATE INDEX space_starts_at_idx ON space (starts_at);

CREATE TABLE signup (
  space_id   uuid NOT NULL REFERENCES space(id) ON DELETE CASCADE,
  person_id  uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  status     text NOT NULL CHECK (status IN ('going', 'waitlist', 'left')),
  created_at timestamptz NOT NULL DEFAULT now(),
  position   int,
  PRIMARY KEY (space_id, person_id)
);
CREATE INDEX signup_person_id_idx ON signup (person_id);

-- +goose Down
DROP TABLE signup;
DROP TABLE space;
DROP TABLE post;
DROP TABLE person;
DROP TABLE sponsor;
DROP TABLE spot;
DROP TABLE zone_walk;
DROP TABLE zone;
DROP TABLE event;
