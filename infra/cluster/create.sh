#!/usr/bin/env bash
# Create the Two Feet DOKS cluster and connect the container registry. Run once, before Monday.
#
#   infra/cluster/create.sh
#
# Small and non-HA on purpose: one app pod, the Dash0 operator, Radar and cloudflared fit on two
# s-2vcpu-4gb nodes. Delete it after the event with:
#   doctl kubernetes cluster delete two-feet-sfo3 --dangerous
set -euo pipefail

NAME=${NAME:-two-feet-sfo3}
REGION=${REGION:-sfo3}
SIZE=${SIZE:-s-2vcpu-4gb}
NODES=${NODES:-2}
REGISTRY=${REGISTRY:-highclip}   # existing DOCR registry name; check with: doctl registry get

command -v doctl >/dev/null || { echo "install doctl and run: doctl auth init --context highclip" >&2; exit 1; }

if doctl kubernetes cluster get "$NAME" >/dev/null 2>&1; then
  echo "cluster $NAME already exists"
else
  doctl kubernetes cluster create "$NAME" \
    --region "$REGION" \
    --ha=false \
    --node-pool "name=pool-$NAME;size=$SIZE;count=$NODES" \
    --wait
fi

# Lets every namespace pull from DOCR without a hand-made pull secret.
doctl kubernetes cluster registry add "$NAME"

# Adds the context do-$REGION-$NAME to your kubeconfig.
doctl kubernetes cluster kubeconfig save "$NAME"

cat <<EOF

Cluster ready. Next:
  1. Put KUBE_CONTEXT=do-$REGION-$NAME in infra/layers/secrets.env
  2. infra/scripts/bootstrap-secrets.sh
  3. infra/layers/deploy.sh all
EOF
