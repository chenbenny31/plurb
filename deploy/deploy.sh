#!/usr/bin/env bash
# shellcheck disable=SC2029 # $stage/$version are meant to expand locally
# Build plurb and roll it out to a server over ssh.
#
#   deploy.sh user@host                         upgrade (backup, migrate, swap, health check)
#   deploy.sh --setup <domain> <email> user@host first time: provision the server, then deploy
#
# The version is `git describe`, stamped into the binary and checked against
# /healthz after the restart, so a deploy only reports success once the new
# build is the one answering. See docs/operations.md.
set -euo pipefail

setup=()
if [ "${1:-}" = --setup ]; then
    setup=("${2:?--setup needs <domain> <email>}" "${3:?--setup needs <domain> <email>}")
    shift 3
fi
host="${1:?usage: deploy.sh [--setup <domain> <email>] user@host}"
arch="${GOARCH:-amd64}"

cd "$(dirname "$0")/.."
if [ -n "$(git status --porcelain)" ] && [ -z "${ALLOW_DIRTY:-}" ]; then
    echo "deploy: working tree has uncommitted changes (ALLOW_DIRTY=1 to deploy anyway)"
    exit 1
fi
version="$(git describe --tags --always --dirty)"

echo "deploy: building $version for linux/$arch"
CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath \
    -ldflags="-s -w -X main.version=$version" -o bin/plurb-linux ./cmd/plurb

stage=/tmp/plurb-deploy
ssh "$host" "rm -rf $stage && mkdir -p $stage"
scp -q bin/plurb-linux "$host:$stage/plurb"
scp -q deploy/*.sh deploy/*.service deploy/*.timer deploy/nginx.conf deploy/plurb.env.example "$host:$stage/"

if [ ${#setup[@]} -gt 0 ]; then
    ssh -t "$host" "sudo bash $stage/provision.sh ${setup[0]} ${setup[1]}"
fi

# Refresh the helper scripts, then upgrade.
ssh -t "$host" "sudo install -m 755 $stage/backup.sh $stage/upgrade.sh $stage/restore.sh $stage/rollback.sh /opt/plurb/scripts/ \
    && sudo /opt/plurb/scripts/upgrade.sh $stage/plurb $version"
