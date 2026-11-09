# Two Feet: rules for every agent

Read this, then `spec/spec.yaml`, before doing anything. The spec is the contract. If something
isn't in it, it's out of scope: escalate with `hx ask` instead of guessing.

## The harness

- The event log is the source of truth. Write to it with `python3 harness/hx.py`. Builders already
  have `HX_AGENT` and `HX_SLICE` set; everyone else passes their role: `python3 harness/hx.py --agent sec-review ...`.
  Always start the command with `python3 harness/hx.py` (no env-var prefix) so it matches the allow list.
- One slice, one worktree, one branch: `slice/S<n>-<short-name>`. Never commit to `main` directly;
  only the integrator merges.
- You may move your slice to `building`. Only `harness/gates.sh` moves it to `gates` and `review`.
  Don't write `gate.result` events yourself.
- Each slice's worktree has its own `DATABASE_URL`, `ELASTIC_INDEX_PREFIX` and `PORT` (`scripts/slice-env.sh`).
  Use them; never hardcode a database, index name or port.
- Builders working in parallel see each other through `python3 harness/hx.py peers <id>`, not chat.
- Stuck on something the spec doesn't answer? Ask, with 2-4 concrete options, and wait:
  `hx ask --slice S3 "Can a guest claim more than one seat?" --option "One" --option "Host sets a limit"`
- Don't edit `spec/spec.yaml`, `CLAUDE.md`, `.claude/`, `harness/` or `infra/` unless your task says to.

## Code layout (app/)

```
app/
  cmd/twofeet/main.go        wiring only: config, deps, router, server
  internal/config            env parsing; fails fast on a missing variable
  internal/store             Postgres access with pgx; all SQL lives here
  internal/store/migrations  goose SQL migrations, numbered 0001_*.sql
  internal/search            Elasticsearch indexing and matching
  internal/live              SSE hub and topics (see spec `live`)
  internal/auth              Clerk middleware, current person, role checks
  internal/web               handlers, one file per area (spots.go, spaces.go, ...)
  internal/web/views         templ components; tokens from design/tokens.css
  internal/telemetry         OpenTelemetry setup
  e2e/                       Playwright tests, phone viewport by default
```

## Contract with the deploy (infra/)

- `app/Dockerfile` builds one static binary on distroless, like `infra/smoke/Dockerfile`, and
  takes `--build-arg VERSION`. CI switches from the smoke service to the app as soon as it exists.
- The binary serves `GET /healthz` → `ok <version>` (CI waits for the new version there) and
  `GET /readyz` → 200 once the process can serve.
- `<binary> migrate` runs the embedded goose migrations and exits. The Helm pre-upgrade job calls it.
- All config comes from the env vars in `infra/local/.env.example`. In the cluster they come from
  the `two-feet-env` secret; OpenTelemetry uses the standard `OTEL_*` variables.
- Server-sent events: no `WriteTimeout` on the server, flush after every event, heartbeat every 15 s.

## Go conventions

- Go 1.23+, standard library first. `net/http` with Go 1.22 route patterns; no web framework.
- Dependencies allowed without asking: pgx/v5, goose, templ, clerk-sdk-go/v2, OpenTelemetry,
  go-elasticsearch/v8, testify. Anything else: `hx ask`.
- Errors are wrapped with context (`fmt.Errorf("join space %s: %w", id, err)`). No panics in handlers.
- Every handler that changes state checks the role from `spec.permissions` in the handler, never only
  in a template.
- SQL is parameterized. Never build SQL or Elasticsearch queries by concatenating user text.
- User text is plain text: length-limited on input, escaped on output (templ does this; never
  use `templ.Raw` on user content).
- Times are stored in UTC and shown in America/Denver.
- Identifiers that appear in URLs are unguessable (random slugs), never sequential ids.

## Tests

- Write the acceptance tests for your slice first, from its `acceptance` list in the spec.
  Name them after the criterion: `TestS4_TwentyJoinsAtOnceFillExactlyEight`.
- Store tests run against the local Postgres from `infra/local/compose.yaml`, using `DATABASE_URL`
  from the environment. Use a transaction per test and roll it back.
- Playwright runs against the real binary built from `app/cmd/twofeet`. A test-only server is allowed
  only for what the real binary can't do, and the demo path always runs against the real binary.
- Playwright servers read `DATABASE_URL` and `PORT` from the environment too, and `app/e2e` depends
  only on `@playwright/test` and `@clerk/testing`. The e2e gate installs them; commit the `package-lock.json` it creates.
- Sign-in tests use Clerk's testing mode: `clerkSetup` and `setupClerkTestingToken` from `@clerk/testing`,
  with a test address like `name+clerk_test@example.com` and the fixed code `424242`. Never stub the sign-in
  in Playwright. The Clerk keys reach tests only through the gates: run `harness/gates.sh </dev/null` to
  check sign-in tests, and never read `.env`.
- New Playwright specs sign in once per file (`beforeAll`, reusing the browser context), not once per test.
  Signing in through Clerk's UI is most of the suite's run time.
- Playwright tests live in `app/e2e/`, run at 390x844 unless the test is about the Wall.
- `go test -race ./...` must pass. No `t.Skip` to get past a gate.

## Design

- Use the tokens in `design/tokens.css`. No new colors, fonts or radii without a design-critic note.
- Match the reference screenshots in `design/reference/`. The spec's `design` and `delight`
  sections are the rules the design critic checks.
- Flat, crisp and digital. No paper textures, tilt, tape or handwriting fonts.

## Done means

Gates green, acceptance tests for the slice written and passing, no TODOs left in the diff,
and the slice's screens match the references at 390px. Then stop and let review happen.
