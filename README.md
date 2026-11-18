# plurb

> Reputation-weighted community platform where your voice is earned.

plurb is a self-hosted, text-only community platform: one Go binary in front of PostgreSQL.
**Panorama** is a multi-user markdown blog; the **Study Room** is a forum where votes are
weighted by earned reputation.

**Live:** [plurb.org](https://plurb.org)

## Design

- **Text only.** The database stores words. Images and video are linked from elsewhere:
  covers must be `https://` URLs, markdown images must be `https://`, and bare YouTube /
  Bilibili / Vimeo links become embeds built from a validated video id.
- **Safe rendering.** Markdown goes through goldmark with raw HTML disabled, plus an AST
  pass that enforces the URL policy before rendering. A strict CSP (`script-src 'self'`,
  no inline styles) backs it up.
- **Reputation-weighted votes.** A vote's weight is snapshotted from the voter's reputation
  (`1 + log2(1 + rep/100)`, capped at 5; accounts under a day old weigh 0). Reputation lives
  in an append-only ledger (`rep_events`): retracting a vote appends a compensating row, and
  per-voter / per-author daily caps blunt vote rings. Each vote is one transaction that locks
  the target and its author, so concurrent votes can't lose updates. Threads rank by
  `score / (age_hours + 2)^1.5`. The rules live in `internal/rep`.
- **Live notifications.** A reply inserts a notification and calls `pg_notify` in the same
  transaction, so NOTIFY fires only on commit. Each instance holds one `LISTEN` connection
  feeding an in-process hub, which pushes unread counts to browsers over Server-Sent Events —
  replies reach readers connected to any instance, with no extra broker.
- **Caching & limits.** Redis (optional) caches rendered markdown, buffers view counts
  (write-behind, flushed every 30s) and runs sliding-window rate limits as a Lua script.
- **One binary.** Templates, CSS and JS are embedded; migrations run on startup.

## Stack

| | |
|---|---|
| Language | Go (stdlib `net/http` routing, `html/template`) |
| Database | PostgreSQL (`lib/pq`), embedded SQL migrations |
| Markdown | goldmark + chroma (server-side highlighting) |
| Search | Postgres full-text search + `pg_trgm` for substring / CJK matches |
| Auth | PBKDF2-SHA256 passwords, DB-backed sessions, GitHub OAuth |
| Cache | Redis (`go-redis`), with an in-process fallback |
| Infra | nginx, systemd |

## Layout

```
cmd/plurb/            entrypoint: serve, migrate, createuser, passwd, rep-rebuild, import-legacy
internal/config/      env configuration
internal/db/          connection pool, migrator, migrations/*.sql
internal/store/       data access (plain SQL)
internal/markdown/    safe markdown renderer, embeds, TOC
internal/auth/        password hashing, GitHub OAuth client
internal/rep/         reputation rules: vote weight, rewards, caps, hot ranking
internal/cache/       cache, rate limiter, view counter (Redis or in-memory)
internal/notify/      LISTEN/NOTIFY hub feeding SSE streams
internal/web/         handlers, middleware, templates/, static/
internal/legacy/      one-off importer from the old Java/Spring database
deploy/               systemd unit, nginx site, env example, deploy script
loadtest/             k6 scripts
.github/workflows/    CI: integration tests with Postgres + Redis, k6 smoke
```

## Development

Requires Go 1.24+ and PostgreSQL 13+.

```bash
createdb plurb
PLURB_DEV=1 go run ./cmd/plurb serve           # http://localhost:8080
echo 'a long password' | go run ./cmd/plurb createuser -admin -name Benny benny
```

### Configuration

| Variable | Default | |
|---|---|---|
| `PLURB_ADDR` | `127.0.0.1:8080` | listen address |
| `DATABASE_URL` | `postgres://plurb:plurb@localhost:5432/plurb?sslmode=disable` | |
| `PLURB_BASE_URL` | `http://localhost:8080` | public origin for feeds, sitemap, OG tags |
| `PLURB_DEV` | – | `1` drops the `Secure` cookie flag for plain-http localhost |
| `PLURB_SIGNUP` | `closed` | `open` enables self-service sign-up |
| `GITHUB_CLIENT_ID` / `GITHUB_CLIENT_SECRET` | – | enable GitHub login |
| `REDIS_URL` | – | Redis for cache / rate limits / view counts (in-memory if unset) |
| `PLURB_TRUST_PROXY` | – | `1` to take client IPs from nginx's `X-Real-IP` |
| `PLURB_RATE_LIMIT_SCALE` | `1` | multiply all rate limits (load tests only) |

### Tests

Unit tests run anywhere. Integration tests need a database whose name ends in `_test`
(each test gets its own throwaway schema):

```bash
createdb plurb_test
PLURB_TEST_DATABASE_URL='postgres://plurb:plurb@localhost:5432/plurb_test?sslmode=disable' go test ./...
# optionally also run the cache tests against Redis:
PLURB_TEST_REDIS_URL=redis://localhost:6379/15 go test ./internal/cache
```

### CI

`.github/workflows/ci.yml` runs gofmt, vet and the full integration suite with the race
detector against Postgres 18 and Redis service containers, then boots the built binary and
runs a k6 smoke test against it.

### Load testing

[k6](https://k6.io) scripts live in `loadtest/`:

```bash
PLURB_SIGNUP=open PLURB_RATE_LIMIT_SCALE=1000 go run ./cmd/plurb serve
k6 run -e BASE=http://127.0.0.1:8080 -e VUS=50 loadtest/read.js    # pages, post, thread
k6 run -e BASE=http://127.0.0.1:8080 -e VUS=30 loadtest/mixed.js   # sign-up, comment, vote
```

`mixed.js` concentrates votes on a few threads to exercise the vote transaction's row locks.
Afterwards, check the invariant it protects:

```sql
SELECT t.id, t.score, sum(v.value * v.weight)
FROM threads t LEFT JOIN votes v ON v.target_type = 'thread' AND v.target_id = t.id
GROUP BY t.id HAVING abs(t.score - COALESCE(sum(v.value * v.weight), 0)) > 1e-9;  -- expect no rows
```

## Deployment

```bash
# once, on the server
sudo useradd --system --no-create-home plurb
sudo mkdir -p /opt/plurb /etc/plurb
sudo cp deploy/plurb.env.example /etc/plurb/plurb.env    # then edit; chmod 600
sudo cp deploy/plurb.service /etc/systemd/system/ && sudo systemctl enable plurb
sudo cp deploy/nginx.conf /etc/nginx/sites-available/plurb   # then enable + certbot

# every release, from your machine
./deploy/deploy.sh user@plurb.org
```

### Moving over from the Java version

The new schema differs from the old Flyway one, so import into a fresh database:

```bash
createdb plurb_go     # point DATABASE_URL at it
plurb import-legacy -from 'postgres://user:pass@localhost:5432/plurb?sslmode=disable'
plurb passwd chenbenny31   # old bcrypt hashes aren't carried over
```

Slugs that don't fit the new `a-z0-9-` pattern are rewritten and reported; non-https
cover images are dropped.

## License

- **Source code:** [AGPL-3.0](https://www.gnu.org/licenses/agpl-3.0.html)
- **Content:** [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/)
