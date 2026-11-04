#!/usr/bin/env bash
# Start a builder session for one slice in its own worktree and tmux window.
#
#   scripts/spawn-builder.sh S3            # branch slice/S3-<slug from the spec title>
#   NO_TMUX=1 scripts/spawn-builder.sh S3  # just create the worktree and print the command
#   HEADLESS=1 scripts/spawn-builder.sh S3 # run the builder headless in the background; log in .harness/logs/
#
# Worktrees go in ../worktrees/two-feet-S3 (the ~/csync/worktrees convention); override the
# parent directory with WORKTREES_DIR.
#
# The builder runs as its own top-level Claude Code session, so the Stop hook (harness/gates.sh)
# gates that slice and nothing else. HX_SLICE tells the hook and hx which slice this is.
set -euo pipefail

slice=${1:?usage: spawn-builder.sh S<n>}
[[ $slice =~ ^S[0-9]+$ ]] || { echo "slice must look like S3" >&2; exit 1; }

root=$(git rev-parse --show-toplevel)
cd "$root"

title=$(python3 - "$slice" <<'EOF'
import sys, yaml
spec = yaml.safe_load(open("spec/spec.yaml"))
match = [s for s in spec["slices"] if s["id"] == sys.argv[1]]
print(match[0]["title"] if match else "")
EOF
)
[[ -n $title ]] || { echo "$slice is not in spec/spec.yaml" >&2; exit 1; }

slug=$(echo "$title" | tr '[:upper:]' '[:lower:]' | sed -E 's/[^a-z0-9]+/-/g; s/^-|-$//g' | cut -c1-32)
branch="slice/$slice-$slug"
wt="${WORKTREES_DIR:-$(cd "$root/.." && pwd)/worktrees}/two-feet-$slice"
mkdir -p "$(dirname "$wt")"

if [[ ! -d $wt ]]; then
  git fetch -q origin main 2>/dev/null || true
  git worktree add -q -b "$branch" "$wt" main
  # .env isn't in git; share the local one so tests can reach Postgres and Elasticsearch.
  [[ -f .env ]] && ln -sf "$root/.env" "$wt/.env"
fi

agent="builder-$slice"
prompt="Build slice $slice ($title) from spec/spec.yaml. Follow CLAUDE.md and the builder instructions."
cmd="cd '$wt' && HX_AGENT=$agent HX_SLICE=$slice scripts/agent.sh builder \"$prompt\""

echo "worktree: $wt"
echo "branch:   $branch"

if [[ -n ${HEADLESS:-} ]]; then
  (cd "$wt" && HX_AGENT=$agent HX_SLICE=$slice HEADLESS=1 nohup scripts/agent.sh builder "$prompt" >/dev/null 2>&1 &)
  echo "builder started headless; follow it in $(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")/.harness/logs/"
  exit 0
fi

if [[ -n ${NO_TMUX:-} ]] || ! command -v tmux >/dev/null || [[ -z ${TMUX:-} ]]; then
  echo
  echo "Run this in a new terminal:"
  echo "  $cmd"
  exit 0
fi

tmux new-window -n "$slice" "$cmd; echo; echo 'Builder exited. Press enter to close.'; read"
echo "started in tmux window $slice"
