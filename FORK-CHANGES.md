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
