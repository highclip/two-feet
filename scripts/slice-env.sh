# Source this: gives a slice's worktree its own database, Elasticsearch index prefix and port, so
# parallel builders can't break each other's tests (S2 rolled back a migration on S3's database in
# the CLI dry run). Does nothing outside a slice/S<n>-* branch, such as the integrator on main.
__slice=$(git branch --show-current 2>/dev/null | sed -nE 's#^slice/S([0-9]+).*#\1#p' || true)
if [ -n "$__slice" ]; then
  export DATABASE_URL="postgres://twofeet:twofeet@localhost:5432/twofeet_s$__slice?sslmode=disable"
  export ELASTIC_INDEX_PREFIX="twofeet_s$__slice"
  export PORT=$((8100 + __slice))
  export PUBLIC_URL="http://localhost:$PORT"
fi
unset __slice
