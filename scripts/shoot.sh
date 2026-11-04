#!/usr/bin/env bash
# Build the app in this worktree, start it on a free port, screenshot pages, stop it.
# One command an agent can run without approval (the design critic couldn't start a server and
# take screenshots on its own in the dry run).
#
#   scripts/shoot.sh /                    # phone size, 390x844
#   scripts/shoot.sh / /e/cnh/spots/abc   # several pages
#   SIZE=1920x1080 scripts/shoot.sh /e/cnh/wall
#   THEME=dark scripts/shoot.sh /
#
# Prints the PNG paths; open them with the Read tool. App logs go next to the shots.
set -euo pipefail

[[ $# -ge 1 ]] || { echo "usage: shoot.sh <path> [path...]" >&2; exit 1; }
root=$(git rev-parse --show-toplevel)
cd "$root/app"
# shellcheck disable=SC1091
source "$root/scripts/slice-env.sh"

mains=(cmd/*/)
[[ -d ${mains[0]} ]] || { echo "no main package under app/cmd/" >&2; exit 1; }
slice=$(git branch --show-current | sed -nE 's#^slice/(S[0-9]+).*#\1#p')
# Inside this worktree (gitignored), so an agent running here can open the PNGs.
out="$root/.harness/shots/${slice:-main}"
mkdir -p "$out"

port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("127.0.0.1",0)); print(s.getsockname()[1])')
bin="$out/app-under-test"
go build -o "$bin" "./${mains[0]}"
# The slice's database starts empty; without this the screens the critic needs can't render.
if [[ -n ${DATABASE_URL:-} ]]; then "$bin" migrate > "$out/migrate.log" 2>&1 || { tail -n 20 "$out/migrate.log" >&2; exit 1; }; fi
PORT=$port ADDR="127.0.0.1:$port" "$bin" > "$out/app.log" 2>&1 &
pid=$!
trap 'kill $pid 2>/dev/null || true' EXIT

for _ in $(seq 1 50); do
  curl -fs "http://127.0.0.1:$port/healthz" >/dev/null 2>&1 && break
  kill -0 $pid 2>/dev/null || { echo "app exited; see $out/app.log" >&2; tail -n 20 "$out/app.log" >&2; exit 1; }
  sleep 0.2
done

size=${SIZE:-390x844}
theme=${THEME:-light}
w=${size%x*}; h=${size#*x}

# Prefer a Playwright library that's already installed with its browser; otherwise the pinned CLI
# that scripts/setup-tools.sh installs a browser for.
if node -e "require('playwright')" 2>/dev/null; then
  shoot() { node -e '
    const { chromium } = require("playwright");
    const [url, file, w, h, theme] = process.argv.slice(1);
    (async () => {
      const b = await chromium.launch();
      const p = await b.newPage({ viewport: { width: +w, height: +h }, colorScheme: theme, reducedMotion: "reduce" });
      await p.goto(url); await p.waitForTimeout(800);
      await p.screenshot({ path: file, fullPage: true }); await b.close();
    })().catch((e) => { console.error(e.message); process.exit(1); });' "$1" "$2" "$w" "$h" "$theme"; }
else
  shoot() { npx --yes playwright@1.52.0 screenshot --viewport-size="$w,$h" --color-scheme="$theme" \
    --wait-for-timeout=800 --full-page "$1" "$2" >/dev/null; }
fi

for path in "$@"; do
  name=$(echo "$path" | sed -E 's#^/##; s#[^A-Za-z0-9]+#-#g; s#^$#index#')
  file="$out/$name-${size}-$theme.png"
  shoot "http://127.0.0.1:$port$path" "$file"
  echo "$file"
done
