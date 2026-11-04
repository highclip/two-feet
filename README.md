# Two Feet

Open spaces used to happen once a day, on a wall of sticky notes. Two Feet makes them happen all
day, from what people are stuck on right now. Post a problem in one line and it points you at the
people, the hallway conversations and the sponsor booths that can help, close to where you're standing.

Built at the Cloud Native Hackathon with Kelsey Hightower, Salt Lake City, November 9, 2026.

> **Before 9:00 on the 9th, `app/` is empty on purpose.** Everything else here is preparation:
> the build harness, the spec, the design and the infrastructure. The app itself gets built on the day.

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
app/                  Empty until the event starts
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

## Local quick start

```bash
scripts/setup-tools.sh                 # Go tools, gitleaks, Playwright
cp infra/local/.env.example .env       # fill in Clerk keys; the rest works locally
docker compose -f infra/local/compose.yaml up -d
alias hx="python3 harness/hx.py"
hx serve                               # the board
```
