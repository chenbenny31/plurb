# Operations

How plurb is deployed, upgraded, backed up and restored. Everything here is
plain shell in `deploy/`, rehearsed end to end (first deploy, upgrade with a
migration, a release that crashes on start, a migration that fails, manual
rollback onto a newer schema, restore after data loss).

## Where the data lives

All state is in one PostgreSQL database. The binary embeds its templates and
assets and writes nothing to disk; Redis holds only caches, rate-limit windows
and view counts waiting to be flushed (losing it costs at most ~30 s of view
counts). So "the data" is the database, and backing it up is backing up plurb.

Some of what's in the database is derived and can be recomputed at any time:

| Stored | Source of truth | Recompute |
|---|---|---|
| `users.reputation` | the append-only `rep_events` ledger | `plurb rep-rebuild` |
| reputation levels | not stored: percentiles over `users.reputation` | automatic, every 10 min |
| search vectors | post/thread text | generated columns |
| rendered markdown | `body_md` | cache only |

## First deploy

DNS for the domain and `www.` must point at the server.

```bash
./deploy/deploy.sh --setup plurb.org you@example.com user@server
```

`--setup` runs `deploy/provision.sh` on the server (idempotent): installs
Postgres, Redis, nginx and certbot; creates the `plurb` system user and
database; writes `/etc/plurb/plurb.env` with a generated database password;
installs the systemd unit, the daily backup timer and the nginx site; gets the
TLS certificate. Then it deploys like any upgrade. Create your account after:

```bash
sudo systemd-run --quiet --wait --pipe --uid=plurb -p EnvironmentFile=/etc/plurb/plurb.env \
    /opt/plurb/plurb createuser -admin -name 'Benny Chen' benny
```

Edit `/etc/plurb/plurb.env` for sign-up (`PLURB_SIGNUP=open`) and GitHub
login, then `sudo systemctl restart plurb`.

## Upgrades

```bash
git tag v0.2.0 && ./deploy/deploy.sh user@server
```

The version is `git describe`, stamped into the binary. On the server
`upgrade.sh`:

1. dumps the database (`pre-<version>`),
2. runs the new binary's migrations **while the old binary keeps serving**,
3. swaps the binary and restarts,
4. waits for `/healthz` to answer 200 with `X-Plurb-Version: <version>`.

If migrations fail, nothing is swapped. If the new binary doesn't come up
healthy, the previous one is put back automatically. In both cases the old
version keeps serving and the script exits non-zero.

A release that comes up healthy but misbehaves: `sudo /opt/plurb/scripts/rollback.sh`
(run it again to swap forward). `plurb migrate -status` shows what the
database has applied; `curl -sI https://plurb.org/healthz` shows what's live.

### Writing migrations

Migrations are `internal/db/migrations/NNNN_name.sql`, applied in order, each
in its own transaction, under an advisory lock.

- **Never edit an applied migration.** The migrator records each file's
  SHA-256 and refuses to start if one changed. Fix forward with a new file.
- **Expand, then contract.** Each migration must keep working with the
  previous release, because during an upgrade the old binary serves on the new
  schema, and a rollback runs it there indefinitely. So:
  - add tables, nullable columns, columns with defaults, indexes
    (`CREATE INDEX CONCURRENTLY` can't run in a transaction; for big tables
    put it in its own release);
  - to rename or change a column: add the new one, write both, backfill,
    switch reads; drop the old one a release later;
  - to drop: stop using it in release N, drop it in N+1.
- A binary that finds migrations newer than itself starts anyway and logs a
  warning; that's the rollback case.

## Backups

`plurb-backup.timer` runs `backup.sh daily` at about 03:17. Every deploy also
takes a `pre-<version>` dump. Dumps are `pg_dump` custom format in
`/var/backups/plurb` (root only), checked with `pg_restore --list` before
they're kept. Retention: the newest 14, plus the newest of each of the last 8
weeks (`KEEP_RECENT`, `KEEP_WEEKS`).

**Off-machine copies.** A backup on the same disk doesn't survive losing the
server. Configure an rclone remote (`sudo rclone config`, e.g. Backblaze B2
or S3), then put `BACKUP_REMOTE=b2:plurb-backups` in `/etc/plurb/backup.env`.
Each dump is then copied there too; set a lifecycle rule on the bucket for
retention.

Manual dump: `sudo /opt/plurb/scripts/backup.sh before-something-risky`.

## Restore

```bash
sudo /opt/plurb/scripts/restore.sh /var/backups/plurb/plurb-20261117T031700Z-daily.dump
```

It restores into a fresh database first (a bad dump never touches the live
one), then stops plurb, swaps databases by renaming, and starts plurb, which
applies any migrations newer than the dump. The replaced database is kept as
`plurb_before_restore_<time>`; drop it once you're satisfied. Restore
from an off-machine copy by `rclone copy`-ing the dump down first.

Practise this before you need it: restore last night's dump on a scratch
machine (or into a scratch database with `PLURB_DB=plurb_drill`) now and then.

## Users' own data

Authors can download a zip of all their posts (drafts included) as markdown
files from the dashboard (`Export`), so their writing is never locked in.
