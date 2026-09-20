#!/usr/bin/env bash
#
# 磁盘用量告警 → Telegram 机器人。
#
# 背景：2026-09-18 ClickHouse 数据盘写满，三个站点的计费日志静默中断两天才被发现
# （见 FORK-CHANGES §11）。这个脚本负责在写满**之前**把人叫醒。
#
# 用法：
#   ./scripts/disk-alert.sh                 检查所有本地文件系统，超阈值则推送
#   ./scripts/disk-alert.sh /var/lib/clickhouse /   只检查指定挂载点
#   ./scripts/disk-alert.sh --test          发一条测试消息，验证 token / chat_id 配好了
#   ./scripts/disk-alert.sh --dry-run       只打印会发什么，不真的推送
#
# 配置：把凭证写进 /etc/nexapi-disk-alert.conf（root 只读，chmod 600），格式：
#   TELEGRAM_BOT_TOKEN=123456:AA...
#   TELEGRAM_CHAT_ID=-1001234567890
# 也可以直接用同名环境变量覆盖。凭证只从文件/环境读取，脚本不会把它打印出来，
# 也不会出现在 ps 的命令行里（curl 通过 stdin 配置读 URL）。
#
# 可调参数（环境变量）：
#   DISK_ALERT_THRESHOLD   触发阈值百分比，默认 90
#   DISK_ALERT_REPEAT_H    持续超标时的重复提醒间隔（小时），默认 6，避免刷屏
#   DISK_ALERT_CONF        配置文件路径，默认 /etc/nexapi-disk-alert.conf
#   DISK_ALERT_STATE       状态文件路径，默认 /var/lib/nexapi-disk-alert.state
#
# 建议用 systemd timer 每 5 分钟跑一次（安装步骤见 FORK-CHANGES 的 Tooling 章节）。
set -uo pipefail

THRESHOLD="${DISK_ALERT_THRESHOLD:-90}"
REPEAT_H="${DISK_ALERT_REPEAT_H:-6}"
CONF="${DISK_ALERT_CONF:-/etc/nexapi-disk-alert.conf}"
STATE="${DISK_ALERT_STATE:-/var/lib/nexapi-disk-alert.state}"

MODE="check"
MOUNTS=()
for arg in "$@"; do
  case "$arg" in
    --test)    MODE="test" ;;
    --dry-run) MODE="dry-run" ;;
    -h|--help) sed -n '2,30p' "$0"; exit 0 ;;
    -*)        echo "ERROR: 未知参数 '$arg'" >&2; exit 2 ;;
    *)         MOUNTS+=("$arg") ;;
  esac
done

if [ -r "$CONF" ]; then
  # shellcheck disable=SC1090
  . "$CONF"
fi
TOKEN="${TELEGRAM_BOT_TOKEN:-}"
CHAT="${TELEGRAM_CHAT_ID:-}"

if [ "$MODE" != "dry-run" ] && { [ -z "$TOKEN" ] || [ -z "$CHAT" ]; }; then
  echo "ERROR: 未配置 TELEGRAM_BOT_TOKEN / TELEGRAM_CHAT_ID（找过 $CONF 和环境变量）" >&2
  exit 3
fi

# 把 token 放进 curl 的 stdin 配置而不是命令行参数，避免它出现在 ps / 进程列表里。
send_telegram() {
  local text="$1"
  if [ "$MODE" = "dry-run" ]; then
    printf '[dry-run] 本应推送：\n%s\n' "$text"
    return 0
  fi
  local body
  if ! body=$(curl -sS --max-time 20 --config - <<CURLCFG
url = "https://api.telegram.org/bot${TOKEN}/sendMessage"
data-urlencode = "chat_id=${CHAT}"
data-urlencode = "text=${text}"
data-urlencode = "disable_web_page_preview=true"
CURLCFG
  ); then
    echo "ERROR: 调用 Telegram 失败（网络或超时）" >&2
    return 1
  fi
  case "$body" in
    *'"ok":true'*) return 0 ;;
    # 失败原因里可能带回 chat_id，但不会带 token；原样输出便于排查配错的 chat。
    *) echo "ERROR: Telegram 拒绝了这条消息：$body" >&2; return 1 ;;
  esac
}

HOST="$(hostname -f 2>/dev/null || hostname)"
NOW="$(date -u '+%F %T UTC')"

if [ "$MODE" = "test" ]; then
  send_telegram "✅ nexapi 磁盘告警测试
主机：${HOST}
时间：${NOW}
阈值：${THRESHOLD}%
收到这条说明 bot token 与 chat_id 都配好了。"
  exit $?
fi

# 未显式指定挂载点时，自动发现本地真实文件系统（排除 snap/loop/tmpfs 等噪声）。
if [ "${#MOUNTS[@]}" -eq 0 ]; then
  while read -r fstype mountpoint; do
    case "$fstype" in
      ext2|ext3|ext4|xfs|btrfs) MOUNTS+=("$mountpoint") ;;
    esac
  done < <(df -PT 2>/dev/null | awk 'NR>1 {print $2, $7}')
fi

if [ "${#MOUNTS[@]}" -eq 0 ]; then
  echo "ERROR: 没有可检查的挂载点" >&2
  exit 4
fi

# 状态文件记录每个挂载点上次告警的时间戳，用来实现「首次立刻报、持续每 REPEAT_H
# 小时重复一次、恢复时报一次」。写不了状态文件不算致命，退化成每次都报。
declare -A LAST_ALERT=()
if [ -r "$STATE" ]; then
  while read -r mp ts; do
    [ -n "$mp" ] && LAST_ALERT["$mp"]="$ts"
  done < "$STATE"
fi

EPOCH="$(date +%s)"
REPEAT_S=$((REPEAT_H * 3600))
RC=0
declare -A NEXT_STATE=()

for mp in "${MOUNTS[@]}"; do
  read -r used_pct used avail total < <(
    df -PH "$mp" 2>/dev/null | awk 'NR==2 {gsub(/%/,"",$5); print $5, $3, $4, $2}'
  )
  [ -z "${used_pct:-}" ] && { echo "WARN: 读不到 $mp 的用量，跳过" >&2; continue; }

  prev="${LAST_ALERT[$mp]:-}"
  if [ "$used_pct" -ge "$THRESHOLD" ]; then
    NEXT_STATE["$mp"]="$EPOCH"
    if [ -n "$prev" ] && [ $((EPOCH - prev)) -lt "$REPEAT_S" ]; then
      NEXT_STATE["$mp"]="$prev"   # 仍在静默窗口内，保留首次告警时间
      continue
    fi
    send_telegram "🔴 磁盘用量告警
主机：${HOST}
挂载：${mp}
用量：${used_pct}%（阈值 ${THRESHOLD}%）
已用/可用/总计：${used} / ${avail} / ${total}
时间：${NOW}

写满会导致日志与数据写入失败。请尽快扩容或清理。" || RC=1
  elif [ -n "$prev" ]; then
    send_telegram "🟢 磁盘用量已恢复
主机：${HOST}
挂载：${mp}
用量：${used_pct}%（阈值 ${THRESHOLD}%）
可用：${avail} / ${total}
时间：${NOW}" || RC=1
  fi
done

if [ "$MODE" != "dry-run" ]; then
  if ! { for mp in "${!NEXT_STATE[@]}"; do printf '%s %s\n' "$mp" "${NEXT_STATE[$mp]}"; done; } > "$STATE" 2>/dev/null; then
    echo "WARN: 写不了状态文件 $STATE，持续超标时会重复告警" >&2
  fi
fi

exit "$RC"
