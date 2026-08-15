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

> **Recent upstream merges:**
>
> - **2026-08-15 → upstream rc.24** (37 commits). Adoption check: **none of the
>   active fork entries were adopted upstream — all retained.** The merge itself
>   was conflict-free. One upstream defect had to be fixed locally: upstream
>   bumped `dompurify` 3.4.11 → 3.4.13 in `web/package.json` **without
>   regenerating `web/bun.lock`** (their lockfile still pins 3.4.11), which
>   breaks `bun install --frozen-lockfile` — used by `Dockerfile:5` and every
>   CI workflow, so the production image build fails. Fixed by regenerating
>   `web/bun.lock`. Re-check on the next merge: if upstream ships its own
>   lockfile fix, drop ours in favour of theirs.
>
> - **2026-08-05 → upstream rc.23** (69 commits). **Breaking upstream change:
>   the frontend was restructured** — `web/classic/` was deleted outright and
>   the default frontend was promoted from `web/default/` to `web/` (bun
>   workspace collapsed to a single package). Adoption check: **none of the
>   remaining fork entries were adopted upstream — all retained**; git's rename
>   detection carried §1/§3/§4/§7/§9 into `web/src/` automatically. **§2 was
>   retired** (its host, the classic UI, no longer exists upstream and
>   production had already stopped serving it). Other churn: §9's stream-status
>   section adopted upstream's `84834eee8 feat(logs): expose stream status to
>   log owners` (dropped the `props.isAdmin` gate) while keeping the fork's
>   `client_gone` display condition and disconnect detail rows; `.dockerignore`
>   updated for the single-package layout. All `web/default/` paths in this
>   file were rewritten to `web/`.
>
> - **2026-06-24 → upstream rc.14** (86 commits: ClickHouse log DB, dashboard
>   traffic-flow sankey, compact `<Dialog>` wrapper, log-deletion refactor,
>   audit auth-method tracking). Adoption check: **none of the 9 fork entries
>   were adopted upstream — all retained.** Notable churn that required
>   re-applying fork code on top of upstream refactors: §1 (dashboard sankey +
>   aggregated quota queries + new Dialog wrapper), §3 (`getVChartDefaultColors`
>   → `getDashboardChartColors`), §5 (`common.UsingMySQL` removed → log-DB-type
>   flag), §7 (filter-bar `CommonLogDraft` rework), §9 (details-dialog +
>   columns restructure). The §8 drain-mode feature was already dropped in the
>   prior rc.11 merge.

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
  - Default UI: `web/src/features/dashboard/{api.ts,types.ts,constants.ts}`,
    `web/src/features/dashboard/lib/filters.ts`,
    `web/src/features/dashboard/components/models/models-filter-dialog.tsx`.
  - i18n: keys `"条件筛选"` / `"模型"` / `"全部模型"` (classic);
    `"Filter by model"` / `"Data Filters"` (default).
- **Upstream adoption check**:
  ```bash
  # If any of these show output, upstream may have added equivalent support.
  git log upstream/main -- model/usedata.go controller/usedata.go \
    | grep -iE "model_name|GetDistinctModelNames"
  git grep -n "model_name" upstream/main -- 'web/src/features/dashboard/*'
  ```

### 2. ~~Classic UI dashboard — "条件筛选" labeled filter button~~ (RETIRED — rc.23)

- **Status**: **Removed in the rc.23 merge (2026-08-05).** Upstream deleted the
  entire classic frontend and promoted the default frontend from `web/default/`
  to `web/` (bun workspace collapsed from multi-package to a single package).
  With `web/classic/` gone upstream, this customization has no host file left;
  production had already stopped serving the classic UI, so it was dropped
  rather than maintained as a fork-only frontend.
- **What was removed**: `web/classic/` in its entirety (the 12 conflicted files
  were resolved by accepting upstream's deletion), including
  `DashboardHeader.jsx` and the 7 classic i18n locale files.
- **Numbering**: section number **2** is retained (not renumbered), consistent
  with [[§5]]; code comments elsewhere reference the fork numbering.

### 3. Default-UI charts — distinct colors when series > theme palette

- **Why**: Theme exposes only 5 chart colors (`--chart-1` .. `--chart-5`).
  Upstream's `getVChartDefaultColors` and `processUserChartData` extend the
  palette with `colors[index % colors.length]`, so the 6th model/user is
  rendered with the **exact same color** as the 1st. With 10+ models or
  10+ top users the four model charts (distribution bar/area, pie, trend,
  ranking) and both user charts become unreadable.
- **Local commit**: `fd70fd71 fix(default-dashboard): avoid duplicate model/user colors when series > palette`
- **Touched files**: `web/src/features/dashboard/lib/charts.ts` only.
- **Fix approach**: new helpers `generateDistinctColors()` (golden-angle hue
  rotation + 4 saturation/lightness bands) and `expandPalette()`. The first N
  slots keep the brand theme colors (so the ≤5-series case is unchanged);
  beyond that the helpers emit non-colliding HSL colors. Used by both
  `getDashboardChartColors` (model charts; upstream renamed this from
  `getVChartDefaultColors` in rc.14) and the `userColorRange` branch in
  `processUserChartData`.
- **Upstream adoption check**:
  ```bash
  # If upstream changes the palette-extension logic, compare diff.
  git log upstream/main --oneline -- web/src/features/dashboard/lib/charts.ts
  git show upstream/main:web/src/features/dashboard/lib/charts.ts \
    | grep -nE "index % .*\.length|generateDistinct|expandPalette|HSL|getDashboardChartColors"
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
  - Default UI: `web/src/hooks/use-root.ts` (new `useIsRoot`
    hook, mirror of `useIsAdmin`),
    `web/src/features/usage-logs/types.ts` (`CommonLogFilters.ip`),
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
    through), `web/src/routes/_authenticated/usage-logs/$section.tsx`
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
    -- 'web/src/features/usage-logs/*'
  ```
  If upstream lands an equivalent operator toggle and an `ip` query
  parameter, drop the local diff and switch to the upstream knob.

### 5. ~~MySQL USE INDEX hint for log queries filtered by username~~ (RETIRED — rc.21)

- **Status**: **Removed in the rc.21 merge (2026-07-14).** Production moved its
  log database to ClickHouse (`LOG_SQL_DSN=clickhouse://…`). The MySQL optimizer
  mis-estimation this hint worked around does not exist on ClickHouse, and
  ClickHouse does not accept `USE INDEX` syntax. The helper was already gated to
  MySQL-only (`UsingLogDatabase(MySQL)`), so it was already a no-op on ClickHouse;
  with no MySQL log DB in use, the whole mechanism became dead code and was dropped.
- **What was removed**: the `logsTableExprForUsername(username)` helper in
  `model/log.go` and its two call sites in `GetAllLogs` / `SumUsedQuota`, which
  now use plain `LOG_DB.Table("logs")` (matching upstream). Upstream's
  `COALESCE(sum(...), 0)` NULL-guards in `SumUsedQuota` are preserved.
- **Orphaned MySQL indexes**: the manually-created `idx_username_created_type`
  (username, created_at, type) and `idx_user_created` (user_id, created_at) on
  any legacy MySQL `logs` table are no longer referenced by code. They are
  harmless if left; drop at leisure. ClickHouse handles these queries via its
  `(created_at, request_id)` primary key + monthly partitioning — see §4/§8 notes
  and the ClickHouse log store.
- **Numbering**: section number **5** is retained (not renumbered); code comments
  and later sections reference the fork numbering, so §6–§9 keep their numbers.

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
  - Default UI: `web/src/features/usage-logs/types.ts` adds
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
    'web/src/features/usage-logs/*'
  ```

### 8. Annotate logs with client disconnect timing (stream + non-stream)

- **Why**: Support reconciliation needs to know *when* a client
  abandoned a request — not just *that* it did. For streams, the bare
  `end_reason='client_gone'` boolean can't answer "did the user get 2
  tokens or 200 before bailing?", which is the first question every
  refund / invoice ticket asks. For non-streaming, upstream's
  `http.NewRequest` uses `context.Background()` so `client.Do` finishes
  the call regardless of the downstream socket state — without
  annotation, the log row gives zero hint that the client wasn't
  around to read the answer.
- **Local commit**: `feat(log): record client-disconnect timing in
  stream and non-stream paths`.
- **Touched files**:
  - `relay/common/stream_status.go`: `MarkClientGone` now also takes
    an `atMs int64` arg and stores it in `ClientGoneAtMs`.
  - `relay/helper/stream_scanner.go`: small `elapsedMs(info)` helper
    derives the value from `info.StartTime`; passed at the
    MarkClientGone call site.
  - **Upstream churn note (rc.18 merge, 2026-07-07)**: upstream
    `153d7f01a fix: avoid stale stream writes after client disconnect
    (#5710)` rewrote this file (~137 lines): goroutine-lifecycle fix
    (unconditional `wg.Wait` via `cleanup()`, close `resp.Body` in
    cleanup), a bounded per-write `streamWriteTimeout`, and — matching
    this fork's own §8/dropped-drain conclusion — it **independently
    dropped drain-on-disconnect** (client gone ⇒ immediate cleanup, no
    billing for post-disconnect tokens). The scanner-loop no longer has
    its own client-disconnect case (upstream handles it via the main
    select + cleanup). The fork's `MarkClientGone(...)` telemetry was
    re-applied to the **main-loop** `c.Request.Context().Done()` case
    only (the single-shot select replaces the old `waitLoop`), where
    `info.ReceivedResponseCount` still reflects chunks delivered at
    disconnect.
  - `relay/common/relay_info.go`: new top-level `ClientDisconnected`
    + `ClientDisconnectedAtMs` fields on RelayInfo. Independent of
    stream/non-stream.
  - `relay/compatible_handler.go`: after `adaptor.DoResponse` returns
    (i.e. when the upstream is fully consumed), check
    `c.Request.Context().Err() != nil` and populate the RelayInfo
    fields. Works for both streaming and non-streaming code paths.
  - `service/log_info_generate.go`: new `appendClientDisconnectStatus`
    emits `other.client_disconnected = true` and
    `other.client_disconnected_at_ms = N` unconditionally when the
    flag is set. Streaming requests additionally get
    `stream_status.client_gone_at_ms` (the stream-scoped version),
    which is slightly redundant but lets a single log row be read
    self-sufficiently regardless of which code path produced it.
- **Effect on log row**:
  - **Non-stream** + client disappeared mid-wait: `other` gains
    `client_disconnected: true, client_disconnected_at_ms: 8420`.
    Billing fields reflect the full upstream response (provider
    finished generating before we knew the client was gone).
  - **Stream** + client_gone: `stream_status` carries
    `{end_reason: "client_gone", status: "error", client_gone: true,
    client_gone_at_chunks: 142, client_gone_at_ms: 12480}`. Billing
    reflects what the client received before disconnecting (matches
    upstream invoice — providers stop billing the moment our HTTP
    connection closes). The `client_gone_at_*` fields let support
    answer "how much did they actually get" without re-running the
    request.
  - **Frontend rendering**: §9 makes the log-details dialog surface
    these fields whenever `client_gone` (or top-level
    `client_disconnected`) is true, independent of `status`. An amber
    `Unplug` icon also appears in the timing column.
- **Upstream adoption check**:
  ```bash
  git grep -n "ClientDisconnected\|client_disconnected_at_ms\|ClientGoneAtMs" \
    upstream/main
  git log upstream/main --oneline -- relay/compatible_handler.go \
    relay/common/relay_info.go service/log_info_generate.go \
  | grep -iE "client.disconnect|client.gone|drain|abort"
  ```

### 9. Surface client_gone / client_disconnected in log details UI

- **Why**: §8 makes the backend record disconnect timing on every
  affected log row, but the default-UI details dialog (`details-
  dialog.tsx`) only revealed `stream_status` when `status !== 'ok'`.
  That gate is correct for upstream-side errors but hides the
  client-gone marker when a stream cleanly ends after the client had
  already disconnected (rare but observed in practice — e.g. when
  upstream finished generating before noticing the dead socket).
  Operators reading log details for refund / reconciliation tickets
  need to see the disconnect fields independent of the upstream-side
  status.
- **Local commit**: `feat(ui): surface client_gone / client_disconnected
  in log details` (5b4a3fbb5).
- **Touched files** (all under `web/src/features/usage-logs/`):
  - `types.ts`: extend `LogOtherData.stream_status` with
    `client_gone`, `client_gone_at_chunks`, `client_gone_at_ms`,
    `client_gone_error`; add top-level `client_disconnected` and
    `client_disconnected_at_ms` on `LogOtherData`. All optional.
  - `components/dialogs/details-dialog.tsx`: open the Stream Status
    section whenever `client_gone === true` (in addition to the
    existing `status !== 'ok'` trigger). Section header switches
    between danger (red) and default styling based on `status`.
    Renders `Client Disconnected: Yes` + `Disconnect at Chunks` +
    `Disconnect at` (formatted via `formatUseTime`) + optional
    `Disconnect Error`. Adds a separate `Client Disconnect` section
    for non-stream rows that carry top-level `client_disconnected`.
  - `components/columns/common-logs-columns.tsx`: in the Timing column
    cell, add an amber `Unplug` tooltip whenever either
    `stream_status.client_gone` or top-level `client_disconnected`
    is true. Coexists with the existing red `CircleAlert` (gated on
    `stream_status.status !== 'ok'`) — both can appear on the same
    row, conveying "stream errored AND client was already gone".
- **Effect**: Historical rows automatically benefit — every
  `end_reason='client_gone'` row now shows disconnect timing in the
  UI without DB migration.
- **Upstream churn note (rc.21 merge, 2026-07-14)**: upstream refactored the
  timing/stream cell into `<TimingMetricsCell>` + `<StreamTpsCell>`
  (`timing-metrics-cell.tsx`) and moved the red `CircleAlert` stream-error mark
  *inside* `StreamTpsCell`. The §9 amber `Unplug` client-disconnect indicator
  was re-grafted as a sibling next to `<StreamTpsCell>` in the is_stream cell
  (adopting upstream's component instead of the old inline JSX); the inline
  `CircleAlert` was dropped since `StreamTpsCell` now renders it. Coexistence
  with the error mark still holds. Separately, §5 (MySQL `USE INDEX`) was retired
  this merge — logs now live in ClickHouse (see §5).
- **Upstream adoption check**:
  ```bash
  # Skip this entry if upstream adopts both the backend annotation
  # (§8) and a frontend that renders client_gone / client_disconnected
  # independently of stream_status.status.
  git grep -n "client_gone\|client_disconnected" upstream/main \
    -- 'web/src/**'
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
