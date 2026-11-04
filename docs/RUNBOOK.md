# Runbook

Event: Monday, November 9, 2026. Check-in 9:00, submissions close 17:30 MT. The Shop Workspace, SLC.

## Before Monday

### Accounts and keys (Thursday)

- [ ] **Rules.** Read the hackathon rules on wemakedevs.org. Confirm a prepared harness, spec and
      infrastructure are allowed, and whether app code written before 9:00 is. This repo assumes it isn't.
- [ ] **DigitalOcean:** `doctl auth init --context highclip`, and know the DOCR registry name.
- [ ] **Cloudflare:** pick the hostname (`publicHost` in `infra/layers/cluster.yaml`), then
      `cloudflared tunnel create two-feet` and `cloudflared tunnel route dns two-feet <hostname>`.
      Put the tunnel id in `cluster.yaml`.
- [ ] **PlanetScale Postgres:** a database and a read/write role. Copy the connection string with
      `sslmode=verify-full`.
- [ ] **Elastic Cloud:** a deployment (8.17+), an API key with index and inference rights, and
      the ELSER inference endpoint deployed so `semantic_text` works.
- [ ] **Clerk:** an application with email-code sign-in only. Add the public hostname to its
      allowed origins. Development keys are fine for the rehearsal.
- [ ] **Dash0:** an auth token and your region's ingress and API endpoints (into `cluster.yaml`).
- [ ] **Sonar / Gitar:** install the Gitar GitHub App on `highclip/two-feet` (the page asks for
      the account before arrival).
- [ ] **GitHub:** repo secret `DIGITALOCEAN_ACCESS_TOKEN`; the default branch is `main`.

### Cluster (Friday)

```bash
infra/cluster/create.sh                               # DOKS two-feet-sfo3, registry connected
cp infra/layers/secrets.env.example infra/layers/secrets.env   # fill in
infra/scripts/bootstrap-secrets.sh
infra/layers/deploy.sh 00-operators                   # Dash0 operator, Radar
infra/layers/deploy.sh 40-platform                    # Cloudflare tunnel
```

Pin the Dash0 and Radar chart versions in `00-operators/skaffold.yaml` once they install cleanly.

### Rehearsal with the smoke service (Saturday)

1. `cd infra/smoke && go mod tidy`, commit `go.sum`, push to main. CI builds and deploys it.
2. Open the public hostname on your phone, on cellular and on a café's wifi:
   - the counter ticks every second and resumes within 2 s after backgrounding the tab
   - Postgres and Elasticsearch show `ok` from the cluster
   - Clerk sign-in works and "server sees" shows your user id
3. In Dash0, find traces for service `two-feet`, and the cluster's metrics from the operator.
4. Open Radar: `kubectl -n radar port-forward svc/radar <port>` (check the port with
   `kubectl -n radar get svc`) and look at the two-feet namespace.

### Harness

A full dry run (three slices, real headless agents) ran on 2026-11-04 and cost $2.33 on the default
model. It drove the fixes in `git log` titled "Dry run findings". What's left to check on your Mac:

- [ ] `scripts/setup-tools.sh` succeeds; every gate command runs (gosec, gitleaks, govulncheck and
      Playwright weren't available in the dry run's sandbox).
- [ ] Run `claude` once in `~/csync/two-feet` and accept the trust prompt. Worktrees inherit that
      trust. `scripts/agent.sh` passes the allow list on the command line, so it works without it too.
- [ ] One builder in a tmux window (`scripts/spawn-builder.sh S1` inside tmux) to see the interactive
      flow; the dry run only used `HEADLESS=1`.
- [ ] The tester agent against a real deploy. The dry run had no deploy, so the demo-path light was
      never exercised.

### How the loop runs (what the dry run settled)

| Step | Command |
|---|---|
| Plan | `HEADLESS=1 scripts/agent.sh planner "Plan the run from spec/spec.yaml."` (or the planner subagent) |
| Build | `scripts/spawn-builder.sh S1` (tmux window) or `HEADLESS=1 scripts/spawn-builder.sh S1` |
| Gates | automatic on every builder stop; `harness/gates.sh S1 </dev/null` by hand |
| Review | `scripts/review.sh S1 --design` (runs inside the slice's worktree) |
| Fix findings | `scripts/spawn-builder.sh S1 --fix` |
| Merge | the integrator subagent, or `HEADLESS=1 scripts/agent.sh integrator "Merge slice S1."` |
| Look | `hx serve`; `hx findings S1`; `hx decisions` |

Budget: the dry run's three small slices cost about $0.75 each end to end (build, review, merge).
Two Feet's slices are bigger; plan on $3-5 a slice, so roughly $40-60 for the day.

### Night before

- [ ] Laptop, charger, phone charged. Print QR signs for a few spots and badge stickers if allowed.
- [ ] Budget for agent spend decided (the board's burn panel uses it).
- [ ] `git pull`, `docker compose -f infra/local/compose.yaml up -d` works on the laptop.

## Event day

| Time | What |
|---|---|
| 9:00 | Check in. Confirm the venue wifi reaches your hostname. Walk the room: name the spots, update `venue_seed` in the spec. |
| 9:20 | `hx run`, then the **kickoff** skill. Planner lays out slices; builders start on S1, S3, S5, S7. |
| 10:30 | **Thin slice live:** sign-in, one spot page, one space, deployed. The demo light should go green. |
| 12:00 | Lunch: put the event QR on screen, invite the room to post and start spaces. Real data for the demo. |
| 12:30–16:30 | Fan out. You answer the inbox, watch stuck slices, cut from `cut_order` if behind. |
| 16:30 | **Feature freeze.** Finish what's in review. Demo seed, demo path, design polish only. |
| 17:15 | Record a backup demo video from the deployed app. Submit by 17:30. |
| After | Presentations at 18:30. |

## If something breaks

| Symptom | First thing to check |
|---|---|
| Hostname shows a Cloudflare error | `kubectl -n cloudflare logs deploy/cloudflared`; is the app pod Ready? |
| Live updates stop on phones | Smoke service still ticking? If yes, the app's hub; if no, the tunnel or wifi |
| CI deploy fails at Helm | `helm -n two-feet history two-feet`; a stuck `pending-upgrade` needs `helm rollback` |
| Migrate job fails | `kubectl -n two-feet logs job/two-feet-migrate`; fix forward with a new migration |
| Elastic matching slow | Check the inference endpoint is deployed and warm in Elastic Cloud |
| Agents loop on a gate | The board shows it stuck; read the last gate detail and answer the escalation |
