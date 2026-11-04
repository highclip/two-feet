---
name: kickoff
description: Start or run the hackathon build as the orchestrator. Use at 9:00 on event day, or when asked to "kick off", "start the run" or "keep the build moving".
---

# Kickoff and orchestration

You are the orchestrator, running in the main checkout on `main`. You never build slices yourself.
Builders run as their own Claude Code sessions, one per worktree; reviewers and the planner run as
your subagents.

## 1. Start the run (once)

```bash
python3 harness/hx.py --agent human run --name "Two Feet" --hours 8.5 --budget <USD from the human>
python3 harness/hx.py serve &        # the board at http://127.0.0.1:4777, if it isn't running
```

## 2. Plan

Use the **planner** subagent: "Plan the run from spec/spec.yaml." It records every slice and
reports which ones are ready.

## 3. Start builders

For each ready slice, up to 4 at a time, run:

```bash
scripts/spawn-builder.sh S1
```

That creates `../wt-S1` on `slice/S1-<name>`, and opens a tmux window running a builder session
for that slice. Prefer slices on the demo path (`spec.demo_path`) and anything others depend on.

## 4. Loop until the freeze

Every few minutes, or when the human asks:

1. `python3 harness/hx.py status`
2. For each slice in **review** without verdicts: run the **sec-review** subagent on it, and the
   **design-critic** subagent too if it draws UI. Run them in parallel; they never see each other's work.
3. For each slice with all approvals: run the **integrator** subagent on it, then the **tester**.
4. For each slice that's **stuck** (the board marks these): read its last gate failure and tell the
   human in one line what's wrong and what you suggest.
5. When builders free up, ask the planner what's unblocked and spawn the next builders.

## 5. Feature freeze (one hour before the deadline)

Stop spawning builders. Ask the planner which slices to cut. Finish what's in review, then spend
the remaining time on the demo path, the demo seed and polish findings from the design critic.

## Rules

- The human answers questions in the board's inbox. Don't answer `hx ask` questions on their behalf.
- Never merge anything yourself; the integrator does.
- If a gate is flaky rather than failing, say so to the human instead of retrying it silently.
