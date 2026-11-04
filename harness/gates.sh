#!/usr/bin/env bash
# Run every gate for a slice, record each result, and refuse "done" while any gate fails.
#
# Two ways to run it:
#   As the Claude Code Stop hook (.claude/settings.json): on failure it prints
#     {"decision":"block","reason":"<the gate report>"} and exits 0, so the agent gets the report
#     and keeps fixing instead of finishing.
#   By hand or from the integrator: harness/gates.sh S3 </dev/null
#     prints the report and exits 1 on failure, 0 when every gate passes.
#
# Why JSON and not exit 2: Claude Code treats a Stop hook that exits 2 with "No such file or
# directory" anywhere in its stderr as a missing hook script, and ignores the block. Gate output
# contains that phrase whenever a test can't open a file. Found in the dry run on 2026-11-04.
#
# After HX_MAX_ATTEMPTS straight failures (default 5) the slice is marked blocked and escalated to
# the decisions inbox, and the agent is allowed to stop. That counter is the loop guard: the hook
# deliberately ignores `stop_hook_active`, which is true on every retry after the first block.
#
# The slice comes from the first argument, $HX_SLICE, or a branch named slice/S<n>-anything.
# Gate commands come from <repo>/harness/gates.conf (bash; set GATES=(...) and CMD[name]=...).
set -uo pipefail

# Hook mode when Claude Code sends the Stop event on stdin; manual mode otherwise.
input=""
[ -t 0 ] || input="$(cat)"
hook=0
case $input in *'"hook_event_name"'*) hook=1 ;; esac

HX=(python3 "$(cd "$(dirname "$0")" && pwd)/hx.py")
SLICE="${1:-${HX_SLICE:-$(git branch --show-current 2>/dev/null | sed -nE 's#^slice/(S[0-9]+).*#\1#p')}}"
[ -z "$SLICE" ] && exit 0    # not on a slice branch: nothing to gate

# The hook inherits the session's working directory, which moves when the agent runs `cd app`.
# Gate commands are written relative to the repo root, so always start there.
cd "$(git rev-parse --show-toplevel)" || exit 0

GATES=(types lint tests semgrep gitleaks deps e2e)
declare -A CMD=(
  [types]="npx tsc --noEmit"
  [lint]="npx eslint . --max-warnings=0"
  [tests]="npm test --silent"
  [semgrep]="semgrep scan --config auto --error --quiet"
  [gitleaks]="gitleaks detect --no-banner --redact"
  [deps]="npm audit --audit-level=high"
  [e2e]="npx playwright test --reporter=line"
)
CONF="$(pwd)/harness/gates.conf"
# shellcheck disable=SC1090
[ -f "$CONF" ] && source "$CONF"

"${HX[@]}" --agent gates state "$SLICE" gates

failed=()
report=""

# Built-in first gate: the gates check the working tree, but the integrator merges commits.
# Work that passes but isn't committed would merge as nothing (found in the dry run).
dirty="$(git status --porcelain --untracked-files=normal)"
if [ -n "$dirty" ]; then
  "${HX[@]}" --agent gates gate "$SLICE" committed fail --detail "uncommitted changes"
  failed+=(committed)
  report+=$'\n\n'"--- committed ---"$'\n'"These changes aren't committed. Commit them from the worktree root with plain git (no cd, no -C): git add -A && git commit -m \"$SLICE: ...\""$'\n'"$(printf '%s\n' "$dirty" | head -n 20)"
else
  "${HX[@]}" --agent gates gate "$SLICE" committed pass
fi

for g in "${GATES[@]}"; do
  out="$(bash -c "${CMD[$g]}" 2>&1)"; code=$?
  if [ "$code" -eq 0 ]; then
    "${HX[@]}" --agent gates gate "$SLICE" "$g" pass
  else
    detail="$(printf '%s\n' "$out" | grep -v '^[[:space:]]*$' | tail -n 1 | cut -c1-200)"
    "${HX[@]}" --agent gates gate "$SLICE" "$g" fail --detail "${detail:-exit $code}"
    failed+=("$g")
    report+=$'\n\n'"--- $g (exit $code) ---"$'\n'"$(printf '%s\n' "$out" | tail -n 40)"
  fi
done

attempts_dir="$(dirname "$("${HX[@]}" path)")/attempts"
mkdir -p "$attempts_dir"
counter="$attempts_dir/$SLICE"

if [ "${#failed[@]}" -eq 0 ]; then
  rm -f "$counter"
  "${HX[@]}" --agent gates state "$SLICE" review --reason "all gates passed"
  [ "$hook" -eq 1 ] || echo "All gates passed for $SLICE."
  exit 0
fi

block() { # message: send it back to the agent (hook) or print it and fail (manual)
  if [ "$hook" -eq 1 ]; then
    python3 -c 'import json, sys; print(json.dumps({"decision": "block", "reason": sys.argv[1]}))' "$1"
    exit 0
  fi
  printf '%s\n' "$1" >&2
  exit 1
}

if [ "$hook" -eq 0 ]; then
  # A manual run (the integrator, a human) reports without counting toward the builder's attempts.
  block "Gates failed for $SLICE: ${failed[*]}.$report"
fi

n=$(( $(cat "$counter" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$counter"

if [ "$n" -ge "${HX_MAX_ATTEMPTS:-5}" ]; then
  rm -f "$counter"
  id="$("${HX[@]}" --agent gates ask --slice "$SLICE" --no-wait \
        "$SLICE has failed gates $n times in a row (${failed[*]}). What should happen?" \
        --option "Keep trying" --option "Cut this slice" --option "I'll take it over")"
  # Let the agent stop; it waits for the human from here.
  echo "Gates failed $n times in a row. Escalated as $id. Stop work on $SLICE and wait: python3 harness/hx.py wait $id" >&2
  exit 0
fi

"${HX[@]}" --agent gates state "$SLICE" building --reason "${failed[*]} failed"
block "Gates failed for $SLICE (attempt $n of ${HX_MAX_ATTEMPTS:-5}): ${failed[*]}. Fix the cause, not the test, then stop again.$report"
