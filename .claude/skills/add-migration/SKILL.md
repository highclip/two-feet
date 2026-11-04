---
name: add-migration
description: Add a Postgres schema change with goose. Use whenever a slice needs a new table, column, index or constraint.
---

# Adding a migration

Migrations live in `app/internal/store/migrations/` and run with goose, both locally and as the
Helm pre-upgrade job in the cluster.

1. Find the next number: `ls app/internal/store/migrations/`. Use 4 digits: `0007_signups.sql`.
   Two builders can pick the same number at the same time; the integrator renumbers on merge if so.
2. Write both directions:

```sql
-- +goose Up
CREATE TABLE signup (
  space_id   uuid NOT NULL REFERENCES space(id) ON DELETE CASCADE,
  person_id  uuid NOT NULL REFERENCES person(id) ON DELETE CASCADE,
  status     text NOT NULL CHECK (status IN ('going', 'waitlist', 'left')),
  position   int,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (space_id, person_id)
);

-- +goose Down
DROP TABLE signup;
```

3. Apply locally: `cd app && goose -dir internal/store/migrations postgres "$DATABASE_URL" up`
4. Check `down` works too, then `up` again.

## Rules

- Field names and constraints come from `spec.entities`. Don't add columns the spec doesn't name
  without asking.
- Every foreign key gets an index if it's queried by that column.
- Never edit a migration that's already merged to main; add a new one.
- Keep migrations compatible with PlanetScale Postgres: no extensions beyond `pgcrypto`, and use
  `gen_random_uuid()` for ids.
