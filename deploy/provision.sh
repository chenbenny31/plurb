#!/usr/bin/env bash
# One-time setup of a fresh Debian 12 / Ubuntu 24.04 server (run as root).
# Safe to re-run: every step checks before it changes anything.
#
#   provision.sh <domain> <email-for-letsencrypt>
#
# Installs Postgres, Redis, nginx and certbot; creates the plurb system user,
# database and /etc/plurb/plurb.env (with a generated DB password); installs
# the systemd units and the nginx site; gets a TLS certificate. DNS for the
# domain (and www.) must already point at this machine.
set -euo pipefail

domain="${1:?usage: provision.sh <domain> <email>}"
email="${2:?usage: provision.sh <domain> <email>}"
src="$(cd "$(dirname "$0")" && pwd)"
log() { echo "provision: $*"; }

[ "$(id -u)" = 0 ] || { log "run as root"; exit 1; }

log "packages"
export DEBIAN_FRONTEND=noninteractive
apt-get update -qq
apt-get install -y -qq postgresql redis-server nginx certbot python3-certbot-nginx curl rclone openssl > /dev/null

log "user and directories"
id plurb > /dev/null 2>&1 || useradd --system --home-dir /nonexistent --no-create-home --shell /usr/sbin/nologin plurb
install -d -m 755 /opt/plurb /opt/plurb/scripts
install -d -m 700 /etc/plurb /var/backups/plurb
for f in backup.sh upgrade.sh restore.sh rollback.sh; do
    install -m 755 "$src/$f" "/opt/plurb/scripts/$f"
done

log "database"
pw=""
if [ ! -f /etc/plurb/plurb.env ]; then
    pw="$(openssl rand -hex 24)"
    sed -e "s|CHANGE_ME|$pw|" -e "s|https://plurb.org|https://$domain|g" "$src/plurb.env.example" > /etc/plurb/plurb.env
    chmod 600 /etc/plurb/plurb.env
    log "wrote /etc/plurb/plurb.env"
fi
as_pg() { runuser -u postgres -- psql -v ON_ERROR_STOP=1 -qAt "$@"; }
if [ "$(as_pg -c "SELECT 1 FROM pg_roles WHERE rolname = 'plurb'")" != 1 ]; then
    if [ -z "$pw" ]; then
        log "role plurb is missing but plurb.env already exists: set its password to match DATABASE_URL by hand"
        exit 1
    fi
    as_pg -c "CREATE ROLE plurb LOGIN PASSWORD '$pw'"
fi
if [ "$(as_pg -c "SELECT 1 FROM pg_database WHERE datname = 'plurb'")" != 1 ]; then
    as_pg -c "CREATE DATABASE plurb OWNER plurb"
fi

log "systemd units"
install -m 644 "$src/plurb.service" /etc/systemd/system/plurb.service
install -m 644 "$src/plurb-backup.service" /etc/systemd/system/plurb-backup.service
install -m 644 "$src/plurb-backup.timer" /etc/systemd/system/plurb-backup.timer
systemctl daemon-reload
systemctl enable --quiet plurb               # started by the first upgrade
systemctl enable --quiet --now plurb-backup.timer
systemctl enable --quiet --now redis-server postgresql

log "tls certificate"
if [ ! -d "/etc/letsencrypt/live/$domain" ]; then
    certbot certonly --nginx --non-interactive --agree-tos -m "$email" -d "$domain" -d "www.$domain"
fi

log "nginx site"
sed "s/plurb\.org/$domain/g" "$src/nginx.conf" > /etc/nginx/sites-available/plurb
ln -sf /etc/nginx/sites-available/plurb /etc/nginx/sites-enabled/plurb
rm -f /etc/nginx/sites-enabled/default
nginx -t
systemctl reload nginx

if command -v ufw > /dev/null && ufw status | grep -q active; then
    ufw allow OpenSSH > /dev/null
    ufw allow 'Nginx Full' > /dev/null
fi

log "done. next: deploy the binary (deploy.sh), then create your account:"
log "  sudo systemd-run --quiet --wait --pipe --uid=plurb -p EnvironmentFile=/etc/plurb/plurb.env /opt/plurb/plurb createuser -admin -name 'Your Name' you"
