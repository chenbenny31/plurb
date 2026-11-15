#!/usr/bin/env bash
# Build a static linux binary and roll it out: ./deploy/deploy.sh user@host
set -euo pipefail
host="${1:?usage: deploy.sh user@host}"
arch="${GOARCH:-amd64}"

cd "$(dirname "$0")/.."
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags="-s -w" -o bin/plurb-linux ./cmd/plurb

scp bin/plurb-linux "$host:/tmp/plurb.new"
# Migrations run on startup; the previous binary is kept for a quick rollback.
ssh "$host" 'set -e
  sudo install -m 755 /tmp/plurb.new /opt/plurb/plurb.next
  if [ -f /opt/plurb/plurb ]; then sudo cp /opt/plurb/plurb /opt/plurb/plurb.prev; fi
  sudo mv /opt/plurb/plurb.next /opt/plurb/plurb
  sudo systemctl restart plurb
  sleep 1
  curl -fsS http://127.0.0.1:8080/healthz'
