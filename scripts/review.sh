#!/usr/bin/env bash
# Review one slice: run sec-review (and design-critic with --design) headless inside the slice's
# worktree, in parallel, and wait for their verdicts.
#
#   scripts/review.sh S2            # security only
#   scripts/review.sh S2 --design   # security and design
#
# Reviewers run in the worktree because an agent started in the main checkout can't read or run
# code in a worktree outside it (found in the dry run). There they see the diff as
# `git diff main...HEAD` and can start the app on their own port.
set -euo pipefail

slice=${1:?usage: review.sh S<n> [--design]}
design=${2:-}
root=$(git rev-parse --show-toplevel)
cd "$root"

wt=$(git worktree list --porcelain | awk -v s="refs/heads/slice/$slice-" '
  /^worktree /{w=substr($0,10)} /^branch /{if (index(substr($0,8), s)==1) print w}' | head -n1)
[[ -n $wt ]] || { echo "no worktree for $slice (scripts/spawn-builder.sh creates one)" >&2; exit 1; }

port=$((8100 + ${slice#S}))
pids=()

(cd "$wt" && HX_AGENT=sec-review HEADLESS=1 scripts/agent.sh sec-review \
  "Review slice $slice. You are in its worktree; the change to review is git diff main...HEAD.") \
  > "/tmp/review-$slice-security.out" 2>&1 &
pids+=($!)

if [[ $design == --design ]]; then
  (cd "$wt" && HX_AGENT=design-critic HEADLESS=1 scripts/agent.sh design-critic \
    "Review slice $slice. You are in its worktree. Start the app from app/ on port $port (ADDR=:$port or PORT=$port, whichever the app reads) and stop it when you're done.") \
    > "/tmp/review-$slice-design.out" 2>&1 &
  pids+=($!)
fi

echo "reviewing $slice in $wt ..."
status=0
for p in "${pids[@]}"; do wait "$p" || status=1; done

python3 harness/hx.py status --json | python3 -c '
import json, sys
s = json.load(sys.stdin)["slices"].get(sys.argv[1], {})
print(f"{sys.argv[1]}: {s.get(\"state\")}" + (f"  last finding: {s[\"last_fail\"]}" if s.get("last_fail") else ""))
' "$slice"
exit $status
