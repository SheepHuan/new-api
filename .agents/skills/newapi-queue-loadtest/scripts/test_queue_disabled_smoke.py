#!/usr/bin/env python3
"""Smoke-test OpenAI-compatible traffic with the request queue disabled."""

from __future__ import annotations

import argparse
import concurrent.futures
import os
import random
import sys
import time
from collections import Counter
from typing import Any

from run_queue_loadtest import load_users, percentile, post_chat_completion
from seed_queue_test_data import NewAPIClient, ensure_setup, login, update_option


def log(message: str) -> None:
    print(f"[queue-disabled-smoke] {message}", flush=True)


def normalize_stream_mode(value: str) -> str:
    return "non_stream" if value == "non-stream" else value


def choose_stream(index: int, args: argparse.Namespace) -> bool:
    mode = normalize_stream_mode(args.stream_mode)
    if mode == "stream":
        return True
    if mode == "non_stream":
        return False
    if mode == "random":
        return random.random() < args.stream_ratio
    return index % 2 == 1


def get_options(client: NewAPIClient) -> dict[str, str]:
    response = client.expect("GET", "/api/option/")
    data = response.get("data")
    if not isinstance(data, list):
        return {}
    options: dict[str, str] = {}
    for item in data:
        if not isinstance(item, dict):
            continue
        key = item.get("key")
        if key is None:
            continue
        options[str(key)] = "" if item.get("value") is None else str(item.get("value"))
    return options


def get_queue_snapshot(client: NewAPIClient) -> tuple[list[dict[str, Any]], list[dict[str, Any]]]:
    response = client.expect("GET", "/api/request_queue/logs")
    data = response.get("data") if isinstance(response.get("data"), dict) else {}
    items = data.get("items") if isinstance(data, dict) else []
    channels = data.get("channels") if isinstance(data, dict) else []
    clean_items = [item for item in items if isinstance(item, dict)] if isinstance(items, list) else []
    clean_channels = [item for item in channels if isinstance(item, dict)] if isinstance(channels, list) else []
    return clean_items, clean_channels


def matching_queue_items(items: list[dict[str, Any]], usernames: set[str]) -> list[dict[str, Any]]:
    return [item for item in items if str(item.get("username") or "") in usernames]


def summarize_channels(channels: list[dict[str, Any]]) -> str:
    if not channels:
        return "-"
    parts: list[str] = []
    for channel in sorted(channels, key=lambda item: str(item.get("channel_name") or item.get("channel_id") or "")):
        name = str(channel.get("channel_name") or channel.get("channel_id") or "unknown")
        queued = int(channel.get("queued_request_count") or 0)
        users = int(channel.get("queued_user_count") or 0)
        rpm = int(channel.get("current_rpm") or 0)
        parts.append(f"{name}:queued={queued},users={users},rpm={rpm}")
    return "; ".join(parts)


def wait_for_no_test_queue_items(
    client: NewAPIClient,
    usernames: set[str],
    timeout_seconds: float,
    interval_seconds: float,
) -> tuple[list[dict[str, Any]], list[dict[str, Any]], list[dict[str, Any]]]:
    deadline = time.monotonic() + timeout_seconds
    last_items: list[dict[str, Any]] = []
    last_channels: list[dict[str, Any]] = []
    last_matches: list[dict[str, Any]] = []
    while True:
        last_items, last_channels = get_queue_snapshot(client)
        last_matches = matching_queue_items(last_items, usernames)
        if not last_matches:
            return last_items, last_channels, last_matches
        if time.monotonic() >= deadline:
            return last_items, last_channels, last_matches
        time.sleep(interval_seconds)


def request_once(index: int, args: argparse.Namespace, user: dict[str, Any]) -> dict[str, Any]:
    stream = choose_stream(index, args)
    status, latency, ok, detail = post_chat_completion(
        args.base_url,
        str(user["token"]),
        args.model,
        args.prompt,
        args.timeout,
        stream,
    )
    return {
        "index": index,
        "username": str(user.get("username") or user.get("id") or ""),
        "stream_mode": "stream" if stream else "non_stream",
        "status": status,
        "latency": latency,
        "ok": ok,
        "detail": detail,
    }


def run_requests(args: argparse.Namespace, users: list[dict[str, Any]]) -> list[dict[str, Any]]:
    selected = [users[index % len(users)] for index in range(args.requests)]
    max_workers = min(max(1, args.max_workers), max(1, args.requests))
    results: list[dict[str, Any]] = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as executor:
        futures = [
            executor.submit(request_once, index, args, user)
            for index, user in enumerate(selected)
        ]
        for future in concurrent.futures.as_completed(futures):
            results.append(future.result())
    results.sort(key=lambda item: int(item["index"]))
    return results


def print_request_summary(results: list[dict[str, Any]]) -> None:
    statuses: Counter[str] = Counter(str(item["status"]) for item in results)
    streams: Counter[str] = Counter(str(item["stream_mode"]) for item in results)
    latencies = [float(item["latency"]) for item in results]
    ok_count = sum(1 for item in results if item["ok"])
    log(
        "requests "
        f"sent={len(results)} ok={ok_count} errors={len(results) - ok_count} "
        f"statuses={dict(statuses)} streams={dict(streams)} "
        f"p50={percentile(latencies, 50):.3f}s p95={percentile(latencies, 95):.3f}s "
        f"max={(max(latencies) if latencies else 0.0):.3f}s"
    )
    for item in results:
        if item["ok"]:
            continue
        log(
            "error "
            f"index={item['index']} user={item['username']} mode={item['stream_mode']} "
            f"status={item['status']} latency={item['latency']:.3f}s detail={item['detail'] or '-'}"
        )


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Smoke-test new-api with request scheduling disabled.")
    parser.add_argument("--base-url", default="http://127.0.0.1:3000")
    parser.add_argument("--admin-username", default=os.environ.get("NEWAPI_ADMIN_USERNAME", "admin"))
    parser.add_argument("--admin-password", default=os.environ.get("NEWAPI_ADMIN_PASSWORD", "admin123"))
    parser.add_argument("--tokens-file", default=os.environ.get("NEWAPI_QUEUE_TEST_TOKENS_FILE", ".codex-deploy/queue-loadtest/tokens.json"))
    parser.add_argument("--limit-users", type=int, default=4)
    parser.add_argument("--requests", type=int, default=8)
    parser.add_argument("--model", default=None, help="Defaults to the model in the token file")
    parser.add_argument("--prompt", default="Reply with a short queue disabled smoke-test acknowledgement.")
    parser.add_argument("--timeout", type=float, default=180.0)
    parser.add_argument("--max-workers", type=int, default=16)
    parser.add_argument("--stream-mode", choices=["mixed", "random", "stream", "non_stream", "non-stream"], default="mixed")
    parser.add_argument("--stream-ratio", type=float, default=0.5, help="Probability of stream requests when --stream-mode=random")
    parser.add_argument("--wait-empty-timeout", type=float, default=30.0, help="Seconds to wait for selected test users to disappear from queue logs after disabling scheduling")
    parser.add_argument("--wait-empty-interval", type=float, default=0.5)
    parser.add_argument("--keep-model-rate-limit", action="store_true", help="Do not force ModelRequestRateLimitEnabled=false during this smoke test")
    parser.add_argument("--allow-queued-test-users", action="store_true", help="Warn instead of failing if selected test users remain in queue logs")
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    if args.limit_users <= 0:
        parser.error("--limit-users must be positive")
    if args.requests <= 0:
        parser.error("--requests must be positive")
    if args.max_workers <= 0:
        parser.error("--max-workers must be positive")
    if args.stream_ratio < 0 or args.stream_ratio > 1:
        parser.error("--stream-ratio must be between 0 and 1")
    args.stream_mode = normalize_stream_mode(args.stream_mode)

    token_payload, users = load_users(args.tokens_file, args.limit_users)
    if args.model is None:
        args.model = token_payload.get("model") or "gpt-4o-mini"

    selected_usernames = {
        str(user.get("username") or user.get("id") or "")
        for user in users[: min(len(users), args.limit_users)]
        if user.get("token")
    }
    if not selected_usernames:
        raise RuntimeError("no selected users found in token file")

    admin = NewAPIClient(args.base_url, timeout=args.timeout)
    ensure_setup(admin, args.admin_username, args.admin_password)
    login(admin, args.admin_username, args.admin_password)

    before_options = get_options(admin)
    log(
        "initial options "
        f"RequestQueueEnabled={before_options.get('RequestQueueEnabled', '<missing>')} "
        f"ModelRequestRateLimitEnabled={before_options.get('ModelRequestRateLimitEnabled', '<missing>')}"
    )

    update_option(admin, "RequestQueueEnabled", False)
    if not args.keep_model_rate_limit:
        update_option(admin, "ModelRequestRateLimitEnabled", False)

    after_options = get_options(admin)
    queue_enabled = after_options.get("RequestQueueEnabled")
    rate_limit_enabled = after_options.get("ModelRequestRateLimitEnabled")
    log(
        "test options "
        f"RequestQueueEnabled={queue_enabled} "
        f"ModelRequestRateLimitEnabled={rate_limit_enabled}"
    )
    if queue_enabled != "false":
        raise RuntimeError(f"RequestQueueEnabled should be false, got {queue_enabled!r}")

    items, channels, matches = wait_for_no_test_queue_items(
        admin,
        selected_usernames,
        args.wait_empty_timeout,
        args.wait_empty_interval,
    )
    log(
        "queue before requests "
        f"items={len(items)} selected_user_items={len(matches)} "
        f"channels={summarize_channels(channels)}"
    )
    if matches and not args.allow_queued_test_users:
        raise RuntimeError(
            f"{len(matches)} selected test-user requests are still queued after disabling scheduling"
        )

    log(
        f"sending {args.requests} requests with {len(users)} users "
        f"model={args.model} stream_mode={args.stream_mode}"
    )
    results = run_requests(args, users)
    print_request_summary(results)

    items, channels = get_queue_snapshot(admin)
    matches = matching_queue_items(items, selected_usernames)
    log(
        "queue after requests "
        f"items={len(items)} selected_user_items={len(matches)} "
        f"channels={summarize_channels(channels)}"
    )

    failed_requests = [item for item in results if not item["ok"]]
    if failed_requests:
        return 2
    if matches and not args.allow_queued_test_users:
        return 2
    log("PASS: request scheduling stayed disabled and OpenAI-compatible requests completed")
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except FileNotFoundError as exc:
        print(
            "[queue-disabled-smoke] ERROR: "
            f"{exc}. Run auto-deploy/seed first so {exc.filename} exists.",
            file=sys.stderr,
        )
        raise SystemExit(1)
    except Exception as exc:
        print(f"[queue-disabled-smoke] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
