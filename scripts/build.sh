#!/usr/bin/env bash
#
# 构建 & 推送 Docker 镜像到 Docker Hub
#
# 用法：
#   ./scripts/build.sh              # 构建并推送 <IMAGE_TAG> 和 latest
#   ./scripts/build.sh --no-push    # 只构建不推送（本地验证用）
#
# 第一次运行前请执行 docker login 登录 Docker Hub。
# 如果没有把当前用户加入 docker 组，请在整条命令前加 sudo，例如：
#   sudo ./scripts/build.sh
#
set -euo pipefail

# ====================================================================
# ↓↓↓ 以下变量请按需硬编码 ↓↓↓
# ====================================================================

# Docker Hub 用户名（必须小写）
DOCKERHUB_USER="zjlywjh001"

# Docker Hub 仓库名（你在网页端创建的 repository）
DOCKERHUB_REPO="nexapi"

# 应用版本号——**这个值会被后台「其他设置」页显示**，也会被注入到二进制（common.Version）
# 建议对齐上游最新 release（如 v0.10.0），以便内置的「检查更新」按钮能正确比对
# 规则：首字符必须是字母或数字；只能含 [A-Za-z0-9._-]
APP_VERSION="v1.0.0-rc.24"

# 镜像 tag——APP_VERSION 加 git short hash 后缀，避免同一版本号重复构建覆盖干净 tag
# 产物示例：zjlywjh001/nexapi:v0.12.14-ba9525e
# 首次某 APP_VERSION 发布时若想保留"干净 tag"（如 v0.12.14），可手动 docker tag + push。
IMAGE_TAG="${APP_VERSION}-$(git rev-parse --short HEAD 2>/dev/null || echo nogit)"

# 是否同时打 latest 标签并推送（true / false）
PUSH_LATEST="true"

# compose 文件路径（相对仓库根）
COMPOSE_FILE="docker-compose.build.yml"

# compose profile 名，需与 docker-compose.build.yml 中 profiles 字段一致
COMPOSE_PROFILE="build-only"

# ====================================================================
# ↑↑↑ 硬编码区 ↑↑↑  下方一般不用改
# ====================================================================

# 定位到仓库根（scripts/ 的上一级），以便脚本可在任意目录调用
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$REPO_ROOT"

# 校验 APP_VERSION 格式（首字符必须字母或数字；只允许 [A-Za-z0-9._-]）
validate_tag() {
  local name="$1" value="$2"
  if ! [[ "$value" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
    echo "ERROR: $name '$value' 不是合法 Docker tag" >&2
    echo "       首字符必须是字母或数字，只能含 [A-Za-z0-9._-]" >&2
    exit 1
  fi
}
validate_tag "APP_VERSION" "$APP_VERSION"
validate_tag "IMAGE_TAG"   "$IMAGE_TAG"

NO_PUSH="false"
for arg in "$@"; do
  case "$arg" in
    --no-push) NO_PUSH="true" ;;
    -h|--help)
      sed -n '2,12p' "$0"
      exit 0
      ;;
    *) echo "unknown arg: $arg" >&2; exit 2 ;;
  esac
done

IMAGE_REF="${DOCKERHUB_USER}/${DOCKERHUB_REPO}"

echo "============================================================"
echo " Repo root    : $REPO_ROOT"
echo " Compose file : $COMPOSE_FILE"
echo " Profile      : $COMPOSE_PROFILE"
echo " App version  : $APP_VERSION   (显示在后台 & common.Version)"
echo " Image tag    : $IMAGE_REF:$IMAGE_TAG"
echo " Also latest  : $PUSH_LATEST"
echo " Push         : $([ "$NO_PUSH" = "true" ] && echo no || echo yes)"
echo "============================================================"

# 关键：把 APP_VERSION 写入 VERSION 文件
#   Dockerfile:9  的 VITE_REACT_APP_VERSION=$(cat VERSION) 注入前端
#   Dockerfile:26 的 -X common.Version=$(cat VERSION)     注入后端
# 二者都会被 /api/status 吐给后台页面，实现「后台显示的版本号 = APP_VERSION」
echo "$APP_VERSION" > VERSION
echo ">>> Wrote VERSION file: $(cat VERSION)"

# 导出给 compose 文件中的 ${DOCKERHUB_USER} / ${IMAGE_TAG} 占位符使用
export DOCKERHUB_USER
export IMAGE_TAG

echo ">>> Building (profile=$COMPOSE_PROFILE)..."
docker compose -f "$COMPOSE_FILE" --profile "$COMPOSE_PROFILE" build --pull

echo ">>> Built images:"
docker images --format 'table {{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Size}}' \
  | grep -E "^(${IMAGE_REF}|REPOSITORY)" || true

if [ "$NO_PUSH" = "true" ]; then
  echo ">>> --no-push 模式，跳过推送。完成。"
  exit 0
fi

echo ">>> Pushing $IMAGE_REF:$IMAGE_TAG"
docker push "$IMAGE_REF:$IMAGE_TAG"

if [ "$PUSH_LATEST" = "true" ]; then
  echo ">>> Pushing $IMAGE_REF:latest"
  docker push "$IMAGE_REF:latest"
fi

echo "============================================================"
echo " Done."
echo "   App version : $APP_VERSION"
echo "   Image       : $IMAGE_REF:$IMAGE_TAG"
[ "$PUSH_LATEST" = "true" ] && echo "           and : $IMAGE_REF:latest"
echo "============================================================"
