#!/usr/bin/env bash
# Install the tools the gates and builders need. Safe to re-run.
# Needs Go 1.23+, Node 20+, and Homebrew or apt for gitleaks.
set -euo pipefail

need() { command -v "$1" >/dev/null || { echo "missing $1: $2" >&2; exit 1; }; }
need go "install Go 1.23+"
need node "install Node 20+"

echo "==> Go tools"
go install github.com/a-h/templ/cmd/templ@v0.3.857
go install github.com/pressly/goose/v3/cmd/goose@v3.24.1
go install github.com/securego/gosec/v2/cmd/gosec@v2.22.2
go install golang.org/x/vuln/cmd/govulncheck@v1.1.4
command -v golangci-lint >/dev/null || \
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.1.6

echo "==> gitleaks"
if ! command -v gitleaks >/dev/null; then
  if command -v brew >/dev/null; then brew install gitleaks
  else go install github.com/zricethezav/gitleaks/v8@v8.24.2
  fi
fi

echo "==> Playwright (Chromium only)"
npx --yes playwright@1.52.0 install chromium

echo "==> Python for hx (spawn-builder.sh reads the spec with PyYAML)"
python3 -c "import yaml" 2>/dev/null || python3 -m pip install --user pyyaml

echo
echo "Done. Check: templ version && goose -version && gosec -version && govulncheck -version && gitleaks version"
