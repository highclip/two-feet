#!/usr/bin/env bash
# Create or drop one slice's local Postgres database.
#
#   scripts/slice-db.sh create S3    # safe to re-run; starts Postgres if it's down
#   scripts/slice-db.sh drop S3      # the integrator runs this after merging
set -euo pipefail

action=${1:?usage: slice-db.sh create|drop S<n>}
slice=${2:?usage: slice-db.sh create|drop S<n>}
[[ $slice =~ ^S[0-9]+$ ]] || { echo "slice must look like S3" >&2; exit 1; }
db="twofeet_s${slice#S}"
compose=(docker compose -f "$(git rev-parse --show-toplevel)/infra/local/compose.yaml")
psql() { "${compose[@]}" exec -T postgres psql -U twofeet -d twofeet -v ON_ERROR_STOP=1 "$@"; }

case $action in
  create)
    "${compose[@]}" up -d --wait postgres >/dev/null 2>&1
    [[ -n $(psql -tAc "SELECT 1 FROM pg_database WHERE datname = '$db'") ]] || psql -qc "CREATE DATABASE $db"
    ;;
  drop)
    psql -qc "DROP DATABASE IF EXISTS $db WITH (FORCE)"
    ;;
  *)
    echo "usage: slice-db.sh create|drop S<n>" >&2
    exit 1
    ;;
esac
