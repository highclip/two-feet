#!/usr/bin/env bash
# Create the Kubernetes secrets every layer needs, from infra/layers/secrets.env. Idempotent:
# re-run it after changing a value, then restart the app (kubectl -n two-feet rollout restart deploy/two-feet).
#
#   infra/scripts/bootstrap-secrets.sh
set -euo pipefail

cd "$(dirname "$0")/../layers"
[[ -f secrets.env ]] || { echo "missing infra/layers/secrets.env (copy secrets.env.example)" >&2; exit 1; }
set -a; source secrets.env; set +a
: "${KUBE_CONTEXT:?set KUBE_CONTEXT in secrets.env}"

kc() { kubectl --context "$KUBE_CONTEXT" "$@"; }
ns() { kc get ns "$1" >/dev/null 2>&1 || kc create ns "$1" >/dev/null; }
apply_secret() { # namespace name args...
  local n=$1 name=$2; shift 2
  ns "$n"
  kc -n "$n" create secret generic "$name" "$@" --dry-run=client -o yaml | kc apply -f - >/dev/null
  echo "  ok   $n/$name"
}

missing=()
for v in DASH0_AUTH_TOKEN TUNNEL_CREDENTIALS_FILE DATABASE_URL ELASTIC_URL ELASTIC_API_KEY CLERK_PUBLISHABLE_KEY CLERK_SECRET_KEY; do
  [[ -n ${!v:-} ]] || missing+=("$v")
done
[[ ${#missing[@]} -eq 0 ]] || { echo "fill these in secrets.env first: ${missing[*]}" >&2; exit 1; }

creds=${TUNNEL_CREDENTIALS_FILE/#\~/$HOME}
[[ -f $creds ]] || { echo "tunnel credentials not found at $creds" >&2; exit 1; }

echo "==> secrets on $KUBE_CONTEXT"
apply_secret dash0-system dash0-authorization-secret --from-literal=token="$DASH0_AUTH_TOKEN"
apply_secret cloudflare tunnel-credentials --from-file=credentials.json="$creds"
apply_secret two-feet two-feet-env \
  --from-literal=DATABASE_URL="$DATABASE_URL" \
  --from-literal=ELASTIC_URL="$ELASTIC_URL" \
  --from-literal=ELASTIC_API_KEY="$ELASTIC_API_KEY" \
  --from-literal=CLERK_PUBLISHABLE_KEY="$CLERK_PUBLISHABLE_KEY" \
  --from-literal=CLERK_SECRET_KEY="$CLERK_SECRET_KEY" \
  --from-literal=DASH0_AUTH_TOKEN="$DASH0_AUTH_TOKEN"
