# Fork Customizations vs Upstream

This document tracks **local-only changes** in this fork that are not yet in
the upstream `QuantumNous/new-api` repository. It exists to help future
upstream merges decide whether a local patch can be **dropped in favour of an
official upstream solution** that solves the same problem.

> Workflow on each upstream merge
>
> 1. After `git fetch upstream && git merge upstream/main`, open this file.
> 2. For every entry below, run the listed "Upstream adoption check" command.
> 3. If upstream now ships an equivalent solution, **drop the local patch**
>    (revert the local commit, prefer upstream's implementation) and remove
>    the row from this file. Note the removal in the commit message so the
>    change is auditable.
> 4. If the local fix is still unique, leave the row in place.
>
> Keep entries sorted by the date the local change first landed (oldest first).

---

## Active local customizations

### 1. Dashboard data filter — `model_name` parameter

- **Why**: Upstream's data dashboard (both classic and default UI) supports
  filtering quota statistics by username (admin only) but not by model name.
- **Local commits**:
  - `86766be4 feat(dashboard): support model filter on data dashboard` (classic UI + backend)
  - `a2bc1850 feat(default-dashboard): add model filter to dashboard analytics` (default UI)
- **Touched files**:
  - Backend: `model/usedata.go` (`GetDistinctModelNames`, `model_name` filter
    on `GetAllQuotaDates` / `GetQuotaDataByUsername` / `GetQuotaDataByUserId`
    / `GetQuotaDataGroupByUser`), `controller/usedata.go`
    (`GetQuotaDataModels`), `router/api-router.go` (`GET /api/data/models`).
  - Classic UI: `web/classic/src/components/dashboard/modals/SearchModal.jsx`,
    `web/classic/src/hooks/dashboard/useDashboardData.js`,
    `web/classic/src/components/dashboard/index.jsx`.
  - Default UI: `web/default/src/features/dashboard/{api.ts,types.ts,constants.ts}`,
    `web/default/src/features/dashboard/lib/filters.ts`,
    `web/default/src/features/dashboard/components/models/models-filter-dialog.tsx`.
  - i18n: keys `"条件筛选"` / `"模型"` / `"全部模型"` (classic);
    `"Filter by model"` / `"Data Filters"` (default).
- **Upstream adoption check**:
  ```bash
  # If any of these show output, upstream may have added equivalent support.
  git log upstream/main -- model/usedata.go controller/usedata.go \
    | grep -iE "model_name|GetDistinctModelNames"
  git grep -n "model_name" upstream/main -- 'web/default/src/features/dashboard/*'
  ```

### 2. Classic UI dashboard — "条件筛选" labeled filter button

- **Why**: Classic UI header showed only a search icon, users did not realize
  the data filter dialog existed.
- **Local commit**: `733c2a86 add ui text`
- **Touched files**: `web/classic/src/components/dashboard/DashboardHeader.jsx`
  (text + button styling); 7 classic i18n locale files
  (`web/classic/src/i18n/locales/{en,zh-CN,zh-TW,fr,ja,ru,vi}.json`).
- **Upstream adoption check**:
  ```bash
  git grep -n "条件筛选\|Filter Dashboard\|search-modal" upstream/main \
    -- 'web/classic/src/components/dashboard/DashboardHeader.jsx'
  ```

### 3. Default-UI charts — distinct colors when series > theme palette

- **Why**: Theme exposes only 5 chart colors (`--chart-1` .. `--chart-5`).
  Upstream's `getVChartDefaultColors` and `processUserChartData` extend the
  palette with `colors[index % colors.length]`, so the 6th model/user is
  rendered with the **exact same color** as the 1st. With 10+ models or
  10+ top users the four model charts (distribution bar/area, pie, trend,
  ranking) and both user charts become unreadable.
- **Local commit**: `fd70fd71 fix(default-dashboard): avoid duplicate model/user colors when series > palette`
- **Touched files**: `web/default/src/features/dashboard/lib/charts.ts` only.
- **Fix approach**: new helpers `generateDistinctColors()` (golden-angle hue
  rotation + 4 saturation/lightness bands) and `expandPalette()`. The first N
  slots keep the brand theme colors (so the ≤5-series case is unchanged);
  beyond that the helpers emit non-colliding HSL colors. Used by both
  `getVChartDefaultColors` (model charts) and the `userColorRange` branch in
  `processUserChartData`.
- **Upstream adoption check**:
  ```bash
  # If upstream changes the palette-extension logic, compare diff.
  git log upstream/main --oneline -- web/default/src/features/dashboard/lib/charts.ts
  git show upstream/main:web/default/src/features/dashboard/lib/charts.ts \
    | grep -nE "index % .*\.length|generateDistinct|expandPalette|HSL|getVChartDefaultColors"
  ```
  If upstream introduces an equivalent palette generator (regardless of
  algorithm), drop the local `generateDistinctColors` / `expandPalette`
  helpers and adopt theirs.

### 4. Force-record client IP for every request log + IP filter on log search

- **Why**: Upstream gates IP recording on a per-user `record_ip_log` opt-in
  (default off, set in the user's notification preferences). Operators who
  need IP for audit / abuse / risk-control on every request can't get it
  unless every user toggles the switch. The log search API and UI also had
  no `ip` filter, so even when IP was recorded, admins could not search by
  it.
- **Local commits**:
  - `feat(log): force-record IP + add ip filter to log search`
  - `feat(log): restrict IP visibility and filter to root role`
- **Touched files**:
  - Backend: `common/constants.go` (new `ForceRecordIpLog` global),
    `common/init.go` (read `FORCE_RECORD_IP_LOG` env var),
    `model/log.go` (`RecordErrorLog` / `RecordConsumeLog` honour the global
    before consulting the per-user setting; `GetAllLogs` / `GetUserLogs`
    accept an `ip` arg with exact-match or wildcard `1.2.*` LIKE),
    `controller/log.go` (gates `?ip=` on `role >= RoleRootUser` for both
    `/api/log/` and `/api/log/self`; strips `Log.Ip` from the response for
    non-root callers and unconditionally for the token-auth
    `GetLogByKey` endpoint).
  - Default UI: `web/default/src/hooks/use-root.ts` (new `useIsRoot`
    hook, mirror of `useIsAdmin`),
    `web/default/src/features/usage-logs/types.ts` (`CommonLogFilters.ip`),
    `…/lib/filter.ts`, `…/lib/utils.ts`, `…/lib/columns.ts`
    (column factory now takes `isRoot`),
    `…/components/common-logs-filter-bar.tsx` (IP input wrapped in
    `{isRoot && …}`, `expandedFilterCount` only counts `ip` for root),
    `…/components/columns/common-logs-columns.tsx`
    (`useCommonLogsColumns` accepts `isRoot`, threads it into the details
    dialog, and renders a root-only `id: 'ip'` column with truncated
    monospace cell + tooltip + sensitive-visible masking),
    `…/components/dialogs/details-dialog.tsx`
    (`showAdminIp` simplified to `!!log.ip && isRoot`),
    `…/components/usage-logs-table.tsx` (call `useIsRoot()` and pass
    through), `web/default/src/routes/_authenticated/usage-logs/$section.tsx`
    (`ip` in search-params zod schema).
  - i18n: new key `"Filter by IP"` across all 6 default-UI locale files.
- **Behaviour**:
  - When `FORCE_RECORD_IP_LOG=true` is set in `.env`, every consume/error
    log row gets `c.ClientIP()` regardless of the user's preference. With
    `false` (default) the upstream behaviour is preserved exactly.
  - Viewing and filtering IPs is **root-only**. Non-root admins and
    regular users:
    1. don't see the "Filter by IP" input at all in the usage-logs UI,
    2. don't see an "IP Address" column in the log table,
    3. don't see the "IP Address" row in the log details dialog,
    4. receive log payloads with the `ip` field empty,
    5. can't bypass via URL: `?ip=` is silently ignored, and the field
       is stripped from the response.
  - Root sees: input filter accepting exact (`157.15.200.38`),
    suffix (`*.108.196`), prefix (`192.169.*`) or arbitrary wildcard
    (`192.169.*`) — the wildcard `*` is converted to a SQL `LIKE %`.
    The `logs.ip` column already has a DB index, so filtering is fast.
- **Privacy note**: Forcing IP recording is a deliberate operator decision;
  document it in your privacy policy before flipping the switch. Even with
  recording on, only the operator (root) can read IPs through the UI/API.
- **Upstream adoption check**:
  ```bash
  # If upstream adds an operator-level IP recording toggle or an ip filter,
  # both can be dropped.
  git log upstream/main --oneline -- model/log.go controller/log.go \
    common/constants.go common/init.go \
  | grep -iE "record_ip_log|RecordIpLog|FORCE_RECORD_IP|filter.*ip|ip.*filter"
  git grep -n '"ip"' upstream/main -- controller/log.go model/log.go
  git grep -n "filter.*ip\\|placeholder.*[Ii][Pp]" upstream/main \
    -- 'web/default/src/features/usage-logs/*'
  ```
  If upstream lands an equivalent operator toggle and an `ip` query
  parameter, drop the local diff and switch to the upstream knob.

### 5. MySQL USE INDEX hint for log queries filtered by username

- **Why**: On large `logs` tables where one user holds most of the rows
  (e.g. a load-test or migration account producing ~50 % of the data),
  MySQL's cost-based optimizer mis-estimates the composite username
  index as more expensive than a full table scan. A 24-hour
  `SumUsedQuota` runs in ~3 s with the index but ~40 s without — and
  worse, the optimizer keeps picking the slow plan even after
  `ANALYZE TABLE`. The log-stat endpoint fires on every visit to the
  usage-logs page, so the regression is user-visible (page hangs for
  ~75 s).
- **Operator prerequisite**: composite indexes must exist on `logs`. Run
  in production via:
  ```sql
  ALTER TABLE logs
    ADD INDEX idx_user_created (user_id, created_at),
    ALGORITHM=INPLACE, LOCK=NONE;
  ALTER TABLE logs
    ADD INDEX idx_username_created_type (username, created_at, type),
    ALGORITHM=INPLACE, LOCK=NONE;
  ```
  Upstream auto-migration does NOT create these indexes. New deployments
  need to run them manually.
- **Local commit**: `fix(log): use composite index hint on MySQL for
  username-filtered log queries`.
- **Touched files**: `model/log.go` only.
- **Behaviour**:
  - New helper `logsTableExprForUsername(username)`. Returns
    `"logs USE INDEX (idx_username_created_type)"` if MySQL is the
    active dialect AND a non-empty `username` is being filtered;
    otherwise returns `"logs"`.
  - `SumUsedQuota` and `GetAllLogs` use it when constructing their
    GORM `Table(...)` expression. SQLite / PostgreSQL paths are
    untouched (those dialects don't understand `USE INDEX` syntax and
    don't suffer the optimizer mis-estimate).
- **Upstream adoption check**:
  ```bash
  # If upstream rewrites the stat queries (e.g. adds materialized
  # aggregates, a separate stats table, or its own hint mechanism) the
  # local hint may become redundant.
  git log upstream/main --oneline -- model/log.go \
  | grep -iE "stat|index|hint|sumused|optimizer"
  git grep -n "USE INDEX\|FORCE INDEX" upstream/main -- model/log.go
  ```
  If upstream switches to a materialized aggregate or a different
  schema-level fix that obviates the hint, drop the local helper.

### 6. Multi-header fallback for `upstream_request_id`

- **Why**: Upstream's relay layer only reads `X-Oneapi-Request-Id` from
  the upstream response when populating the `upstream_request_id` log
  column. That header is set only when the upstream is itself another
  new-api instance. Real providers (Anthropic, OpenAI, Azure) and edge
  proxies (Cloudflare) use different header names, so the column stayed
  empty for >99% of production traffic, making post-hoc reconciliation
  with upstream invoices impossible.
- **Local commit**: `fix(log): try multiple upstream trace headers when
  populating upstream_request_id`.
- **Touched files**: `relay/channel/api_request.go` only.
- **Behaviour**: After the upstream response arrives, try
  `X-Oneapi-Request-Id`, `X-Request-Id`, `Request-Id`, `Cf-Ray`,
  `Apim-Request-Id` in order; first non-empty value wins. No DB schema
  change required — `upstream_request_id` is already `VARCHAR(128)` with
  an index, easily fits all known trace-id formats (longest UUID is
  ~36 chars).
- **Upstream adoption check**:
  ```bash
  git grep -n "Cf-Ray\|X-Request-Id\|Request-Id\|Apim-Request-Id" \
    upstream/main -- relay/channel/api_request.go
  ```
  If upstream picks up the same fallback chain (or implements an
  equivalent), drop the local diff.

### 7. `is_stream` query filter on log search APIs

- **Why**: Operators frequently need to scope log queries to streaming
  or non-streaming requests separately when triaging timeout/abort
  patterns (streaming has different lifecycle and failure modes). The
  underlying column already exists; only the query/filter plumbing
  was missing.
- **Local commit**: same as §6 — single commit `feat(log): add
  is_stream filter ...`.
- **Touched files**:
  - Backend: `model/log.go` adds `parseIsStreamFilter()` helper and
    threads an `isStream` arg into `GetAllLogs` / `GetUserLogs`.
    `controller/log.go` reads `?is_stream=` from the query string.
    Accepted values: `""`/`"all"` (no filter), `"stream"`/`"1"`,
    `"non_stream"`/`"0"`.
  - Default UI: `web/default/src/features/usage-logs/types.ts` adds
    `isStream` to `CommonLogFilters`; `lib/filter.ts` and `lib/utils.ts`
    propagate the value; `routes/.../$section.tsx` widens the search
    schema; `components/common-logs-filter-bar.tsx` renders a new
    Select (`All Modes` / `Stream Only` / `Non-Stream Only`) next to
    the existing IP input.
  - i18n: 3 new keys (`All Modes`, `Stream Only`, `Non-Stream Only`)
    in all 6 default-UI locale files.
- **Upstream adoption check**:
  ```bash
  git grep -n "is_stream" upstream/main -- model/log.go controller/log.go
  git grep -n "isStream" upstream/main -- \
    'web/default/src/features/usage-logs/*'
  ```

### 8. Drain upstream on client_gone so billing matches the provider

- **Why**: When a downstream client disconnects mid-stream, upstream's
  scanner immediately returns and the `defer resp.Body.Close()` kills
  the upstream TCP connection. Providers (Anthropic, OpenAI, Azure) do
  NOT stop their LLM inference when a downstream TCP socket goes away
  — the GPU job keeps running until completion and the operator is
  billed for the FULL output. Meanwhile new-api only records the
  partial tokens the client received before disconnecting, so the
  fork's DB-side billing is systematically less than the upstream
  invoice. On busy traffic this is a real, ongoing P&L leak —
  observed ~2750 client_gone-with-ct=0 rows in a single day on this
  deployment, each leaking the full output's worth of tokens.
- **Local commit**: `feat(stream): drain upstream after client_gone to
  match provider billing`.
- **Touched files**:
  - `common/constants.go` (new `StreamDrainOnClientGone` flag) +
    `common/init.go` (read `STREAM_DRAIN_ON_CLIENT_GONE` env var,
    default false so behaviour only changes when operator opts in).
  - `relay/common/stream_status.go`: new `ClientGoneDetected`,
    `ClientGoneAtChunks`, `ClientGoneError` fields + idempotent
    `MarkClientGone(atChunks, err)` helper. Independent of EndReason.
  - `relay/helper/stream_scanner.go`: in both the scanner loop and the
    main waiting select, when drain is on, observe client_gone once
    via a one-shot `clientGoneCh` (nil'd after first observation so
    the always-signaled `Context.Done()` channel does not starve the
    other cases), record it, and KEEP READING. The scanner naturally
    exits on the real upstream EOF / `[DONE]` / streaming timeout, so
    `info.Usage` ends up populated from the full upstream stream.
    `dataHandler` writes to a dead client socket silently fail (Go's
    http server tolerates `EPIPE`), and the verified providers
    (Claude / OpenAI) only call `sr.Stop()` on PARSE errors, not on
    write errors — so the drain is safe across the standard adapters.
  - `service/log_info_generate.go`: emit
    `stream_status.client_gone = true` and
    `stream_status.client_gone_at_chunks = N` whenever
    `ClientGoneDetected` is set, independent of EndReason. So a typical
    log row in drain mode looks like:
    ```json
    "stream_status": {
      "status": "ok",
      "end_reason": "eof",
      "client_gone": true,
      "client_gone_at_chunks": 142
    }
    ```
    while billing reflects the full upstream output.
- **Operational caveats**:
  - Default is OFF (`STREAM_DRAIN_ON_CLIENT_GONE=false`). Operators
    must opt in explicitly because draining ties up the goroutine
    and upstream conn for the full inference duration even after the
    client is gone — that costs concurrency. Worthwhile only if the
    provider really bills for the full output (Anthropic, OpenAI,
    Bedrock all do).
  - Streaming timeout (`STREAMING_TIMEOUT`, default 300s) still
    applies — if the upstream itself hangs after client_gone, the
    drain is cut off and `EndReason=timeout` takes over.
  - User charged amount is now consistent with upstream invoice, so
    the user-visible spend on a "I cancelled after 2 seconds" request
    may look surprisingly high. Document this behaviour to end users
    (see the customer-facing explanation drafted alongside this
    change in chat notes).
- **Upstream adoption check**:
  ```bash
  # If upstream ever ships native drain support, drop this fork patch.
  git log upstream/main --oneline -- relay/helper/stream_scanner.go \
    relay/common/stream_status.go \
  | grep -iE "drain|client.gone|continue.*read|bill.*match"
  git grep -n "MarkClientGone\|StreamDrainOnClientGone\|ClientGoneDetected" \
    upstream/main
  ```

#### ⚠ Operational trap — DO NOT delete `idx_logs_username`

The `Log` struct in `model/log.go` declares `Username string
\`gorm:"index;..."\``. GORM's AutoMigrate runs on every container
start and **automatically recreates** any indexes declared on the
struct that are missing from the live schema. On the production
~10 M-row `logs` table, recreating `idx_logs_username` takes
**~130 seconds** and blocks `/api/status`, marking the container
unhealthy during the rebuild.

Rules of thumb:
- **Do not** `ALTER TABLE logs DROP INDEX idx_logs_username` on any
  environment that has accumulated significant log volume.
- The single-column index can safely coexist with
  `idx_username_created_type` (~1.5 GB extra space). The Go-side
  `USE INDEX` hint above forces the correct index for the hot path
  regardless of which indexes are present.
- If you ever need to drop it (e.g. disk pressure), also strip the
  `index` tag from the `Username` struct field in the same commit;
  otherwise the next container start will silently rebuild it.

The compose file's healthcheck has `start_period: 180s` to make
this scenario survivable: if the index ever does need to rebuild,
the container won't be reported unhealthy and traffic-routing /
orchestrator behaviour stays normal during the migration.

---

## Tooling / packaging customizations (kept regardless of upstream)

These are not upstream-mergeable — they describe **how this fork is built and
deployed**. They should remain even if upstream adds similar tooling.

- `.dockerignore` — extended for the upstream `web/{classic,default}` split
  plus secrets/runtime data exclusion.
- `docker-compose.build.yml` — dedicated build-only compose file with the
  `build-only` profile, only used to publish to Docker Hub.
- `scripts/build.sh` — hardcodes `DOCKERHUB_USER` / `DOCKERHUB_REPO` /
  `APP_VERSION` and writes the version into the `VERSION` file before
  invoking the build. `APP_VERSION` should be bumped to the latest upstream
  release tag on every upstream merge.
- `deploy/docker-compose.prod.yml` — tracked copy of the **production**
  compose file used on the RDS-backed deployment (nexapi.org). The root
  `docker-compose.yml` is the upstream demo template with bundled postgres
  + redis containers and is NOT what production uses. Production keeps its
  own copy at `~/new-api-docker/docker-compose.yml` on the host; sync it
  with `deploy/docker-compose.prod.yml` whenever either changes. See
  `deploy/README.md` for the workflow.

---

## How to use this file in a merge

```bash
git fetch upstream
git checkout main
BACKUP="backup/pre-upstream-merge-$(date +%Y%m%d-%H%M%S)"
git branch "$BACKUP"
git merge upstream/main

# Open FORK-CHANGES.md, walk through every entry under "Active local
# customizations", run its "Upstream adoption check" command.
# For each entry where upstream now solves the problem:
#   - revert the local commit referenced in the entry
#   - delete that entry from this file
#   - commit with message: "chore(fork): drop local <X>, adopted upstream"

# Always bump APP_VERSION in scripts/build.sh to the latest upstream tag:
git tag --sort=-creatordate | head -5
```
