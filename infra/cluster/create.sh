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
# The registry isn't named here: a team has one (honk-app, on the Professional plan), and
# `registry add` below connects whichever it is. Images go to image.repository in cluster.yaml.
CONTEXT=${CONTEXT:-highclip}     # doctl auth init --context highclip, with a Highclip team token
PROJECT=${PROJECT:-cn-hackathon} # DigitalOcean project the cluster belongs to

command -v doctl >/dev/null || { echo "install doctl and run: doctl auth init --context $CONTEXT" >&2; exit 1; }
doctl() { command doctl --context "$CONTEXT" "$@"; }

# Check access before creating anything: a token without project scope can create the cluster but
# not file it under the project.
project_id=$(doctl projects list --format ID,Name --no-header | awk -v p="$PROJECT" '$2 == p {print $1}')
[[ -n $project_id ]] || { echo "no project named $PROJECT visible to doctl context $CONTEXT" >&2; exit 1; }

if doctl kubernetes cluster get "$NAME" >/dev/null 2>&1; then
  echo "cluster $NAME already exists"
else
  doctl kubernetes cluster create "$NAME" \
    --region "$REGION" \
    --ha=false \
    --node-pool "name=pool-$NAME;size=$SIZE;count=$NODES" \
    --wait
fi

# `cluster create` has no project flag, so move it into the project afterwards. Safe to re-run.
cluster_id=$(doctl kubernetes cluster get "$NAME" --format ID --no-header)
doctl projects resources assign "$project_id" --resource "do:kubernetes:$cluster_id" >/dev/null
echo "cluster $NAME is in project $PROJECT"

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
