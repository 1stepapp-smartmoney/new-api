#!/usr/bin/env bash
#
# new-api 容器被 OOM kill 时推送 Telegram 告警。
#
# 背景：2026-11-13 Tokyo 容器每 1~3 分钟被 OOM kill 一次（见 FORK-CHANGES 的
# rc.50 合并注记），docker 自动重启掩盖了它，直到看板与日志对不上才被发现。
# 这个守护进程监听 docker 的 oom 事件，第一时间把人叫醒。
#
# 用法：
#   oom-telegram-notify.sh          常驻监听（由 systemd 拉起，见 FORK-CHANGES Tooling）
#   oom-telegram-notify.sh --test   向全部推送目标发一条测试消息
#
# 凭证与 disk-alert 共用 /etc/nexapi-disk-alert.conf（root 只读，chmod 600）。
# 推送目标是 alert_bot（scripts/alert_bot.py）维护的订阅者清单
# /var/lib/nexapi-alert-bot/targets.txt，bot 未部署时回退到 TELEGRAM_CHAT_ID 单发。
# bot token 只经 curl 的 stdin 配置传入，不会出现在 ps 的命令行里。
#
# 可调参数（环境变量）：
#   OOM_ALERT_CONTAINER   监听的容器名，默认 new-api
#   OOM_ALERT_THROTTLE    两条告警的最小间隔（秒），默认 60；OOM 连环发生时不刷屏
set -u

CONF=/etc/nexapi-disk-alert.conf
TARGETS_FILE=/var/lib/nexapi-alert-bot/targets.txt
CONTAINER="${OOM_ALERT_CONTAINER:-new-api}"
THROTTLE="${OOM_ALERT_THROTTLE:-60}"

if [ -r "$CONF" ]; then
  # shellcheck disable=SC1090
  . "$CONF"
fi
HOST="$(hostname)"

send() {
  local text="$1" target listed body failed=0 total=0
  if [ -z "${TELEGRAM_BOT_TOKEN:-}" ]; then
    echo "ERROR: $CONF 未配置 TELEGRAM_BOT_TOKEN，告警未发出" >&2
    return 1
  fi
  # 清单只读一次：先判断「非空」再读是两次读，中间若被改写就会读到空清单。
  listed=$(cat "$TARGETS_FILE" 2>/dev/null)
  [ -n "$listed" ] || listed="${TELEGRAM_CHAT_ID:-}"
  while IFS= read -r target; do
    [ -z "$target" ] && continue
    total=$((total + 1))
    # URL 含 token，只走 stdin 配置；chat_id 与正文不是机密，走普通参数
    # （curl 配置文件逐行解析，多行正文放进去只会发出第一行）。
    if ! body=$(curl -sS --max-time 10 \
          --data-urlencode "chat_id=${target}" \
          --data-urlencode "text=${text}" \
          --config - <<CURLCFG
url = "https://api.telegram.org/bot${TELEGRAM_BOT_TOKEN}/sendMessage"
CURLCFG
    ); then
      echo "ERROR: 调用 Telegram 失败（网络或超时）target=${target}" >&2
      failed=$((failed + 1)); continue
    fi
    case "$body" in
      *'"ok":true'*) ;;
      *) echo "ERROR: Telegram 拒绝了这条消息 target=${target}：$body" >&2
         failed=$((failed + 1)) ;;
    esac
  done <<< "$listed"
  # 一个目标都没有等于告警没发出去，必须报错，不能当成功。
  [ "$total" -gt 0 ] || { echo "ERROR: 没有任何推送目标，告警未发出" >&2; return 1; }
  [ "$failed" -eq 0 ] || { echo "ERROR: ${failed}/${total} 个目标发送失败" >&2; return 1; }
}

if [ "${1:-}" = "--test" ]; then
  send "✅ oom-telegram-notify test from ${HOST}: broadcast path works."
  exit $?
fi

LAST=0
echo "watching container ${CONTAINER} for oom events..."
while :; do
  while read -r ts; do
    [ -z "$ts" ] && continue
    now=$(date +%s)
    if [ $((now - LAST)) -lt "$THROTTLE" ]; then
      echo "oom event at $ts throttled"; continue
    fi
    LAST=$now
    when=$(date -d @"$ts" '+%F %H:%M:%S %Z' 2>/dev/null || date -r "$ts" '+%F %H:%M:%S %Z')
    mem=$(docker stats "$CONTAINER" --no-stream --format '{{.MemUsage}}' 2>/dev/null || echo '?')
    send "🚨 OOM: container ${CONTAINER} @ ${HOST} killed at ${when} (exit 137, mem ${mem}).
Docker 已自动重启。注意：被杀瞬间的在途请求 upstream usage 会丢失（少计费），看板缓存最多丢 1 分钟。"
  done < <(docker events --filter "container=${CONTAINER}" --filter event=oom --format '{{.Time}}')
  echo "docker events stream ended; reconnecting in 5s"
  sleep 5
done
