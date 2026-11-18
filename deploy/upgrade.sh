#!/usr/bin/env bash
# Roll a new plurb binary out on this machine (run as root; deploy.sh calls it).
#
#   upgrade.sh <new-binary> <expected-version>
#
# 1. back up the database
# 2. run the new binary's migrations while the old binary keeps serving
#    (migrations are expand/contract, so the old binary works on the new schema)
# 3. swap the binary, restart, and wait for /healthz to answer with the new version
# 4. if it doesn't come up, put the previous binary back and restart it
#
# Data is never rolled back automatically: the schema stays migrated and the
# old binary runs on it. The pre-upgrade dump is there if a restore is needed
# (see restore.sh).
set -euo pipefail

new="${1:?usage: upgrade.sh <new-binary> <expected-version>}"
want="${2:?usage: upgrade.sh <new-binary> <expected-version>}"

app_dir="${PLURB_DIR:-/opt/plurb}"
service="${PLURB_SERVICE:-plurb}"
health_url="${HEALTH_URL:-http://127.0.0.1:8080/healthz}"
scripts="$(cd "$(dirname "$0")" && pwd)"
# How to run the binary as the service would (same user, same env file).
run_as_app="${RUN_AS_APP-systemd-run --quiet --wait --pipe --collect --uid=plurb --gid=plurb -p EnvironmentFile=/etc/plurb/plurb.env}"
systemctl="${SYSTEMCTL:-systemctl}"
tries="${HEALTH_TRIES:-20}"

log() { echo "upgrade: $*"; }

healthy() { # healthy <version>: 200 from /healthz with that version
    local headers
    headers="$(curl -fsS -m 2 -D - -o /dev/null "$health_url" 2>/dev/null)" || return 1
    grep -qi "^x-plurb-version: $1"$'\r'"\?$" <<< "$headers"
}

wait_healthy() {
    for _ in $(seq "$tries"); do
        healthy "$1" && return 0
        sleep 1
    done
    return 1
}

chmod 755 "$new"
got="$("$new" version)"
[ "$got" = "$want" ] || { log "binary reports version $got, expected $want"; exit 1; }

current="none"
[ -x "$app_dir/plurb" ] && current="$("$app_dir/plurb" version 2>/dev/null || echo unknown)"
log "$current -> $want"

log "backing up"
"$scripts/backup.sh" "pre-$want"

log "applying migrations with the new binary (old one still serving)"
# shellcheck disable=SC2086 # run_as_app is a command prefix
if ! $run_as_app "$new" migrate; then
    log "migrations failed; nothing was swapped, $current keeps running"
    exit 1
fi

# Keep the running binary aside; it only becomes plurb.prev (the target of
# rollback.sh) once the new one is confirmed healthy, so a failed attempt
# leaves the previous rollback target intact.
if [ -x "$app_dir/plurb" ]; then
    cp -p "$app_dir/plurb" "$app_dir/plurb.running"
fi
install -m 755 "$new" "$app_dir/plurb.next"
mv "$app_dir/plurb.next" "$app_dir/plurb"   # atomic rename
$systemctl restart "$service"

if wait_healthy "$want"; then
    [ -f "$app_dir/plurb.running" ] && mv "$app_dir/plurb.running" "$app_dir/plurb.prev"
    log "live: $want"
    exit 0
fi

log "$want did not become healthy; rolling back to $current"
if [ -x "$app_dir/plurb.running" ]; then
    mv "$app_dir/plurb.running" "$app_dir/plurb"
    $systemctl restart "$service"
    if wait_healthy "$current"; then
        log "rolled back: $current is serving (schema stays migrated)"
    else
        log "ROLLBACK ALSO UNHEALTHY: check 'journalctl -u $service'"
    fi
else
    log "no previous binary to roll back to"
fi
exit 1
