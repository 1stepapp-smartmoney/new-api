#!/usr/bin/env bash
#
# 一条龙发布：构建机拉代码 → 编译并推送镜像 → 东京 + us-west-2 各站拉取新镜像并重建容器。
#
# 用法：
#   ./scripts/redeploy.sh            构建 + 全部三个实例
#   ./scripts/redeploy.sh build      仅在构建机上编译并推送镜像
#   ./scripts/redeploy.sh deploy     跳过构建，仅让三个实例重拉现有 latest
#   ./scripts/redeploy.sh tokyo      仅东京
#   ./scripts/redeploy.sh usw2       仅美西两站
#
# 前置条件：本机 ssh 能免密登录构建机与目标主机，各机 sudo 无需交互输入密码，
# 且构建机已 docker login 过 Docker Hub（build.sh 用 sudo -E 继承该登录态）。
#
# 主机地址可用环境变量覆盖，便于换机或临时指向别的环境：
#   TOKYO_HOST=ubuntu@1.2.3.4 ./scripts/redeploy.sh tokyo
set -uo pipefail

BUILD_HOST="${BUILD_HOST:-wjh@80.240.25.4}"
BUILD_DIR="${BUILD_DIR:-~/new-api}"
TOKYO="${TOKYO_HOST:-ubuntu@54.64.192.229}"
USW2="${USW2_HOST:-ubuntu@52.41.195.28}"
TARGET="${1:-all}"
FAIL=0

DO_BUILD=false
DO_TOKYO=false
DO_USW2=false
case "$TARGET" in
  all)    DO_BUILD=true; DO_TOKYO=true; DO_USW2=true ;;
  build)  DO_BUILD=true ;;
  deploy) DO_TOKYO=true; DO_USW2=true ;;
  tokyo)  DO_TOKYO=true ;;
  usw2)   DO_USW2=true ;;
  *)
    echo "ERROR: 未知目标 '$TARGET'（可选：all / build / deploy / tokyo / usw2）" >&2
    exit 2
    ;;
esac

# 在构建机上拉最新代码并编译推送镜像。build.sh 会把 APP_VERSION 写进 VERSION 文件，
# 所以每次构建后工作区都是脏的；先丢弃这一处改动，否则 git pull 会因本地修改被覆盖而拒绝。
build_image() {
  echo "──────── 构建镜像  ($BUILD_HOST:$BUILD_DIR) ────────"
  local log
  log=$(mktemp)
  ssh -o ConnectTimeout=10 "$BUILD_HOST" "set -e
    cd $BUILD_DIR
    git checkout -- VERSION 2>/dev/null || true
    git pull --ff-only
    echo \">>> HEAD: \$(git rev-parse --short HEAD) \$(git log -1 --pretty=%s)\"
    sudo -E scripts/build.sh
  " 2>&1 | tee "$log"
  local rc=${PIPESTATUS[0]}
  if [ "$rc" != 0 ]; then
    echo "  ✗ 构建失败，未推送镜像，终止发布"
    rm -f "$log"
    return 1
  fi
  local image
  image=$(sed -n 's/^ *Image *: *//p' "$log" | tail -1)
  rm -f "$log"
  echo "  ✓ 构建并推送完成${image:+：$image}"
}

# host  目录  容器名  本地探测端口
deploy() {
  local HOST=$1 DIR=$2 NAME=$3 PORT=$4
  echo "──────── $NAME  ($HOST:$DIR) ────────"
  ssh -o ConnectTimeout=10 "$HOST" "set -e
    cd $DIR
    BEFORE=\$(sudo docker inspect -f '{{.Image}}' $NAME 2>/dev/null || echo none)
    sudo docker compose pull -q
    AFTER=\$(sudo docker image inspect -f '{{.Id}}' \$(grep -oP '^\s+image:\s*\K\S+' docker-compose.yml | head -1))
    if [ \"\$BEFORE\" = \"\$AFTER\" ]; then echo '  镜像无变化,仍执行 up -d 以确保配置生效'; fi
    sudo docker compose up -d
  " || { echo "  ✗ $NAME 部署命令失败"; FAIL=1; return; }

  # 等待健康:AutoMigrate 在大表上可能耗时,给 240s
  echo -n "  等待健康 "
  for i in $(seq 1 48); do
    ST=$(ssh "$HOST" "sudo docker inspect -f '{{.State.Health.Status}}' $NAME 2>/dev/null" || echo unknown)
    case "$ST" in
      healthy) echo " ✓ healthy"; break ;;
      unhealthy) echo " ✗ unhealthy"; FAIL=1; break ;;
      *) printf "." ; sleep 5 ;;
    esac
    [ "$i" = 48 ] && { echo " ✗ 超时(仍为 $ST)"; FAIL=1; }
  done

  OK=$(ssh "$HOST" "curl -s -m 10 http://127.0.0.1:$PORT/api/status | grep -o '\"success\":true'" || true)
  if [ -n "$OK" ]; then echo "  ✓ /api/status 正常"; else
    echo "  ✗ /api/status 异常,最后 20 行日志:"; FAIL=1
    ssh "$HOST" "sudo docker logs --tail 20 $NAME 2>&1" | sed 's/^/    /'
  fi
}

if [ "$DO_BUILD" = true ]; then
  build_image || exit 1
fi

if [ "$DO_TOKYO" = true ]; then
  deploy "$TOKYO" '~/nexapi-docker' new-api 3000
fi

if [ "$DO_USW2" = true ]; then
  deploy "$USW2" '~/nexapi-a' new-api-a 3000
  deploy "$USW2" '~/nexapi-b' new-api-b 3001
fi

echo
[ "$FAIL" = 0 ] && echo "全部实例部署完成且健康" || { echo "有实例未通过检查,见上方输出"; exit 1; }
