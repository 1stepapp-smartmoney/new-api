#!/usr/bin/env python3
"""Nexapi Alert Bot — 订阅码分发告警推送。

配套 §11 的主机级告警（disk-alert / oom-telegram-notify）：告警不再只发给
管理员一人，而是发给全部订阅者。订阅必须凭一次性订阅码完成，防止 bot
用户名泄露后被随意订阅：

  管理员（/etc/nexapi-disk-alert.conf 的 ADMIN_CHAT_ID）在 Telegram 里
  /newcode [备注]  → 生成一个只能用一次的订阅码，发给要接收告警的人；
  对方点「订阅」按钮或直接把订阅码发给这个 bot → 订阅成功，码即刻作废。

交互（inline keyboard）：
  普通用户 /start → 「🎟 订阅告警推送」按钮 → 点按后 bot 用 force_reply
  提示回复订阅码，下一步照着回复即可，不需要记任何命令。
  管理员 /start → 管理面板按钮：生成订阅码 / 查看未用码 / 查看订阅者。

状态目录 /var/lib/nexapi-alert-bot/：
  subscribers.json  订阅者明细（chat_id、姓名、用了哪个码、时间）
  codes.json        订阅码台账（未用/已用、备注、使用者）
  targets.txt       告警广播目标（管理员 + 全部订阅者，每行一个 chat_id，
                    disk-alert / oom-telegram-notify 只读这个文件）
  offset.txt        getUpdates 断点，重启不重复消费

凭据只从 /etc/nexapi-disk-alert.conf 读取（与 disk-alert 共用一份），token
不出现在任何命令行参数里。CLI：--newcode [备注] 在主机上直接生成码（root），
--targets 打印当前广播目标，供脚本与排查用。
"""
import json
import os
import re
import secrets
import sys
import tempfile
import time
import urllib.parse
import urllib.request

CONF = os.environ.get("ALERT_BOT_CONF", "/etc/nexapi-disk-alert.conf")
STATE_DIR = os.environ.get("ALERT_BOT_STATE", "/var/lib/nexapi-alert-bot")
SUBS_FILE = os.path.join(STATE_DIR, "subscribers.json")
CODES_FILE = os.path.join(STATE_DIR, "codes.json")
TARGETS_FILE = os.path.join(STATE_DIR, "targets.txt")
OFFSET_FILE = os.path.join(STATE_DIR, "offset.txt")

# 去掉易混淆的 I/L/O/0/1，口头发码不容易错
ALPHABET = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
CODE_LEN = 8
CODE_RE = re.compile(r"^[A-Z0-9]{4,16}$")

WELCOME = (
    "Nexapi Alert Bot — 基础设施告警（OOM / 磁盘等）。\n\n"
    "凭管理员发放的一次性订阅码订阅，每个码只能用一次。"
)
ADMIN_HINT = (
    "管理员命令：/newcode [备注]、/codes、/subs、/rm <chat_id>、"
    "/broadcast <内容>"
)


def log(msg):
    print(time.strftime("[%F %T] ") + msg, flush=True)


def read_conf():
    conf = {}
    try:
        with open(CONF) as f:
            for line in f:
                line = line.strip()
                if line and not line.startswith("#") and "=" in line:
                    k, v = line.split("=", 1)
                    conf[k.strip()] = v.strip().strip('"').strip("'")
    except FileNotFoundError:
        pass
    return conf


def load_json(path, default):
    try:
        with open(path) as f:
            return json.load(f)
    except (FileNotFoundError, json.JSONDecodeError):
        return default


def save_json(path, obj):
    os.makedirs(STATE_DIR, exist_ok=True)
    fd, tmp = tempfile.mkstemp(dir=STATE_DIR)
    with os.fdopen(fd, "w") as f:
        json.dump(obj, f, ensure_ascii=False, indent=1)
    os.replace(tmp, path)


def api(token, method, **params):
    if "reply_markup" in params:
        params["reply_markup"] = json.dumps(params["reply_markup"])
    data = urllib.parse.urlencode(params).encode()
    req = urllib.request.Request(
        f"https://api.telegram.org/bot{token}/{method}", data=data)
    with urllib.request.urlopen(req, timeout=35) as r:
        return json.load(r)


def keyboard(rows):
    """inline keyboard：rows 是 [[{text, callback_data}], ...]"""
    return {"inline_keyboard": [[{"text": t, "callback_data": d} for t, d in row]
                                for row in rows]}


class Bot:
    def __init__(self, token, admin):
        self.token = token
        self.admin = str(admin) if admin else ""

    def send(self, chat, text, reply_markup=None, force_reply=False):
        kwargs = {"chat_id": chat, "text": text,
                  "disable_web_page_preview": "true"}
        if reply_markup is not None:
            kwargs["reply_markup"] = reply_markup
        elif force_reply:
            # Telegram 会在输入框上方挂一条“回复 bot”的横幅，照着打字就行
            kwargs["reply_markup"] = {"force_reply": True,
                                      "selective": False}
        try:
            api(self.token, "sendMessage", **kwargs)
            return True
        except urllib.error.HTTPError as e:
            body = e.read().decode(errors="replace")[:200]
            log(f"sendMessage to {chat} failed: {body}")
            # 403: 被对方拉黑或从未启动过对话 —— 该订阅者已收不到任何推送，
            # 直接移除，保持 targets 干净。
            if e.code == 403:
                self.drop_subscriber(chat, reason="403 blocked")
            return False
        except Exception as e:  # 网络抖动等，下一条告警再试
            log(f"sendMessage to {chat} error: {e}")
            return False

    # ---- 订阅者与广播目标 -------------------------------------------------
    def subscribers(self):
        return load_json(SUBS_FILE, [])

    def save_subscribers(self, subs):
        save_json(SUBS_FILE, subs)
        self.refresh_targets(subs)

    def refresh_targets(self, subs=None):
        subs = self.subscribers() if subs is None else subs
        targets, seen = [], set()
        if self.admin and self.admin not in seen:
            targets.append(self.admin)
            seen.add(self.admin)
        for s in subs:
            cid = str(s["chat_id"])
            if cid not in seen:
                targets.append(cid)
                seen.add(cid)
        os.makedirs(STATE_DIR, exist_ok=True)
        with open(TARGETS_FILE, "w") as f:
            f.write("\n".join(targets) + "\n")
        return targets

    def drop_subscriber(self, chat, reason=""):
        subs = self.subscribers()
        rest = [s for s in subs if str(s["chat_id"]) != str(chat)]
        if len(rest) != len(subs):
            log(f"drop subscriber {chat} ({reason})")
            self.save_subscribers(rest)

    # ---- 订阅码 -----------------------------------------------------------
    def new_code(self, note=""):
        codes = load_json(CODES_FILE, {})
        while True:
            code = "".join(secrets.choice(ALPHABET) for _ in range(CODE_LEN))
            if code not in codes:
                break
        codes[code] = {"note": note, "created_at": time.strftime("%F %T")}
        save_json(CODES_FILE, codes)
        return code

    def redeem(self, chat, name, code):
        subs = self.subscribers()
        if any(str(s["chat_id"]) == str(chat) for s in subs):
            self.send(chat, "你已订阅过告警，无需重复订阅。")
            return
        codes = load_json(CODES_FILE, {})
        entry = codes.get(code)
        if entry is None:
            self.send(chat, "❌ 无效的订阅码。请向管理员索取。", force_reply=True)
            return
        if entry.get("used_by"):
            self.send(chat, "❌ 该订阅码已被使用（每个码只能订阅一次）。")
            return
        entry["used_by"] = chat
        entry["used_by_name"] = name
        entry["used_at"] = time.strftime("%F %T")
        save_json(CODES_FILE, codes)
        subs.append({"chat_id": chat, "name": name, "code": code,
                     "added_at": time.strftime("%F %T")})
        self.save_subscribers(subs)
        self.send(chat, "✅ 订阅成功。此后 OOM / 磁盘等告警会推送到这个对话。")
        if self.admin:
            self.send(self.admin,
                      f"🔔 新订阅：{name} ({chat}) 使用订阅码 {code}。")
        log(f"redeem ok: {name} ({chat}) code={code}")

    # ---- 界面 -------------------------------------------------------------
    def show_welcome(self, chat):
        is_admin = self.admin and str(chat) == self.admin
        if is_admin:
            self.send(chat, WELCOME + "\n\n" + ADMIN_HINT,
                      reply_markup=keyboard([
                          [("🎟 生成订阅码", "cmd:newcode")],
                          [("📋 未用码", "cmd:codes"), ("👥 订阅者", "cmd:subs")],
                      ]))
        else:
            self.send(chat, WELCOME,
                      reply_markup=keyboard([
                          [("🎟 订阅告警推送", "subscribe")],
                      ]))

    def prompt_code(self, chat):
        self.send(chat, "请把管理员发给你的订阅码（8 位大写字母/数字）"
                        "直接回复到这里：", force_reply=True)

    # ---- 命令 -------------------------------------------------------------
    def handle_message(self, msg):
        chat = (msg.get("chat") or {}).get("id")
        if chat is None:
            return
        text = (msg.get("text") or "").strip()
        frm = msg.get("from") or {}
        name = ("@" + frm["username"]) if frm.get("username") \
            else (frm.get("first_name") or str(chat))
        argv = text.split()
        cmd = argv[0].split("@")[0] if argv else ""

        if not self.admin:
            # 引导期：ADMIN_CHAT_ID 还没写进 conf，把任何来信来源记进
            # journal，供管理员在主机上核对后写入并重启。
            log(f"bootstrap: message from {name} ({chat}) — "
                f"确认是管理员后写入 {CONF} 的 ADMIN_CHAT_ID")

        if cmd in ("/start", "/help"):
            if len(argv) > 1 and CODE_RE.match(argv[1]):
                self.redeem(chat, name, argv[1].upper())
            else:
                self.show_welcome(chat)
            return
        if cmd in ("/newcode", "/codes", "/subs", "/rm", "/broadcast"):
            if not self.admin or str(chat) != self.admin:
                self.send(chat, "❌ 仅管理员可用。")
                return
            self.admin_cmd(chat, cmd, argv[1:])
            return
        if text and CODE_RE.match(text):
            self.redeem(chat, name, text.upper())
            return
        if not self.admin:
            return  # 引导期不回复，等管理员绑定
        self.send(chat, "点下方按钮或把订阅码发给我即可订阅；/help 查看用法。",
                  reply_markup=keyboard([[("🎟 订阅告警推送", "subscribe")]]))

    def handle_callback(self, cq):
        data = cq.get("data") or ""
        chat = (cq.get("message") or {}).get("chat", {}).get("id")
        frm = cq.get("from") or {}
        name = ("@" + frm["username"]) if frm.get("username") \
            else (frm.get("first_name") or str(chat))
        try:
            api(self.token, "answerCallbackQuery", callback_query_id=cq["id"])
        except Exception as e:
            log(f"answerCallbackQuery error: {e}")
        if chat is None:
            return
        if data == "subscribe":
            self.prompt_code(chat)
            return
        if data.startswith("cmd:"):
            if not self.admin or str(chat) != self.admin:
                self.send(chat, "❌ 仅管理员可用。")
                return
            self.admin_cmd(chat, "/" + data[4:], [])

    def handle(self, update):
        if update.get("callback_query"):
            self.handle_callback(update["callback_query"])
            return
        msg = update.get("message") or update.get("channel_post") or {}
        self.handle_message(msg)

    def admin_cmd(self, chat, cmd, args):
        if cmd == "/newcode":
            note = " ".join(args)
            code = self.new_code(note)
            self.send(chat,
                      f"🎟 新订阅码：{code}\n"
                      f"备注：{note or '(无)'}\n"
                      "把它发给要订阅的人；每个码只能用一次。",
                      reply_markup=keyboard([[("📋 再生成一个", "cmd:newcode")]]))
            log(f"new code {code} note={note}")
        elif cmd == "/codes":
            codes = load_json(CODES_FILE, {})
            unused = [c for c, e in codes.items() if not e.get("used_by")]
            used = [c for c, e in codes.items() if e.get("used_by")]
            self.send(chat,
                      f"未使用 ({len(unused)})：{', '.join(unused) or '无'}\n"
                      f"已使用 ({len(used)})：{', '.join(used) or '无'}")
        elif cmd == "/subs":
            subs = self.subscribers()
            lines = [f"{s['name']}  {s['chat_id']}  {s['added_at']}  码 {s['code']}"
                     for s in subs] or ["(无)"]
            self.send(chat, f"订阅者 ({len(subs)})：\n" + "\n".join(lines))
        elif cmd == "/rm":
            if not args or not args[0].lstrip("-").isdigit():
                self.send(chat, "用法：/rm <chat_id>")
                return
            self.drop_subscriber(args[0], reason="admin /rm")
            self.send(chat, f"已移除 {args[0]}。")
        elif cmd == "/broadcast":
            body = " ".join(args)
            if not body:
                self.send(chat, "用法：/broadcast <内容>")
                return
            targets = self.refresh_targets()
            ok = sum(1 for t in targets if self.send(t, body))
            self.send(chat, f"已广播给 {ok}/{len(targets)} 个目标。")

    def run(self):
        offset = 0
        try:
            with open(OFFSET_FILE) as f:
                offset = int(f.read().strip() or 0)
        except (FileNotFoundError, ValueError):
            pass
        self.refresh_targets()
        log(f"bot running; admin={'set' if self.admin else 'NOT SET'}; "
            f"targets={len(self.refresh_targets())}")
        while True:
            try:
                params = {"timeout": 25}
                if offset:
                    params["offset"] = offset
                r = api(self.token, "getUpdates", **params)
                for u in r.get("result", []):
                    offset = u["update_id"] + 1
                    with open(OFFSET_FILE, "w") as f:
                        f.write(str(offset))
                    try:
                        self.handle(u)
                    except Exception as e:
                        log(f"handle error: {e}")
            except Exception as e:
                log(f"getUpdates error: {e}; retry in 5s")
                time.sleep(5)


def main():
    conf = read_conf()
    token = conf.get("TELEGRAM_BOT_TOKEN", "")
    admin = conf.get("ADMIN_CHAT_ID", "")
    if not token or token == "fill-me":
        print(f"ERROR: {CONF} 缺少 TELEGRAM_BOT_TOKEN", file=sys.stderr)
        return 1
    bot = Bot(token, admin)
    if len(sys.argv) > 1 and sys.argv[1] == "--newcode":
        print(bot.new_code(" ".join(sys.argv[2:])))
        return 0
    if len(sys.argv) > 1 and sys.argv[1] == "--targets":
        print("\n".join(bot.refresh_targets()) or "(none)")
        return 0
    bot.run()
    return 0


if __name__ == "__main__":
    sys.exit(main())
