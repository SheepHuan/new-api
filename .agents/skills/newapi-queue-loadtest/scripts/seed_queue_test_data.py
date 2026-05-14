#!/usr/bin/env python3
"""Seed local new-api data for request queue load tests."""

from __future__ import annotations

import argparse
import http.cookiejar
import json
import os
import secrets
import sqlite3
import string
import sys
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone
from decimal import Decimal, InvalidOperation, ROUND_HALF_UP
from typing import Any


TOKEN_NAME = "queue-loadtest"
TOKEN_ALPHABET = string.ascii_letters + string.digits
QUOTA_PER_USD = Decimal("500000")


def log(message: str) -> None:
    print(f"[queue-seed] {message}", flush=True)


def parse_csv_ints(raw: str) -> list[int]:
    values: list[int] = []
    for item in raw.replace(" ", ",").split(","):
        item = item.strip()
        if not item:
            continue
        value = int(item)
        values.append(value)
    if not values:
        raise argparse.ArgumentTypeError("expected at least one integer")
    return values


def parse_ports(raw: str) -> list[int]:
    ports = parse_csv_ints(raw)
    for port in ports:
        if port <= 0 or port > 65535:
            raise argparse.ArgumentTypeError(f"invalid port: {port}")
    return ports


def parse_user_quota(raw_quota: str | None, raw_dollars: str | None) -> int:
    if raw_quota is not None and raw_quota.strip() != "":
        try:
            quota = int(raw_quota)
        except ValueError as exc:
            raise argparse.ArgumentTypeError(f"invalid user quota units: {raw_quota}") from exc
        if quota < 0:
            raise argparse.ArgumentTypeError("--user-quota cannot be negative")
        return quota

    dollars_text = (raw_dollars or "0").strip() or "0"
    try:
        dollars = Decimal(dollars_text)
    except InvalidOperation as exc:
        raise argparse.ArgumentTypeError(f"invalid user quota dollars: {raw_dollars}") from exc
    if dollars < 0:
        raise argparse.ArgumentTypeError("--user-quota-dollars cannot be negative")
    quota = (dollars * QUOTA_PER_USD).to_integral_value(rounding=ROUND_HALF_UP)
    return int(quota)


def extract_items(response: dict[str, Any]) -> list[dict[str, Any]]:
    data = response.get("data")
    if isinstance(data, dict):
        items = data.get("items")
        if isinstance(items, list):
            return [item for item in items if isinstance(item, dict)]
    if isinstance(data, list):
        return [item for item in data if isinstance(item, dict)]
    items = response.get("items")
    if isinstance(items, list):
        return [item for item in items if isinstance(item, dict)]
    return []


def require_success(response: dict[str, Any], action: str) -> dict[str, Any]:
    if response.get("success") is False:
        raise RuntimeError(f"{action} failed: {response.get('message') or response}")
    return response


class NewAPIClient:
    def __init__(self, base_url: str, timeout: float = 20.0) -> None:
        self.base_url = base_url.rstrip("/")
        self.timeout = timeout
        self.user_id: int | None = None
        self.cookie_jar = http.cookiejar.CookieJar()
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.cookie_jar))

    def request(
        self,
        method: str,
        path: str,
        data: dict[str, Any] | None = None,
        query: dict[str, Any] | None = None,
    ) -> dict[str, Any]:
        url = self.base_url + path
        if query:
            url += "?" + urllib.parse.urlencode(query)
        for attempt in range(8):
            body = None
            headers = {"Accept": "application/json"}
            if self.user_id is not None:
                headers["New-Api-User"] = str(self.user_id)
            if data is not None:
                body = json.dumps(data, separators=(",", ":")).encode("utf-8")
                headers["Content-Type"] = "application/json"
            request = urllib.request.Request(url, data=body, headers=headers, method=method.upper())
            try:
                with self.opener.open(request, timeout=self.timeout) as response:
                    raw = response.read()
                    status = response.status
            except urllib.error.HTTPError as exc:
                raw = exc.read()
                status = exc.code
            except urllib.error.URLError as exc:
                raise RuntimeError(f"{method} {url} failed: {exc}") from exc

            text = raw.decode("utf-8", errors="replace") if raw else "{}"
            try:
                parsed = json.loads(text)
            except json.JSONDecodeError:
                parsed = {"success": False, "message": text}
            if status == 429 and attempt < 7:
                sleep_seconds = min(8.0, 0.5 * (attempt + 1))
                log(f"rate limited by admin API; retrying {method} {path} in {sleep_seconds:.1f}s")
                time.sleep(sleep_seconds)
                continue
            if status >= 400:
                raise RuntimeError(f"{method} {url} returned HTTP {status}: {parsed}")
            if not isinstance(parsed, dict):
                return {"success": True, "data": parsed}
            return parsed
        raise RuntimeError(f"{method} {url} failed after retries")

    def expect(self, method: str, path: str, data: dict[str, Any] | None = None, query: dict[str, Any] | None = None) -> dict[str, Any]:
        return require_success(self.request(method, path, data, query), f"{method} {path}")


def ensure_setup(client: NewAPIClient, username: str, password: str) -> None:
    response = client.expect("GET", "/api/setup")
    setup_data = response.get("data") if isinstance(response.get("data"), dict) else {}
    if setup_data.get("status") is True:
        return
    payload = {
        "username": username,
        "password": password,
        "confirmPassword": password,
        "SelfUseModeEnabled": False,
        "DemoSiteEnabled": False,
    }
    response = client.request("POST", "/api/setup", payload)
    if response.get("success") is False and "已经初始化" not in str(response.get("message", "")):
        raise RuntimeError(f"setup failed: {response.get('message') or response}")
    log(f"initialized admin user {username}")


def login(client: NewAPIClient, username: str, password: str) -> None:
    response = client.expect("POST", "/api/user/login", {"username": username, "password": password})
    data = response.get("data")
    if isinstance(data, dict) and data.get("id") is not None:
        client.user_id = int(data["id"])


def update_option(client: NewAPIClient, key: str, value: Any) -> None:
    client.expect("PUT", "/api/option/", {"key": key, "value": value})


def configure_queue_options(
    args: argparse.Namespace,
    client: NewAPIClient,
    ports: list[int],
) -> None:
    options: dict[str, Any] = {
        "RequestQueueEnabled": True,
        "RequestQueueDefaultChannelRPM": args.default_channel_rpm,
        "RequestQueueMaxChannelPending": args.max_channel_pending,
        "RequestQueueDefaultUserMaxPending": args.default_user_max_pending,
        "RequestQueueScheduleStrategy": args.schedule_strategy,
        "RequestQueueChannelRPM": "{}",
    }
    if args.channel_rpm is not None:
        channel_rpm_map = {f"newapi-queue-{port}": args.channel_rpm for port in ports}
        options["RequestQueueChannelRPM"] = json.dumps(channel_rpm_map, separators=(",", ":"))
    for key, value in options.items():
        update_option(client, key, value)
    if args.channel_rpm is None:
        log(f"queue options enabled with default channel rpm={args.default_channel_rpm}; channel rpm JSON={{{{}}}}")
    else:
        log(f"queue options enabled with per-channel rpm override={args.channel_rpm}")


def find_user(client: NewAPIClient, username: str) -> dict[str, Any] | None:
    response = client.expect(
        "GET",
        "/api/user/search",
        query={"keyword": username, "group": "", "p": 1, "page_size": 100},
    )
    for user in extract_items(response):
        if user.get("username") == username:
            return user
    return None


def ensure_user(
    client: NewAPIClient,
    username: str,
    password: str,
    group: str,
    reset_password: bool,
) -> dict[str, Any]:
    user = find_user(client, username)
    if user is None:
        client.expect(
            "POST",
            "/api/user/",
            {
                "username": username,
                "password": password,
                "display_name": username,
                "role": 1,
            },
        )
        user = find_user(client, username)
        if user is None:
            raise RuntimeError(f"created user {username}, but could not find it")

    update_payload: dict[str, Any] = {
        "id": user["id"],
        "username": username,
        "display_name": username,
        "group": group,
        "remark": "queue load test user",
    }
    if reset_password:
        update_payload["password"] = password
    client.expect("PUT", "/api/user/", update_payload)
    updated = find_user(client, username)
    return updated or user


def ensure_user_min_quota(client: NewAPIClient, user: dict[str, Any], min_quota: int) -> dict[str, Any]:
    if min_quota <= 0:
        return user
    current_quota = 0
    try:
        current_quota = int(user.get("quota") or 0)
    except (TypeError, ValueError):
        current_quota = 0
    if current_quota >= min_quota:
        return user

    client.expect(
        "POST",
        "/api/user/manage",
        {
            "id": int(user["id"]),
            "action": "add_quota",
            "mode": "add",
            "value": min_quota - current_quota,
        },
    )
    user["quota"] = min_quota
    return user


def find_channel(client: NewAPIClient, name: str, base_url: str) -> dict[str, Any] | None:
    response = client.expect(
        "GET",
        "/api/channel/search",
        query={"keyword": name, "group": "", "model": "", "p": 1, "page_size": 100},
    )
    for channel in extract_items(response):
        if channel.get("name") == name:
            return channel
    response = client.expect(
        "GET",
        "/api/channel/search",
        query={"keyword": base_url, "group": "", "model": "", "p": 1, "page_size": 100},
    )
    for channel in extract_items(response):
        if channel.get("base_url") == base_url:
            return channel
    return None


def ensure_channel(args: argparse.Namespace, client: NewAPIClient, port: int) -> dict[str, Any] | None:
    base_url = f"http://127.0.0.1:{port}"
    name = f"newapi-queue-{port}"
    channel: dict[str, Any] = {
        "type": 1,
        "key": args.upstream_key,
        "status": 1,
        "name": name,
        "base_url": base_url,
        "models": args.model,
        "group": args.group,
        "priority": 0,
        "weight": args.channel_weight,
        "auto_ban": 0,
        "tag": "queue-loadtest",
        "setting": "{}",
        "test_model": args.model,
    }
    existing = find_channel(client, name, base_url)
    if existing is None:
        client.expect("POST", "/api/channel/", {"mode": "single", "channel": channel})
        log(f"created channel {name} -> {base_url}")
        return find_channel(client, name, base_url)
    channel["id"] = existing["id"]
    client.expect("PUT", "/api/channel/", channel)
    log(f"updated channel {name} -> {base_url}")
    return find_channel(client, name, base_url)


def sqlite_path_from_dsn(dsn: str) -> str:
    path = dsn.split("?", 1)[0]
    return os.path.abspath(os.path.expanduser(path))


def new_token_key() -> str:
    return "".join(secrets.choice(TOKEN_ALPHABET) for _ in range(48))


def sqlite_query_token(conn: sqlite3.Connection, user_id: int) -> sqlite3.Row | None:
    try:
        return conn.execute(
            'SELECT id, key FROM tokens WHERE user_id = ? AND name = ? AND deleted_at IS NULL ORDER BY id DESC LIMIT 1',
            (user_id, TOKEN_NAME),
        ).fetchone()
    except sqlite3.OperationalError:
        return conn.execute(
            "SELECT id, key FROM tokens WHERE user_id = ? AND name = ? ORDER BY id DESC LIMIT 1",
            (user_id, TOKEN_NAME),
        ).fetchone()


def generate_sqlite_tokens(
    sqlite_dsn: str,
    users: list[dict[str, Any]],
    group: str,
) -> list[dict[str, Any]]:
    db_path = sqlite_path_from_dsn(sqlite_dsn)
    if not os.path.exists(db_path):
        raise RuntimeError(f"SQLite database not found: {db_path}")
    conn = sqlite3.connect(db_path, timeout=30)
    conn.row_factory = sqlite3.Row
    now = int(time.time())
    records: list[dict[str, Any]] = []
    try:
        for user in users:
            user_id = int(user["id"])
            row = sqlite_query_token(conn, user_id)
            if row is None:
                key = new_token_key()
                conn.execute(
                    (
                        'INSERT INTO tokens (user_id, key, status, name, created_time, accessed_time, expired_time, '
                        'remain_quota, unlimited_quota, model_limits_enabled, model_limits, allow_ips, used_quota, '
                        '"group", cross_group_retry) VALUES (?, ?, 1, ?, ?, ?, -1, 0, 1, 0, ?, ?, 0, ?, 0)'
                    ),
                    (user_id, key, TOKEN_NAME, now, now, "", "", group),
                )
            else:
                key = str(row["key"])
                conn.execute(
                    (
                        'UPDATE tokens SET status = 1, expired_time = -1, unlimited_quota = 1, "group" = ?, '
                        "accessed_time = ? WHERE id = ?"
                    ),
                    (group, now, int(row["id"])),
                )
            records.append(
                {
                    "id": user_id,
                    "username": user["username"],
                    "token": "sk-" + key,
                    "token_name": TOKEN_NAME,
                }
            )
        conn.commit()
    finally:
        conn.close()
    return records


def find_user_token(client: NewAPIClient) -> dict[str, Any] | None:
    response = client.expect("GET", "/api/token/", query={"p": 1, "page_size": 100})
    for token in extract_items(response):
        if token.get("name") == TOKEN_NAME:
            return token
    return None


def generate_api_tokens(
    base_url: str,
    users: list[dict[str, Any]],
    password: str,
    group: str,
    timeout: float,
) -> list[dict[str, Any]]:
    records: list[dict[str, Any]] = []
    for index, user in enumerate(users, start=1):
        client = NewAPIClient(base_url, timeout=timeout)
        login(client, str(user["username"]), password)
        token = find_user_token(client)
        if token is None:
            client.expect(
                "POST",
                "/api/token/",
                {
                    "name": TOKEN_NAME,
                    "expired_time": -1,
                    "remain_quota": 0,
                    "unlimited_quota": True,
                    "model_limits_enabled": False,
                    "model_limits": "",
                    "group": group,
                },
            )
            token = find_user_token(client)
        if token is None:
            raise RuntimeError(f"could not create token for {user['username']}")
        key_response = client.expect("POST", f"/api/token/{token['id']}/key")
        key = key_response.get("data", {}).get("key")
        if not key:
            raise RuntimeError(f"could not reveal token key for {user['username']}")
        records.append(
            {
                "id": user["id"],
                "username": user["username"],
                "token": "sk-" + str(key).removeprefix("sk-"),
                "token_name": TOKEN_NAME,
            }
        )
        log(f"created/reused API token {index}/{len(users)} for {user['username']}")
    return records


def write_tokens_file(path: str, payload: dict[str, Any]) -> None:
    os.makedirs(os.path.dirname(os.path.abspath(path)), exist_ok=True)
    with open(path, "w", encoding="utf-8") as handle:
        json.dump(payload, handle, indent=2, sort_keys=True)
        handle.write("\n")
    try:
        os.chmod(path, 0o600)
    except OSError:
        pass


def build_parser() -> argparse.ArgumentParser:
    channel_rpm_env = os.environ.get("NEWAPI_QUEUE_TEST_CHANNEL_RPM")
    channel_rpm_default = int(channel_rpm_env) if channel_rpm_env not in (None, "") else None
    parser = argparse.ArgumentParser(description="Seed local request queue channels, users, and tokens.")
    parser.add_argument("--base-url", default="http://127.0.0.1:3000")
    parser.add_argument("--admin-username", default=os.environ.get("NEWAPI_ADMIN_USERNAME", "admin"))
    parser.add_argument("--admin-password", default=os.environ.get("NEWAPI_ADMIN_PASSWORD", "admin123"))
    parser.add_argument("--ports", default=os.environ.get("NEWAPI_QUEUE_TEST_PORTS", "12000,12001,12003"))
    parser.add_argument("--model", default=os.environ.get("NEWAPI_QUEUE_TEST_MODEL", "gpt-4o-mini"))
    parser.add_argument("--group", default=os.environ.get("NEWAPI_QUEUE_TEST_GROUP", "default"))
    parser.add_argument("--user-prefix", default=os.environ.get("NEWAPI_QUEUE_TEST_USER_PREFIX", "test"))
    parser.add_argument("--user-count", type=int, default=int(os.environ.get("NEWAPI_QUEUE_TEST_USER_COUNT", "64")))
    parser.add_argument("--user-password", default=os.environ.get("NEWAPI_QUEUE_TEST_USER_PASSWORD", "test123456"))
    parser.add_argument("--user-quota", default=os.environ.get("NEWAPI_QUEUE_TEST_USER_QUOTA", ""), help="Minimum raw quota units for each seeded test user")
    parser.add_argument("--user-quota-dollars", default=os.environ.get("NEWAPI_QUEUE_TEST_USER_QUOTA_DOLLARS", "10000000"), help="Minimum USD balance for each seeded test user when --user-quota is unset")
    parser.add_argument("--channel-rpm", type=int, default=channel_rpm_default, help="Optional per-channel RPM override written to RequestQueueChannelRPM JSON; omitted by default")
    parser.add_argument("--channel-weight", type=int, default=int(os.environ.get("NEWAPI_QUEUE_TEST_CHANNEL_WEIGHT", "1")))
    parser.add_argument("--default-channel-rpm", type=int, default=int(os.environ.get("NEWAPI_QUEUE_TEST_DEFAULT_CHANNEL_RPM", "80")))
    parser.add_argument("--max-channel-pending", type=int, default=int(os.environ.get("NEWAPI_QUEUE_TEST_MAX_CHANNEL_PENDING", "512")))
    parser.add_argument("--default-user-max-pending", type=int, default=int(os.environ.get("NEWAPI_QUEUE_TEST_DEFAULT_USER_MAX_PENDING", "32")))
    parser.add_argument("--schedule-strategy", choices=["fifo", "user_loop"], default=os.environ.get("NEWAPI_QUEUE_TEST_SCHEDULE_STRATEGY", "user_loop"))
    parser.add_argument("--upstream-key", default=os.environ.get("NEWAPI_QUEUE_TEST_UPSTREAM_KEY", "sk-local-queue-test"))
    parser.add_argument("--sqlite-path", default=os.environ.get("NEWAPI_SQLITE_PATH", os.environ.get("SQLITE_PATH", "data/new-api.sqlite?_busy_timeout=30000")))
    parser.add_argument("--token-mode", choices=["auto", "sqlite", "api", "none"], default=os.environ.get("NEWAPI_QUEUE_TEST_TOKEN_MODE", "auto"))
    parser.add_argument("--tokens-file", default=os.environ.get("NEWAPI_QUEUE_TEST_TOKENS_FILE", ".codex-deploy/queue-loadtest/tokens.json"))
    parser.add_argument("--timeout", type=float, default=20.0)
    parser.add_argument("--no-reset-user-password", action="store_true")
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    if args.user_count <= 0:
        parser.error("--user-count must be positive")
    if args.channel_rpm is not None and args.channel_rpm < 0:
        parser.error("--channel-rpm cannot be negative")
    if args.default_channel_rpm < 0:
        parser.error("--default-channel-rpm cannot be negative")

    ports = parse_ports(args.ports)
    try:
        user_quota = parse_user_quota(args.user_quota, args.user_quota_dollars)
    except argparse.ArgumentTypeError as exc:
        parser.error(str(exc))

    admin = NewAPIClient(args.base_url, timeout=args.timeout)
    ensure_setup(admin, args.admin_username, args.admin_password)
    login(admin, args.admin_username, args.admin_password)
    configure_queue_options(args, admin, ports)

    channels = [ensure_channel(args, admin, port) for port in ports]
    if user_quota > 0:
        log(f"ensuring each test user has at least {user_quota} raw quota units")
    users: list[dict[str, Any]] = []
    for index in range(1, args.user_count + 1):
        username = f"{args.user_prefix}{index}"
        user = ensure_user(
            admin,
            username=username,
            password=args.user_password,
            group=args.group,
            reset_password=not args.no_reset_user_password,
        )
        user = ensure_user_min_quota(admin, user, user_quota)
        users.append(user)
        if index == 1 or index == args.user_count or index % 8 == 0:
            log(f"seeded user {index}/{args.user_count}: {username}")

    token_records: list[dict[str, Any]] = []
    token_mode = args.token_mode
    if token_mode == "auto":
        if os.environ.get("SQL_DSN"):
            token_mode = "api"
        else:
            db_path = sqlite_path_from_dsn(args.sqlite_path)
            token_mode = "sqlite" if os.path.exists(db_path) else "api"

    if token_mode == "sqlite":
        token_records = generate_sqlite_tokens(args.sqlite_path, users, args.group)
        log(f"created/reused {len(token_records)} SQLite-backed API tokens")
    elif token_mode == "api":
        log("using API token generation; this may hit CriticalRateLimit on local defaults")
        token_records = generate_api_tokens(
            args.base_url,
            users,
            args.user_password,
            args.group,
            args.timeout,
        )
    else:
        log("token generation skipped")

    payload = {
        "base_url": args.base_url,
        "model": args.model,
        "ports": ports,
        "channels": [channel for channel in channels if channel],
        "users": token_records,
        "seeded_user_count": len(users),
        "created_at": datetime.now(timezone.utc).isoformat(),
        "notes": "Local request queue load-test tokens. Treat as secrets.",
    }
    if token_records:
        write_tokens_file(args.tokens_file, payload)
        log(f"wrote token file: {args.tokens_file}")
    log(f"done: channels={len(ports)} users={len(users)} tokens={len(token_records)}")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"[queue-seed] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
