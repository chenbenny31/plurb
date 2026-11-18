#!/usr/bin/env bash
# Swap back to the previous binary (run as root): for a release that came up
# healthy but misbehaves. Running it again swaps forward. Data is untouched;
# migrations are backward compatible, so the older binary runs on the newer schema.
set -euo pipefail
app_dir="${PLURB_DIR:-/opt/plurb}"
service="${PLURB_SERVICE:-plurb}"
systemctl="${SYSTEMCTL:-systemctl}"

[ -x "$app_dir/plurb.prev" ] || { echo "rollback: no previous binary in $app_dir"; exit 1; }
from="$("$app_dir/plurb" version)"
to="$("$app_dir/plurb.prev" version)"
mv "$app_dir/plurb" "$app_dir/plurb.swap"
mv "$app_dir/plurb.prev" "$app_dir/plurb"
mv "$app_dir/plurb.swap" "$app_dir/plurb.prev"
$systemctl restart "$service"
echo "rollback: $from -> $to (run again to undo)"
