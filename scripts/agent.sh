#!/usr/bin/env bash
# Run one of the harness agents (.claude/agents/*.md) with this repo's allow list.
#
#   scripts/agent.sh <agent> "<prompt>"              interactive session
#   HEADLESS=1 scripts/agent.sh <agent> "<prompt>"   headless; output goes to .harness/logs/
#
# Why this exists: Claude Code ignores permissions.allow in .claude/settings.json until the project
# is trusted, and trust doesn't carry over from a parent folder. For a worktree, trust is keyed to the
# main checkout, so trusting ~/csync/two-feet once (run `claude` there and accept) covers the builders.
# Passing the rules with --allowedTools works either way, so the harness doesn't depend on it.
# Hooks (the gates) run with or without trust.
set -euo pipefail

agent=${1:?usage: agent.sh <agent> "<prompt>"}
prompt=${2:?usage: agent.sh <agent> "<prompt>"}

root=$(git rev-parse --show-toplevel)
common=$(git rev-parse --path-format=absolute --git-common-dir)
[[ -f $root/.claude/agents/$agent.md ]] || { echo "no agent named $agent in .claude/agents/" >&2; exit 1; }

mapfile -t allow < <(python3 -c '
import json, sys
for rule in json.load(open(sys.argv[1]))["permissions"]["allow"]:
    print(rule)
' "$root/.claude/settings.json")

if [[ -n ${HEADLESS:-} ]]; then
  logdir="$(dirname "$common")/.harness/logs"
  mkdir -p "$logdir"
  log="$logdir/${HX_AGENT:-$agent}-$(date +%H%M%S).log"
  echo "$agent running headless; log: $log"
  # --allowedTools takes a variable number of values, so it goes last. JSON output carries the run's
  # cost, which goes on the board's burn panel (it read $0 all through the dry run without this).
  set +e
  claude -p --agent "$agent" --permission-mode acceptEdits --output-format json "$prompt" \
    --allowedTools "${allow[@]}" < /dev/null > "$log.json" 2> "$log"
  status=$?
  set -e
  python3 - "$log.json" "$log" "${HX_AGENT:-$agent}" "$root/harness/hx.py" <<'PY' || true
import json, subprocess, sys
raw, log, who, hx = sys.argv[1:]
try:
    r = json.load(open(raw))
except Exception:
    r = {}
with open(log, "a") as f:
    f.write((r.get("result") or open(raw).read()) + "\n")
cost = r.get("total_cost_usd")
if cost:
    u = r.get("usage") or {}
    args = ["python3", hx, "--agent", who, "usage", "--cost", f"{cost:.4f}"]
    if u.get("input_tokens") is not None:
        args += ["--tokens-in", str(u["input_tokens"] + u.get("cache_read_input_tokens", 0) + u.get("cache_creation_input_tokens", 0))]
    if u.get("output_tokens") is not None:
        args += ["--tokens-out", str(u["output_tokens"])]
    subprocess.run(args, check=False)
PY
  rm -f "$log.json"
  tail -n 25 "$log"
  exit $status
fi

exec claude --agent "$agent" "$prompt" --allowedTools "${allow[@]}"
