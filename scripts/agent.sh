#!/usr/bin/env bash
# Run one of the harness agents (.claude/agents/*.md) with this repo's allow list.
#
#   scripts/agent.sh <agent> "<prompt>"              interactive session
#   HEADLESS=1 scripts/agent.sh <agent> "<prompt>"   headless; output goes to .harness/logs/
#
# Why this exists: Claude Code ignores permissions.allow in .claude/settings.json until you've
# trusted the directory, trust doesn't carry over from a parent folder, and every builder worktree
# is a new directory. Passing the same rules with --allowedTools works without trust. Hooks (the
# gates) run either way.
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
  # --allowedTools takes a variable number of values, so it goes last.
  claude -p --agent "$agent" --permission-mode acceptEdits "$prompt" --allowedTools "${allow[@]}" \
    < /dev/null > "$log" 2>&1
  status=$?
  tail -n 25 "$log"
  exit $status
fi

exec claude --agent "$agent" "$prompt" --allowedTools "${allow[@]}"
