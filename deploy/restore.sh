#!/usr/bin/env bash
# Restore a dump made by backup.sh (run as root).
#
#   restore.sh <dump-file>
#
# Restores into a fresh database first, so a bad dump never touches the live
# one. Then it stops plurb, swaps the databases by renaming (the current one is
# kept as plurb_before_restore_<time>, drop it by hand once you're happy), and
# starts plurb again. The binary applies any migrations the dump predates.
set -euo pipefail

dump="${1:?usage: restore.sh <dump-file>}"
db="${PLURB_DB:-plurb}"
owner="${PLURB_DB_OWNER:-plurb}"
service="${PLURB_SERVICE:-plurb}"
pg_as="${PG_AS-runuser -u postgres --}"
systemctl="${SYSTEMCTL:-systemctl}"
stamp="$(date -u +%Y%m%dT%H%M%SZ)"
staging="${db}_restore_$stamp"
old="${db}_before_restore_$stamp"

log() { echo "restore: $*"; }
# shellcheck disable=SC2086 # pg_as is a command prefix
psql_admin() { $pg_as psql -v ON_ERROR_STOP=1 -q -d postgres "$@"; }

[ -r "$dump" ] || { log "can't read $dump"; exit 1; }
# Dumps live in a root-only directory, so pg_restore (running as postgres)
# reads them on stdin rather than by path.
# shellcheck disable=SC2086
$pg_as pg_restore --list < "$dump" > /dev/null

if [ -z "${ASSUME_YES:-}" ]; then
    read -r -p "Replace database '$db' with $dump? [y/N] " ok
    [ "$ok" = y ] || exit 1
fi

log "restoring into $staging"
psql_admin -c "CREATE DATABASE \"$staging\" OWNER \"$owner\""
# --role: objects end up owned by the app role, not postgres.
# shellcheck disable=SC2086
if ! $pg_as pg_restore --exit-on-error --no-owner --role="$owner" --dbname="$staging" < "$dump"; then
    log "restore failed; live database untouched ($staging left for inspection)"
    exit 1
fi

log "swapping databases"
$systemctl stop "$service"
psql_admin > /dev/null <<SQL
SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = '$db' AND pid <> pg_backend_pid();
ALTER DATABASE "$db" RENAME TO "$old";
ALTER DATABASE "$staging" RENAME TO "$db";
SQL
$systemctl start "$service"
log "done. previous database kept as $old"
