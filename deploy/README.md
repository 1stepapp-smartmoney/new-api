# Deployment configs

Two compose files live in this repo, for different audiences:

| File | Audience | Backing services |
|---|---|---|
| `/docker-compose.yml` (repo root) | Upstream demo / first-time evaluators | Sidecar PostgreSQL + Redis containers |
| `deploy/docker-compose.prod.yml` (this dir) | Production (nexapi.org) | Managed AWS RDS MySQL + ClickHouse + Valkey, all secrets in `.env` |

The root file is shipped by upstream `QuantumNous/new-api` and should keep
following upstream changes. Don't use it for production — the bundled
postgres container is just a "try-it-out" convenience.

## Production topology

Since the 2026-07-14 migration off Aliyun, production runs on AWS:

| Component | Where |
|---|---|
| Main DB (business data) | AWS RDS MySQL 8.0, ap-northeast-1 — only ~30 MB; users/channels/tokens/quota |
| **Request logs** | **ClickHouse** (`LOG_SQL_DSN`), *not* the main DB. MergeTree, monthly partitions, retention via `LOG_SQL_CLICKHOUSE_TTL_DAYS` |
| Cache | ElastiCache for **Valkey**, cluster-mode-disabled, TLS → `rediss://` |

Three application instances, all running the same image:

| Site | Host | Directory | Container | Published port | `mem_limit` |
|---|---|---|---|---|---|
| Tokyo | `ubuntu@54.64.192.229` | `~/nexapi-docker` | `new-api` | `3000:3000` | 40g |
| us-west-2 A | `ubuntu@52.41.195.28` | `~/nexapi-a` | `new-api-a` | `127.0.0.1:3000:3000` | 24g |
| us-west-2 B | `ubuntu@52.41.195.28` | `~/nexapi-b` | `new-api-b` | `127.0.0.1:3001:3000` | 24g |

The two us-west-2 sites are **billing-independent** (separate databases and log
databases) but reuse the Tokyo RDS/ClickHouse over an inter-region VPC peering.
They share one 64 GiB box, hence 24g each; Tokyo has its 62 GiB box to itself.

`deploy/docker-compose.prod.yml` is the **canonical copy and matches Tokyo
verbatim**. The us-west-2 files differ only in the three columns above.

## Deploying

Normal path — one command from a workstation:

```bash
./scripts/redeploy.sh            # build on the build host, then roll out all three sites
./scripts/redeploy.sh build      # build + push the image only
./scripts/redeploy.sh deploy     # skip the build, just re-pull the current :latest everywhere
./scripts/redeploy.sh tokyo      # single site
./scripts/redeploy.sh usw2       # both us-west-2 sites
```

It pulls, recreates, then waits for `healthy` (up to 240 s — AutoMigrate is slow
on large tables) and probes `/api/status`; any failure exits non-zero.

Manual equivalent for one host:

```bash
ssh ubuntu@<host> 'cd ~/nexapi-docker && sudo docker compose pull && sudo docker compose up -d'
```

> **Use `up -d`, never `restart`.** `docker compose restart` reuses the existing
> container definition, so changes to `.env` or to the compose file are silently
> ignored. `up -d` recreates the container and is also what picks up a new
> `:latest` image.

Sync a compose change from this repo to a host:

```bash
scp deploy/docker-compose.prod.yml ubuntu@54.64.192.229:~/nexapi-docker/docker-compose.yml
```

Drift-check a host against the tracked copy (ignoring comments/blank lines):

```bash
diff <(grep -vE '^\s*#|^\s*$' deploy/docker-compose.prod.yml) \
     <(ssh ubuntu@54.64.192.229 'sudo cat ~/nexapi-docker/docker-compose.yml' | grep -vE '^\s*#|^\s*$')
```

For a us-west-2 site, expect exactly the three documented deltas
(`container_name`, port binding, `mem_limit`) and nothing else.

## Secrets and key settings

`.env` lives only on the hosts and is excluded from this repo (see `.gitignore` /
`.dockerignore`). Keys currently in use:

**Connections**

- `SQL_DSN` — AWS RDS MySQL. Go MySQL DSN form (`user:pass@tcp(host:3306)/db?...`),
  so special characters in the password need no URL-encoding.
- `LOG_SQL_DSN` — ClickHouse, `clickhouse://user:pass@host:9000/db`.
- `REDIS_CONN_STRING` — Valkey. ElastiCache enforces TLS, so the scheme must be
  **`rediss://`**; plain `redis://` fails with a PING timeout.

> ⚠️ `LOG_SQL_DSN` and `REDIS_CONN_STRING` are **URLs**: `# ? @ / %` in a password
> must be percent-encoded (`#` → `%23`). A raw `#` truncates the URL and the host
> collapses to the username — the symptom is `dial tcp: lookup default`. Prefer
> alphanumeric-only passwords for these two.

**Identity — must be consistent, or shared state breaks**

- `SESSION_SECRET` — same value on every instance, or logins bounce between them.
- `CRYPTO_SECRET` — same value on every instance that shares a Redis/Valkey.
  It defaults to a random UUID per process, so leaving it unset silently makes
  each instance unable to decrypt the others' cached data.
- `NODE_NAME` — **unique and stable per instance**. Unset means the container's
  random hostname is used, so every recreate registers a new "instance" row in
  `system_instances` and leaves ghost entries behind.

**Runtime tuning**

- `GOMEMLIMIT` — Go soft memory ceiling. Keep it several GB **below** the
  container's `mem_limit` so the GC gets aggressive before Docker kills the
  container. Setting them equal defeats the purpose.
- `SQL_MAX_LIFETIME=1800` — required on the us-west-2 sites. The default (60 s)
  rebuilds every connection each minute; a trans-Pacific handshake costs ~400 ms.
- `TRUSTED_PROXIES`, `SESSION_COOKIE_SECURE`, `SESSION_COOKIE_TRUSTED_URL` —
  set on the us-west-2 sites, which sit behind Cloudflare.
- `FORCE_RECORD_IP_LOG=true` — enables the root-only IP audit (FORK-CHANGES §4).
- `ENABLE_PPROF=true` — exposes pprof on :8005 inside the container; used to
  diagnose heap growth.

Rotate secrets via the cloud console and edit `.env` on the host directly.
Never check `.env` into git.

### Stale key to clean up

`STREAM_DRAIN_ON_CLIENT_GONE` is present in every host `.env` but **no longer
read by any code** — the fork's drain-on-disconnect feature was dropped, and
upstream independently removed drain behaviour in its rc.18 stream_scanner
rewrite. Harmless, but safe to delete on the next `.env` edit.

## Real client IP (Tokyo)

Tokyo is fronted by nginx behind Zenlayer GA with Proxy Protocol enabled.
Enabling `listen ... proxy_protocol` alone is **not enough**: `$remote_addr`
stays the GA origin-fetch IP unless the real_ip module is told to substitute it.
Both lines are required in each `proxy_protocol` server block:

```nginx
set_real_ip_from 129.227.236.0/24;   # every Zenlayer GA origin-fetch range
real_ip_header proxy_protocol;
```

Without them all users collapse into a single client IP, which shares one
rate-limit bucket (login returns 429 for everyone) and makes the §4 IP audit log
worthless. Verify by checking that new-api sees a spread of client IPs:

```bash
ssh ubuntu@54.64.192.229 "cd ~/nexapi-docker && sudo docker compose logs --since 5m new-api" \
  | grep -oE '\|[[:space:]]+[0-9.]+[[:space:]]+\|' | tr -d ' |' | sort | uniq -c | sort -rn | head
```

The us-west-2 sites are behind Cloudflare and bind to `127.0.0.1` only, so they
use ordinary `X-Forwarded-For` handling and no Proxy Protocol.
