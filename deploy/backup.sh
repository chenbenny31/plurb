#!/usr/bin/env bash
# Dump the plurb database and prune old dumps.
#
#   backup.sh [label]       e.g. backup.sh daily, backup.sh pre-v0.3.0
#
# Dumps are pg_dump custom-format archives (compressed, restorable table by
# table with pg_restore), named plurb-<UTC time>-<label>.dump. Retention keeps
# the newest KEEP_RECENT dumps plus the newest dump of each of the last
# KEEP_WEEKS ISO weeks. Set BACKUP_REMOTE to an rclone destination
# (e.g. "b2:plurb-backups") to also copy each dump off the machine.
#
# Runs as root (systemd timer / deploy); talks to Postgres as the postgres
# superuser over the local socket. Every knob below can be overridden from
# the environment, which is also how the scripts are rehearsed locally.
set -euo pipefail

label="${1:-manual}"
db="${PLURB_DB:-plurb}"
dir="${BACKUP_DIR:-/var/backups/plurb}"
keep_recent="${KEEP_RECENT:-14}"
keep_weeks="${KEEP_WEEKS:-8}"
# Command prefix for running Postgres client tools; empty = run as-is.
pg_as="${PG_AS-runuser -u postgres --}"

mkdir -p "$dir"
chmod 700 "$dir"

stamp="$(date -u +%Y%m%dT%H%M%SZ)"
file="$dir/plurb-$stamp-$label.dump"
tmp="$file.partial"

# shellcheck disable=SC2086 # pg_as is a command prefix, split on purpose
$pg_as pg_dump --format=custom --compress=6 --no-owner --dbname="$db" > "$tmp"
# A dump that pg_restore can't read is worse than none: check before keeping it.
# (Fed on stdin: the postgres user can't open files in this root-only directory.)
# shellcheck disable=SC2086
$pg_as pg_restore --list < "$tmp" > /dev/null
mv "$tmp" "$file"
chmod 600 "$file"
echo "backup: $file ($(du -h "$file" | cut -f1))"

if [ -n "${BACKUP_REMOTE:-}" ]; then
    rclone copy --quiet "$file" "$BACKUP_REMOTE"
    echo "backup: copied to $BACKUP_REMOTE"
fi

# ---- retention ----
# Names sort by time, so newest first is a reverse sort.
mapfile -t dumps < <(find "$dir" -maxdepth 1 -name 'plurb-*.dump' -printf '%f\n' | sort -r)
declare -A keep=() week_seen=()
for i in "${!dumps[@]}"; do
    f="${dumps[$i]}"
    [ "$i" -lt "$keep_recent" ] && keep[$f]=1
    # plurb-20261117T031700Z-... -> ISO week of that date
    d="${f:6:8}"
    week="$(date -u -d "${d:0:4}-${d:4:2}-${d:6:2}" +%G-%V)"
    if [ -z "${week_seen[$week]:-}" ] && [ "${#week_seen[@]}" -lt "$keep_weeks" ]; then
        week_seen[$week]=1
        keep[$f]=1
    fi
done
for f in "${dumps[@]}"; do
    if [ -z "${keep[$f]:-}" ]; then
        rm -f -- "${dir:?}/$f"
        echo "backup: pruned $f"
    fi
done
