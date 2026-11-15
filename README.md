# plurb

> Reputation-weighted community platform where your voice is earned.

plurb is a self-hosted, text-only community platform: one Go binary in front of PostgreSQL.
Phase 1 (**Panorama**) is a multi-user markdown blog. Next: reputation-weighted discussion.

**Live:** [plurb.org](https://plurb.org)

## Design

- **Text only.** The database stores words. Images and video are linked from elsewhere:
  covers must be `https://` URLs, markdown images must be `https://`, and bare YouTube /
  Bilibili / Vimeo links become embeds built from a validated video id.
- **Safe rendering.** Markdown goes through goldmark with raw HTML disabled, plus an AST
  pass that enforces the URL policy before rendering. A strict CSP (`script-src 'self'`,
  no inline styles) backs it up.
- **One binary.** Templates, CSS and JS are embedded; migrations run on startup.

## Stack

| | |
|---|---|
| Language | Go (stdlib `net/http` routing, `html/template`) |
| Database | PostgreSQL (`lib/pq`), embedded SQL migrations |
| Markdown | goldmark + chroma (server-side highlighting) |
| Search | Postgres full-text search + `pg_trgm` for substring / CJK matches |
| Auth | PBKDF2-SHA256 passwords, DB-backed sessions, GitHub OAuth |
| Infra | nginx, systemd |

## Layout

```
cmd/plurb/            entrypoint: serve, migrate, createuser, passwd, import-legacy
internal/config/      env configuration
internal/db/          connection pool, migrator, migrations/*.sql
internal/store/       data access (plain SQL)
internal/markdown/    safe markdown renderer, embeds, TOC
internal/auth/        password hashing, GitHub OAuth client
internal/web/         handlers, middleware, templates/, static/
internal/legacy/      one-off importer from the old Java/Spring database
deploy/               systemd unit, nginx site, env example, deploy script
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

### Tests

Unit tests run anywhere. Integration tests need a database whose name ends in `_test`
(each test gets its own throwaway schema):

```bash
createdb plurb_test
PLURB_TEST_DATABASE_URL='postgres://plurb:plurb@localhost:5432/plurb_test?sslmode=disable' go test ./...
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
