#!/usr/bin/env bash
# Run every gate for the current slice, record each result, and refuse "done" while any gate fails.
#
# As a Claude Code Stop hook: exit 2 sends the failure output back to the agent,
# so it keeps fixing instead of finishing. After HX_MAX_ATTEMPTS straight failures (default 5)
# the slice is marked blocked and escalated to your decisions inbox, which also stops the loop.
#
# This hook deliberately does NOT exit early on `stop_hook_active`. That flag is true on every
# retry after the first block, and honoring it would end the fix-until-green loop after one try.
# The attempt counter below is the loop guard instead. Claude Code also overrides a Stop hook that
# blocks 8 times in a row with no tool call in between; a builder fixing code always makes tool calls.
#
# The slice comes from $HX_SLICE or a branch named slice/S<n>-anything.
# Gate commands come from <repo>/harness/gates.conf (bash; set GATES=(...) and CMD[name]=...).
set -uo pipefail

[ -t 0 ] || cat >/dev/null   # drain hook input from stdin

HX=(python3 "$(cd "$(dirname "$0")" && pwd)/hx.py")
SLICE="${HX_SLICE:-$(git branch --show-current 2>/dev/null | sed -nE 's#^slice/(S[0-9]+).*#\1#p')}"
[ -z "$SLICE" ] && exit 0    # not on a slice branch: nothing to gate

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
CONF="$(git rev-parse --show-toplevel 2>/dev/null)/harness/gates.conf"
# shellcheck disable=SC1090
[ -f "$CONF" ] && source "$CONF"

"${HX[@]}" --agent gates state "$SLICE" gates

failed=()
report=""
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
  exit 0
fi

n=$(( $(cat "$counter" 2>/dev/null || echo 0) + 1 ))
echo "$n" > "$counter"

if [ "$n" -ge "${HX_MAX_ATTEMPTS:-5}" ]; then
  rm -f "$counter"
  id="$("${HX[@]}" --agent gates ask --slice "$SLICE" --no-wait \
        "$SLICE has failed gates $n times in a row (${failed[*]}). What should happen?" \
        --option "Keep trying" --option "Cut this slice" --option "I'll take it over")"
  echo "Gates failed $n times in a row. Escalated as $id. Stop work on $SLICE and wait: hx wait $id" >&2
  exit 0
fi

"${HX[@]}" --agent gates state "$SLICE" building --reason "${failed[*]} failed"
echo "Gates failed for $SLICE (attempt $n): ${failed[*]}. Fix these before you finish.$report" >&2
exit 2
