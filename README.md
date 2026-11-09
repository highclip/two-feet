# Two Feet

Open spaces used to happen once a day, on a wall of sticky notes. Two Feet makes them happen all
day, from what people are stuck on right now. Post a problem in one line and it points you at the
people, the hallway conversations and the sponsor booths that can help, close to where you're standing.

Built at the Cloud Native Hackathon with Kelsey Hightower, Salt Lake City, November 9, 2026.

> **What was prepared and what was built today.** The spec, design tokens and references, the agent
> harness and the infrastructure were prepared before the event (commits from November 4 to 8). All
> application code in `app/` was written during the hackathon on November 9, starting at 10:37 MT.

## Try it

Live at https://twofeet.highclip.dev/e/cnh. Scan a table's QR code or open that link, sign in with an
email code, and post what you're stuck on. The Wall, for a projector, is at `/e/cnh/wall`.

## What's in here

```
spec/spec.yaml        The contract. Slices, acceptance criteria, permissions, demo path, cut order.
CLAUDE.md             Conventions every agent reads first.
.claude/agents/       planner, builder, tester, sec-review, design-critic, integrator
.claude/skills/       kickoff, add-migration, add-live-topic
harness/              hx (event log + board), gates.sh (the gates hook), gates.conf
design/               tokens.css and reference screenshots of the mockups
infra/local/          docker compose: Postgres, Elasticsearch, OTel collector
infra/cluster/        DOKS cluster creation
infra/layers/         Numbered skaffold/Helm layers: operators, platform, app
infra/smoke/          Throwaway service that proves the whole pipeline before Monday
scripts/              agent.sh (run any agent), spawn-builder.sh, review.sh, shoot.sh (screenshots), setup-tools.sh
docs/RUNBOOK.md       Before-Monday checklist, how the loop runs, and the day-of timeline
app/                  The application, built on November 9
```

## Stack

Go, `net/http`, templ + htmx with server-sent events. Postgres on PlanetScale. Elasticsearch on
Elastic Cloud for matching. Clerk passwordless sign-in, entered by scanning a QR code. OpenTelemetry
to Dash0. One container on a DigitalOcean Kubernetes cluster, reached through a Cloudflare tunnel.
Radar watches the cluster.

## How the build runs

1. `hx run` starts the clock. The **planner** turns `spec/spec.yaml` into slices on the board.
2. Each **builder** takes one slice in its own git worktree on a `slice/S<n>-*` branch.
3. When a builder tries to finish, `harness/gates.sh` runs fmt, vet, lint, tests, gosec,
   gitleaks, govulncheck and Playwright. "Done" is refused until they all pass.
4. **sec-review** and **design-critic** review the diff cold, without the builder's reasoning.
5. The **integrator** merges to `main`. CI builds the image and deploys it.
6. You watch `hx serve` at http://127.0.0.1:4777 and answer the questions agents escalate.

Details: [`harness/README.md`](harness/README.md) · [`docs/RUNBOOK.md`](docs/RUNBOOK.md)

## Run it locally

You need Go 1.27, Node 20+, Docker, and a Clerk development application with email-code sign-in.

```bash
scripts/setup-tools.sh                          # Go tools, gitleaks, Playwright
cp infra/local/.env.example .env                # add your Clerk test keys; the rest works locally
docker compose -f infra/local/compose.yaml up -d
cd app
set -a; source ../.env; set +a
go run ./cmd/twofeet migrate                    # schema and the venue
go run ./cmd/twofeet seed-demo                  # optional sample people, posts and spaces
go run ./cmd/twofeet                            # http://localhost:8080/e/cnh
```

Tests: `go test -race ./...` in `app/` and `npx playwright test` in `app/e2e/`.
To change the venue, edit `app/internal/store/seed_data.json` and run `migrate` again.

## AI use

This project was built with AI coding agents, and we want to be plain about how.

- **Claude Code wrote the application code.** Separate agents built each slice in its own git worktree;
  others reviewed security and design; another merged.
- **A person made the product and design decisions.** Agents had to escalate anything the spec didn't
  settle. The answers are in the harness's event log, including what was accepted, changed or cut.
- **Automated checks gated every merge:** gofmt, go vet, golangci-lint, `go test -race`, gosec, gitleaks,
  govulncheck and Playwright.
- **Gitar reviewed every feature pull request** and caught real bugs, including a forged-session issue
  that could have exhausted the Clerk rate limit, and resolved posts staying searchable after a lost
  delete.

## How the sponsors' tools are used

- **Elasticsearch** matches a posted problem to spaces, people and sponsors with hybrid semantic and
  tag search, and powers search and the Wall's topic strip. Postgres stays the system of record.
- **Dash0** receives OpenTelemetry traces and metrics, and its Agent0 helps diagnose demo failures.
- **Radar** watches the Kubernetes cluster and reports rollout health after each deploy.
- **Gitar (Sonar)** reviews every feature pull request.
