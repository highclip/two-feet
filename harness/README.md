# Harness

An append-only event log plus a live board for running a hackathon with agents.
Agents and hooks write the log. The board, and `hx status`, are projections of it.

```
harness/
  hx.py                 CLI: write events, ask the human, print status, serve the board (stdlib only)
  gates.sh              Runs every gate for a slice; blocks "done" while any fail (the Stop hook)
  gates.conf            The gate commands for the Go app in app/
  board.html            The live board (works offline with a sample run)
  events.schema.json    One JSON Schema for every event type
```

The hook is wired in `.claude/settings.json`.

## Quick start

```bash
alias hx="python3 harness/hx.py"

hx run --name "Two Feet" --hours 8.5 --budget 150
hx spec             # copy the slices into the log so the board can show them
hx plan S1 "Foundations and sign-in" --spec spec/spec.yaml#/slices/S1
hx serve            # board at http://127.0.0.1:4777
```

On event day the `kickoff` skill does this for you.

## Conventions the board relies on

- **One slice, one branch, one worktree.** `scripts/spawn-builder.sh S3` creates `../worktrees/two-feet-S3` on `slice/S3-<name>` and starts a builder session there with `HX_SLICE=S3`.
- **One shared log.** `hx` resolves the log through git's common dir, so every worktree writes to the same `<repo>/.harness/events.jsonl`. Override with `HX_LOG`.
- **Identify the writer.** Set `HX_AGENT=builder-2` in each builder's session, or pass `--agent`.
- **Only hooks mark gates.** Builders move slices to `building`. `gates.sh` moves them to `gates`, then `review` or back to `building`. An agent can't skip ahead.
- **Escalate, don't guess.** When the spec is unclear, an agent runs:

  ```bash
  hx ask --slice S3 "Can a guest claim more than one dish?" \
    --option "One dish per guest" --option "Unlimited" --option "Host sets a limit"
  ```

  This marks S3 blocked, shows the question in **Waiting on you**, and prints your answer when you click it. It waits up to 9 minutes so it stays under a tool timeout. If it times out, run `hx wait D1`. Use `--fyi` for questions that shouldn't block.

- **Record choices, not just questions.** A builder puts `Decision: <what>. Why: <why>` on its own line in a commit message, and `gates.sh` copies it to the log on every run. Other agents use `hx note`. These don't override the spec, and `hx decisions` leaves them out unless you pass `--choices`, because reviewers read that command and are meant not to see builders' reasoning.
- **Parallel slices see each other through git.** `hx peers S3` lists what other active slices have committed and chosen. The gates fail until a slice has merged `main` (`uptodate`), and send a builder back once per new set of files it shares with another active slice.
- **One database per worktree.** `scripts/slice-env.sh` gives a slice branch its own `DATABASE_URL`, `ELASTIC_INDEX_PREFIX` and `PORT`. `spawn-builder.sh` creates the database and the integrator drops it.
- **Bounded retries.** After 5 straight gate failures (`HX_MAX_ATTEMPTS`), `gates.sh` blocks the slice and asks you what to do. That also ends the Stop-hook loop. The hook intentionally ignores `stop_hook_active`; see the comment in `gates.sh`.

## Event types

| type | written by | key fields |
|---|---|---|
| `run.start` | you | `name`, `deadline`, `budget_usd` |
| `slice.planned` | planner | `slice`, `title`, `spec` |
| `slice.state` | builders, gates, integrator | `slice`, `to` (planned · building · gates · review · blocked · merged), `reason`, `sha` |
| `gate.result` | gates.sh | `slice`, `gate`, `status`, `detail` |
| `review.verdict` | reviewers | `slice`, `reviewer` (security · design · code), `verdict` (approve · changes), `findings[]` |
| `decision.request` | any agent | `id`, `question`, `options[]`, `blocking` |
| `decision.resolved` | you (board or `hx decide`) | `id`, `choice` |
| `agent.usage` | orchestrator | `agent`, `cost_usd` (delta), `tokens_in`, `tokens_out` |
| `demo.check` | tester | `status`, `flow`, `detail` |
| `spec.loaded` | `hx spec` | `sha`, `path`, `slices[]` (id, title, depends_on, scope, acceptance) |
| `decision.made` | gates.sh from commits, or `hx note` | `slice`, `text`, `why`, `sha`, `source` |
| `agent.error` | scripts/agent.sh | `agent`, `slice`, `detail`; cleared on the board by that agent's next successful run |

Every event also carries `ts` (UTC), `run` and `agent`. See `events.schema.json`.

## Gates

`gates.sh` has TypeScript defaults; `gates.conf` overrides them for this repo's Go app:
gofmt, go vet, golangci-lint, `go test -race`, gosec, gitleaks, govulncheck and Playwright.
Install the tools with `scripts/setup-tools.sh`.

## Notes

- The board server binds to 127.0.0.1. It accepts one kind of write: answering an open decision with one of its listed options.
- `hx status` prints the same board in a terminal, and `hx status --json` gives your orchestrator agent the whole picture in one call.
- The log doubles as a replay. Open any `events.jsonl` on the board and scrub through the run.
