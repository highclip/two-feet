#!/usr/bin/env bash
# Deploy one layer, or all of them in order, to the cluster in secrets.env.
#
#   infra/layers/deploy.sh <00-operators|40-platform|50-app|all>
#   IMAGE_TAG=3f02d9a infra/layers/deploy.sh 50-app      # app needs an image tag
#
# Same shape as honk-k8s: each layer is a skaffold.yaml of Helm releases, layers run in order,
# and each is health-checked before the next. Re-running is safe (helm upgrade --install).
# CI deploys 50-app on every push to main; use this for operators, platform and manual deploys.
set -euo pipefail

cd "$(dirname "$0")"
LAYERS=(00-operators 40-platform 50-app)

usage() { echo "usage: $0 <${LAYERS[*]// /|}|all>"; exit 1; }
[[ $# -eq 1 ]] || usage

[[ -f secrets.env ]] || { echo "missing infra/layers/secrets.env (copy secrets.env.example)" >&2; exit 1; }
set -a; source secrets.env; set +a
: "${KUBE_CONTEXT:?set KUBE_CONTEXT in secrets.env}"

# Values skaffold templates into releases (setValueTemplates), read from cluster.yaml.
eval "$(python3 - <<'EOF'
import shlex, yaml
c = yaml.safe_load(open("cluster.yaml"))
for k, v in {
    "CLUSTER_NAME": c["cluster"]["name"],
    "DASH0_ENDPOINT": c["dash0"]["endpoint"],
    "DASH0_API_ENDPOINT": c["dash0"]["apiEndpoint"],
}.items():
    print(f"export {k}={shlex.quote(str(v))}")
EOF
)"

kc() { kubectl --context "$KUBE_CONTEXT" "$@"; }
ok()   { echo "  ok   $*"; }
fail() { echo "  FAIL $*" >&2; exit 1; }
ready() { kc -n "$1" rollout status "$2" --timeout=5m >/dev/null && ok "$1 $2" || fail "$1 $2 not ready"; }
all_ready() { kc -n "$1" wait --for=condition=Available deploy --all --timeout=5m >/dev/null && ok "$1 deployments" || fail "$1 deployments not ready"; }

check() {
  case $1 in
    00-operators)
      kc get crd dash0monitorings.operator.dash0.com >/dev/null && ok "crd dash0monitorings" || fail "Dash0 CRD missing"
      all_ready dash0-system
      all_ready radar
      ;;
    40-platform)
      ready cloudflare deploy/cloudflared
      ;;
    50-app)
      ready two-feet deploy/two-feet
      host=$(awk '/^publicHost:/{print $2; exit}' cluster.yaml)
      curl -fsS --max-time 10 "https://$host/healthz" >/dev/null && ok "https://$host/healthz" \
        || fail "https://$host/healthz unreachable (tunnel route or DNS?)"
      ;;
  esac
}

deploy_layer() {
  local layer=$1
  echo "==> $layer  (context: $KUBE_CONTEXT)"
  if [[ $layer == 50-app ]]; then
    : "${IMAGE_TAG:?set IMAGE_TAG to an image tag that exists in the registry}"
    export IMAGE_TAG
  fi
  (cd "$layer" && skaffold deploy --kube-context "$KUBE_CONTEXT")
  check "$layer"
}

case $1 in
  all) for l in "${LAYERS[@]}"; do
         if [[ $l == 50-app && -z ${IMAGE_TAG:-} ]]; then echo "==> 50-app skipped (no IMAGE_TAG; CI deploys it)"; continue; fi
         deploy_layer "$l"
       done ;;
  *)   [[ " ${LAYERS[*]} " == *" $1 "* ]] || usage; deploy_layer "$1" ;;
esac
