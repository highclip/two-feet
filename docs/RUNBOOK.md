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

### Harness dry run (Sunday)

Run the whole loop on a throwaway spec in a scratch branch, with one or two tiny slices:

- [ ] `scripts/setup-tools.sh` succeeds; every gate command runs.
- [ ] `scripts/spawn-builder.sh S1` opens a builder in its own worktree. **Check that
      `claude --agent builder "<prompt>"` starts a session as the builder agent in your Claude Code
      version**; if not, start `claude` in the worktree and paste the prompt from the script.
- [ ] A deliberately failing test makes the Stop hook block "done" and the builder fixes it.
- [ ] `hx ask` shows up on the board and your click unblocks the builder.
- [ ] The sec-review and design-critic subagents record verdicts; the integrator merges and pushes;
      CI deploys; the tester records a demo check.
- [ ] Delete the scratch branch and reset the log: `rm -rf .harness`.

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
