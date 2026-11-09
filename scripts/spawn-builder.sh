#!/usr/bin/env bash
# Start a builder session for one slice in its own worktree and tmux window.
#
#   scripts/spawn-builder.sh S3            # branch slice/S3-<slug from the spec title>
#   NO_TMUX=1 scripts/spawn-builder.sh S3  # just create the worktree and print the command
#   HEADLESS=1 scripts/spawn-builder.sh S3 # run the builder headless in the background; log in .harness/logs/
#   scripts/spawn-builder.sh S3 --fix      # send the slice back to its builder to fix review findings
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

# The venv lives in the main checkout, which this finds even when run from a worktree.
venv_python="$(dirname "$(git rev-parse --path-format=absolute --git-common-dir)")/.venv/bin/python"
[[ -x $venv_python ]] || { echo "missing $venv_python: run scripts/setup-tools.sh" >&2; exit 1; }

title=$("$venv_python" - "$slice" <<'EOF'
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
  # .env isn't in git. Each worktree gets its own copy pointing at its own database, index and port.
  if [[ -f .env ]]; then
    {
      grep -v -E '^(DATABASE_URL|ELASTIC_INDEX_PREFIX|PORT|PUBLIC_URL)=' .env || true
      (cd "$wt" && source scripts/slice-env.sh && printf 'DATABASE_URL=%s\nELASTIC_INDEX_PREFIX=%s\nPORT=%s\nPUBLIC_URL=%s\n' \
        "$DATABASE_URL" "$ELASTIC_INDEX_PREFIX" "$PORT" "$PUBLIC_URL")
    } > "$wt/.env"
    chmod 600 "$wt/.env"
  fi
  # Builders can run Playwright themselves only if its package is already there.
  if [[ -f $wt/app/e2e/package-lock.json ]]; then
    (cd "$wt/app/e2e" && npm ci --ignore-scripts --silent --no-fund --no-audit)
  fi
fi
scripts/slice-db.sh create "$slice"

# Two builders in one worktree would overwrite each other's work.
for pid in $(pgrep -f '[c]laude -p --agent builder' || true); do
  if [[ $(lsof -a -p "$pid" -d cwd -Fn 2>/dev/null | sed -n 's/^n//p') == "$wt" ]]; then
    echo "a builder is already running in $wt (pid $pid)"; exit 0
  fi
done

agent="builder-$slice"
prompt="Build slice $slice ($title) from spec/spec.yaml. Follow CLAUDE.md and the builder instructions."
if [[ ${2:-} == --fix ]]; then
  # Record it here rather than trusting the agent to: moving to building clears the old verdicts,
  # which no longer describe the code (the fix builder in the dry run never did it). That also
  # empties `hx findings`, so they have to be read first and handed over in the prompt.
  findings=$(python3 harness/hx.py findings "$slice")
  prompt="Slice $slice ($title) came back from review. These are the findings. They are cleared from the log once the slice is back in building, so hx findings won't show them:

$findings

Fix every finding in your worktree, commit, and stop so the gates and reviewers run again."
  python3 harness/hx.py --agent "builder-$slice" state "$slice" building --reason "back from review"
fi
# Findings can hold quotes, backticks and dollar signs, and cmd is re-read by a shell (tmux's, or
# whichever one it's pasted into). Single quotes with '\'' work in both bash and fish.
squote="'\\''"
quoted_prompt=${prompt//\'/$squote}
cmd="cd '$wt' && HX_AGENT=$agent HX_SLICE=$slice scripts/agent.sh builder '$quoted_prompt'"

echo "worktree: $wt"
echo "branch:   $branch"

if [[ -n ${HEADLESS:-} ]]; then
  # Redirect the whole subshell, not just the command: otherwise the subshell keeps the caller's
  # stdout open and `spawn-builder.sh S2 | ...` waits for the builder to finish (found in the dry run).
  (cd "$wt" && HX_AGENT=$agent HX_SLICE=$slice HEADLESS=1 exec nohup scripts/agent.sh builder "$prompt") \
    </dev/null >/dev/null 2>&1 &
  disown 2>/dev/null || true
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
