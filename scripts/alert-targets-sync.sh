#!/usr/bin/env bash
#
# 把 alert bot 的推送名单同步到不跑 bot 的主机。
#
# 背景：订阅只在 Tokyo 的 alert bot（scripts/alert_bot.py）上管理，名单在
# /var/lib/nexapi-alert-bot/targets.txt。ClickHouse 主机只跑磁盘告警，没有
# 名单时只能发给 TELEGRAM_CHAT_ID 一人——而 2026-09-18 撑爆的正是它的数据盘。
# 同一个 bot 同时只能有一个进程接收消息，所以不能在那台机上再跑一个 bot，
# 只能把名单同步过去。
#
# 用法：
#   alert-targets-sync.sh push      在 bot 所在主机上运行：把名单推给 SYNC_TARGET
#   alert-targets-sync.sh receive   在接收主机上，作为 ssh 强制命令运行：从 stdin
#                                   读名单，校验后原子替换本机名单
#
# 方向是推送而不是拉取：拉取意味着接收主机要持有能登录 bot 主机的凭证，而 bot
# 主机上有生产库的全部密钥。推送端用一把专用密钥，接收端在 authorized_keys 里
# 把它限定为「只能从 bot 主机内网地址连入、只能执行 receive」：
#   restrict,from="<bot 主机内网 IP>",command="/usr/local/bin/nexapi-alert-targets-sync receive" ssh-ed25519 AAAA...
#
# 可调参数（环境变量，push 用）：
#   SYNC_TARGET   接收端，默认 alertsync@172.31.15.54
#   SYNC_KEY      推送用私钥，默认 /root/.ssh/nexapi-alert-sync
set -uo pipefail

TARGETS_FILE=/var/lib/nexapi-alert-bot/targets.txt
SYNC_TARGET="${SYNC_TARGET:-alertsync@172.31.15.54}"
SYNC_KEY="${SYNC_KEY:-/root/.ssh/nexapi-alert-sync}"
# 名单是 Telegram chat_id，每行一个；上限远高于实际人数，只为挡住异常输入。
MAX_TARGETS=200
MAX_BYTES=8192

push() {
  if [ ! -s "$TARGETS_FILE" ]; then
    # 空名单不推：接收端会拒收，推了也只会制造一条错误。
    echo "ERROR: $TARGETS_FILE 不存在或为空，未推送" >&2
    return 1
  fi
  # 主机密钥必须事先固定在 known_hosts 里（见 FORK-CHANGES），绝不自动接受新密钥。
  # -T：接收端的 restrict 本就不给终端，不申请可免掉一条无用的告警行。
  if ! ssh -T -i "$SYNC_KEY" -o BatchMode=yes -o IdentitiesOnly=yes \
        -o StrictHostKeyChecking=yes -o ConnectTimeout=10 \
        "$SYNC_TARGET" < "$TARGETS_FILE"; then
    echo "ERROR: 推送名单到 $SYNC_TARGET 失败" >&2
    return 1
  fi
  echo "已推送 $(grep -c . "$TARGETS_FILE") 个目标到 $SYNC_TARGET"
}

receive() {
  local dir tmp lines
  dir=$(dirname "$TARGETS_FILE")
  # 多读 1 字节，用来判断输入是否超长。
  lines=$(head -c $((MAX_BYTES + 1)) | tr -d '\r')
  if [ "${#lines}" -gt "$MAX_BYTES" ]; then
    echo "ERROR: 名单超过 ${MAX_BYTES} 字节，拒收" >&2
    return 1
  fi
  if [ -z "$(printf '%s' "$lines" | tr -d '[:space:]')" ]; then
    echo "ERROR: 名单为空，拒收（保留现有名单）" >&2
    return 1
  fi
  if printf '%s\n' "$lines" | grep -v '^$' | grep -qvE '^-?[0-9]{1,20}$'; then
    echo "ERROR: 名单含非 chat_id 的行，拒收" >&2
    return 1
  fi
  if [ "$(printf '%s\n' "$lines" | grep -c .)" -gt "$MAX_TARGETS" ]; then
    echo "ERROR: 名单超过 ${MAX_TARGETS} 个目标，拒收" >&2
    return 1
  fi
  mkdir -p "$dir"
  tmp=$(mktemp "$dir/.targets.XXXXXX") || return 1
  printf '%s\n' "$lines" | grep -v '^$' > "$tmp"
  chmod 644 "$tmp"
  # 原子替换：磁盘告警随时可能在读，绝不能让它读到写了一半的文件。
  mv -f "$tmp" "$TARGETS_FILE"
  echo "已接收 $(grep -c . "$TARGETS_FILE") 个目标"
}

case "${1:-}" in
  push)    push ;;
  receive) receive ;;
  *)       sed -n '2,25p' "$0"; exit 2 ;;
esac
