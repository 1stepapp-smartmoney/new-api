# Deployment configs

Two compose files live in this repo, for different audiences:

| File | Audience | Backing services |
|---|---|---|
| `/docker-compose.yml` (repo root) | Upstream demo / first-time evaluators | Sidecar PostgreSQL + Redis containers |
| `deploy/docker-compose.prod.yml` (this dir) | Production (nexapi.org) | Managed Aliyun RDS MySQL + managed Redis, all secrets in `.env` |

The root file is shipped by upstream `QuantumNous/new-api` and should keep
following upstream changes. Don't use it for production — the bundled
postgres container is just a "try-it-out" convenience.

## Deploying to production

The production server keeps a working tree under `~/new-api-docker/` on
the host. It is **not** a git checkout — it only contains the compose file
and an `.env` with secrets.

Sync workflow:

```bash
# 1. After editing deploy/docker-compose.prod.yml in this repo:
git pull origin main
scp deploy/docker-compose.prod.yml \
    ecs-user@<host>:~/new-api-docker/docker-compose.yml

# 2. On the host, apply:
ssh ecs-user@<host> 'cd ~/new-api-docker && \
  sudo docker compose pull && \
  sudo docker compose up -d'
```

Or to drift-check before pushing:

```bash
ssh ecs-user@<host> 'sudo cat ~/new-api-docker/docker-compose.yml' \
  | diff - deploy/docker-compose.prod.yml
```

## Secrets

The `.env` file lives only on the production host and is excluded from
this repo (see `.gitignore` / `.dockerignore`). It contains:

- `SQL_DSN` — RDS MySQL connection string
- `REDIS_CONN_STRING` — managed Redis URL
- `SESSION_SECRET` — must be a stable random string across redeployments
- `CRYPTO_SECRET` — same; rotating it invalidates all stored encrypted blobs
- Optional: `FORCE_RECORD_IP_LOG=true` to enable the root-only IP audit
  (see FORK-CHANGES.md §4)

If you need to rotate any of the above, do it via the cloud console and
update `.env` directly on the host. Never check `.env` into git.
