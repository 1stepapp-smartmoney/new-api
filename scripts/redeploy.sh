#!/usr/bin/env bash
#
# 在东京 + us-west-2 两台机器上重新 pull 镜像并滚动拉起所有 new-api 实例。
#
# 用法：
#   ./scripts/redeploy.sh            全部三个实例
#   ./scripts/redeploy.sh tokyo      仅东京
#   ./scripts/redeploy.sh usw2       仅美西两站
#
# 前置条件：本机 ssh 能免密登录目标主机，且目标主机上的 sudo 无需交互输入密码。
# 通常流程：先 ./scripts/build.sh 构建并推送镜像，再执行本脚本让各站拉取新镜像。
#
# 主机地址可用环境变量覆盖，便于换机或临时指向别的环境：
#   TOKYO_HOST=ubuntu@1.2.3.4 ./scripts/redeploy.sh tokyo
set -uo pipefail

TOKYO="${TOKYO_HOST:-ubuntu@54.64.192.229}"
USW2="${USW2_HOST:-ubuntu@52.41.195.28}"
TARGET="${1:-all}"
FAIL=0

case "$TARGET" in
  all|tokyo|usw2) ;;
  *)
    echo "ERROR: 未知目标 '$TARGET'（可选：all / tokyo / usw2）" >&2
    exit 2
    ;;
esac

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

[ "$TARGET" = all ] || [ "$TARGET" = tokyo ] && deploy "$TOKYO" '~/nexapi-docker' new-api 3000
[ "$TARGET" = all ] || [ "$TARGET" = usw2 ] && {
  deploy "$USW2" '~/nexapi-a' new-api-a 3000
  deploy "$USW2" '~/nexapi-b' new-api-b 3001
}

echo
[ "$FAIL" = 0 ] && echo "全部实例部署完成且健康" || { echo "有实例未通过检查,见上方输出"; exit 1; }
