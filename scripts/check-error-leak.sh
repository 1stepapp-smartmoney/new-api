#!/usr/bin/env bash
#
# 检测对外错误泄漏:找出 nginx 兜底清单尚未覆盖的错误 type。
#
# 两路检测:
#   ① 日志侧 —— 扫三个站的 ClickHouse 日志,列出白名单之外的 xxx_error 形态 token
#   ② 线上侧 —— 直接请求三个站触发 401,确认响应体里的 type 已脱敏
#
# 发现新 token 时以退出码 1 结束,并打印需要追加到
# /etc/nginx/snippets/leak-guard.conf 的 sub_filter 行。
#
# 用法：
#   ./scripts/check-error-leak.sh          扫最近 7 天
#   DAYS=90 ./scripts/check-error-leak.sh  扫最近 90 天
set -uo pipefail

TOKYO="${TOKYO_HOST:-ubuntu@54.64.192.229}"
CH_HOST="${CH_HOST:-172.31.15.54}"
DAYS="${DAYS:-7}"
SITES="${SITES:-nexapi.org neuromarket.club intellitoken.xyz}"
FOUND=0

# 协议语义类型:客户端 SDK 按其分支处理,必须原样透传,不得脱敏。
# 内部分类字段(error_code 等)也会出现在 other 里,一并放行。
ALLOW='openai_error|claude_error|gemini_error|midjourney_error|rerank_error|upstream_error
|invalid_request_error|invalid_parameter_error|permission_error|overloaded_error
|service_unavailable_error|internal_server_error|server_error|api_error
|unknown_error|end_error|client_gone_error|up_server_error|previous_error
|scanner_error|is_error|query_data_error|aws_invoke_error|image_parse_error
|nexapi_error'
ALLOW_RE=$(printf '%s' "$ALLOW" | tr -d '\n')

# 已覆盖 token 与各站品牌名都从线上配置现读,避免脚本与配置各自漂移
# (这正是原方案文档里警告的「两份独立维护的清单」问题)。
COVERED=$(ssh -o ConnectTimeout=10 "$TOKYO" \
  "sudo grep -oE \"sub_filter '\\\"[A-Za-z0-9_]+\\\"'\" /etc/nginx/snippets/leak-guard.conf 2>/dev/null \
   | grep -oE '[A-Za-z0-9_]+' | grep -v '^sub_filter\$'" 2>/dev/null | sort -u)
if [ -n "$COVERED" ]; then
  echo "已由 nginx 覆盖的 token: $(printf '%s' "$COVERED" | tr '\n' ' ')"
  ALLOW_RE="$ALLOW_RE|$(printf '%s' "$COVERED" | tr '\n' '|' | sed 's/|$//')"
else
  echo "⚠ 读不到线上 leak-guard.conf,本次仅按内置白名单判断"
fi

# 站点 -> 对外品牌 type,取自 conf.d/brand-map.conf 的映射表
BRAND_MAP=$(ssh -o ConnectTimeout=10 "$TOKYO" \
  "sudo grep -oE '^\s+\.[A-Za-z0-9.-]+\s+[A-Za-z0-9_]+_error;' /etc/nginx/conf.d/brand-map.conf 2>/dev/null \
   | tr -d ';' | awk '{print \$1\" \"\$2}'" 2>/dev/null)
if [ -n "$BRAND_MAP" ]; then
  echo "各站品牌映射:"; printf '%s\n' "$BRAND_MAP" | sed 's/^/    /'
  BRANDS=$(printf '%s\n' "$BRAND_MAP" | awk '{print $2}' | sort -u)
  ALLOW_RE="$ALLOW_RE|$(printf '%s' "$BRANDS" | tr '\n' '|' | sed 's/|$//')"
fi

# 查某站点应当返回的品牌 type;查不到则回退到默认
brand_for() {
  local host=$1 suffix brand
  while IFS=' ' read -r suffix brand; do
    [ -z "$suffix" ] && continue
    case ".$host" in
      *"$suffix") printf '%s' "$brand"; return ;;
    esac
  done <<< "$BRAND_MAP"
  printf 'nexapi_error'
}

echo
echo "════════ ① 日志侧:扫描最近 ${DAYS} 天 ════════"
LOG_HITS=$(ssh -o ConnectTimeout=10 "$TOKYO" "bash -s" <<REMOTE
cd ~/nexapi-docker
DSN=\$(grep -m1 '^LOG_SQL_DSN=' .env | sed 's|^LOG_SQL_DSN=clickhouse://default:||; s|@.*||')
PW=\$(printf '%s' "\$DSN" | python3 -c 'import sys,urllib.parse;sys.stdout.write(urllib.parse.unquote(sys.stdin.read()))')
for DB in nexapi_logs nexapi_logs_a nexapi_logs_b; do
  clickhouse-client --host $CH_HOST --port 9000 --user default --password "\$PW" -q "
    SELECT DISTINCT '\$DB|' || token FROM (
      SELECT arrayJoin(extractAll(other,   '([A-Za-z0-9]+_[A-Za-z0-9_]*error)')) AS token
      FROM \$DB.logs WHERE created_at > toUnixTimestamp(now() - INTERVAL $DAYS DAY) AND other LIKE '%_error%'
      UNION ALL
      SELECT arrayJoin(extractAll(content, '([A-Za-z0-9]+_[A-Za-z0-9_]*error)')) AS token
      FROM \$DB.logs WHERE created_at > toUnixTimestamp(now() - INTERVAL $DAYS DAY) AND content LIKE '%_error%'
    )" 2>/dev/null
done
REMOTE
)

NEW_TOKENS=$(printf '%s\n' "$LOG_HITS" | grep -v '^$' | awk -F'|' '{print $2}' \
             | sort -u | grep -vE "^($ALLOW_RE)$" || true)

if [ -z "$NEW_TOKENS" ]; then
  echo "  未发现白名单之外的错误 type ✓"
else
  FOUND=1
  echo "  ⚠ 发现未覆盖的 token:"
  printf '%s\n' "$NEW_TOKENS" | sed 's/^/    /'
  echo
  echo "  出现位置:"
  printf '%s\n' "$LOG_HITS" | grep -E "\|($(printf '%s' "$NEW_TOKENS" | tr '\n' '|' | sed 's/|$//'))$" | sed 's/^/    /'
  echo
  echo "  若确认属于品牌泄漏,追加到 /etc/nginx/snippets/leak-guard.conf:"
  printf '%s\n' "$NEW_TOKENS" | sed "s|.*|    sub_filter '\"&\"' '\"nexapi_error\"';|"
fi

echo
echo "════════ ② 线上侧:实时探测 ════════"
for D in $SITES; do
  printf "  %-20s " "$D"
  BODY=$(curl -s -m 15 -X POST "https://$D/v1/chat/completions" \
    -H "Authorization: Bearer sk-invalid-leak-probe" -H "content-type: application/json" \
    -d '{"model":"gpt-4o","messages":[{"role":"user","content":"x"}]}')
  TYPE=$(printf '%s' "$BODY" | grep -o '"type":"[^"]*"' | head -1 | sed 's/.*:"//; s/"$//')
  HDR=$(curl -sI -m 15 "https://$D/" | grep -icE '^x-(new-api|oneapi)')
  WANT=$(brand_for "$D")
  if [ "$TYPE" = "$WANT" ] && [ "$HDR" = 0 ]; then
    echo "type=$TYPE 品牌头=已隐藏 ✓"
  else
    FOUND=1
    echo "✗ type=${TYPE:-无}(应为 $WANT) 品牌头残留=$HDR"
  fi
done

echo
if [ "$FOUND" = 0 ]; then
  echo "全部通过,无新增泄漏"
else
  echo "发现问题,见上方输出"
  exit 1
fi
