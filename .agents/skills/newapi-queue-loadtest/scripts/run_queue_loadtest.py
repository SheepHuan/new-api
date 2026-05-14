#!/usr/bin/env python3
"""Run concurrent per-user OpenAI-compatible traffic against new-api."""

from __future__ import annotations

import argparse
import concurrent.futures
import json
import math
import random
import statistics
import sys
import threading
import time
import urllib.error
import urllib.request
from collections import Counter, defaultdict
from typing import Any


def parse_rpm_pattern(raw: str) -> list[float]:
    values: list[float] = []
    for item in raw.replace(" ", ",").split(","):
        item = item.strip()
        if not item:
            continue
        value = float(item)
        if value < 0:
            raise argparse.ArgumentTypeError(f"rpm cannot be negative: {value}")
        values.append(value)
    if not values:
        raise argparse.ArgumentTypeError("expected at least one rpm value")
    return values


def percentile(values: list[float], pct: float) -> float:
    if not values:
        return 0.0
    ordered = sorted(values)
    index = max(0, min(len(ordered) - 1, math.ceil((pct / 100.0) * len(ordered)) - 1))
    return ordered[index]


class Stats:
    def __init__(self, error_sample_limit: int) -> None:
        self._lock = threading.Lock()
        self._error_sample_limit = error_sample_limit
        self.started = time.monotonic()
        self.sent = 0
        self.completed = 0
        self.ok = 0
        self.errors = 0
        self.skipped = 0
        self.statuses: Counter[str] = Counter()
        self.latencies: list[float] = []
        self.by_offered_rpm: dict[float, dict[str, Any]] = defaultdict(lambda: {"count": 0, "ok": 0, "errors": 0, "latencies": []})
        self.by_stream_mode: dict[str, dict[str, Any]] = defaultdict(lambda: {"count": 0, "ok": 0, "errors": 0, "latencies": []})
        self.error_samples: list[dict[str, Any]] = []

    def mark_sent(self) -> None:
        with self._lock:
            self.sent += 1

    def mark_skipped(self) -> None:
        with self._lock:
            self.skipped += 1

    def record(
        self,
        status: str,
        latency: float,
        offered_rpm: float,
        stream_mode: str,
        ok: bool,
        username: str,
        error_detail: str,
    ) -> None:
        with self._lock:
            self.completed += 1
            self.statuses[status] += 1
            self.latencies.append(latency)
            rpm_target = self.by_offered_rpm[offered_rpm]
            rpm_target["count"] += 1
            rpm_target["latencies"].append(latency)
            stream_target = self.by_stream_mode[stream_mode]
            stream_target["count"] += 1
            stream_target["latencies"].append(latency)
            if ok:
                self.ok += 1
                rpm_target["ok"] += 1
                stream_target["ok"] += 1
            else:
                self.errors += 1
                rpm_target["errors"] += 1
                stream_target["errors"] += 1
                if len(self.error_samples) < self._error_sample_limit:
                    self.error_samples.append(
                        {
                            "status": status,
                            "username": username,
                            "rpm": offered_rpm,
                            "stream": stream_mode,
                            "latency": latency,
                            "detail": error_detail,
                        }
                    )

    def snapshot(self) -> dict[str, Any]:
        with self._lock:
            latencies = list(self.latencies)
            stream_modes = {
                key: {"count": value["count"], "ok": value["ok"], "errors": value["errors"]}
                for key, value in sorted(self.by_stream_mode.items())
            }
            return {
                "elapsed": time.monotonic() - self.started,
                "sent": self.sent,
                "completed": self.completed,
                "ok": self.ok,
                "errors": self.errors,
                "skipped": self.skipped,
                "statuses": dict(self.statuses),
                "streams": stream_modes,
                "p50": percentile(latencies, 50),
                "p95": percentile(latencies, 95),
                "max": max(latencies) if latencies else 0.0,
            }

    def grouped_summary(self) -> list[tuple[Any, dict[str, Any]]]:
        source = self.by_offered_rpm
        with self._lock:
            output = []
            for key, value in sorted(source.items(), key=lambda item: item[0]):
                latencies = list(value["latencies"])
                output.append(
                    (
                        key,
                        {
                            "count": value["count"],
                            "ok": value["ok"],
                            "errors": value["errors"],
                            "p50": percentile(latencies, 50),
                            "p95": percentile(latencies, 95),
                            "max": max(latencies) if latencies else 0.0,
                        },
                    )
                )
            return output

    def stream_summary(self) -> list[tuple[str, dict[str, Any]]]:
        source = self.by_stream_mode
        with self._lock:
            output = []
            for key, value in sorted(source.items(), key=lambda item: item[0]):
                latencies = list(value["latencies"])
                output.append(
                    (
                        key,
                        {
                            "count": value["count"],
                            "ok": value["ok"],
                            "errors": value["errors"],
                            "p50": percentile(latencies, 50),
                            "p95": percentile(latencies, 95),
                            "max": max(latencies) if latencies else 0.0,
                        },
                    )
                )
            return output

    def sampled_errors(self) -> list[dict[str, Any]]:
        with self._lock:
            return list(self.error_samples)


def choose_stream(args: argparse.Namespace) -> bool:
    if args.stream_mode == "stream":
        return True
    if args.stream_mode == "non_stream":
        return False
    return random.random() < args.stream_ratio


def truncate_detail(value: str, max_length: int = 800) -> str:
    value = " ".join(value.replace("\r", "\n").split())
    if len(value) <= max_length:
        return value
    return value[: max_length - 3] + "..."


def post_chat_completion(base_url: str, token: str, model: str, prompt: str, timeout: float, stream: bool) -> tuple[str, float, bool, str]:
    url = base_url.rstrip("/") + "/v1/chat/completions"
    payload: dict[str, Any] = {
        "model": model,
        "messages": [{"role": "user", "content": prompt}],
        "temperature": 0,
        "max_tokens": 16,
    }
    if stream:
        payload["stream"] = True
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    request = urllib.request.Request(
        url,
        data=body,
        method="POST",
        headers={
            "Authorization": f"Bearer {token}",
            "Content-Type": "application/json",
            "Accept": "application/json",
        },
    )
    started = time.monotonic()
    status = "error"
    ok = False
    detail = ""
    try:
        with urllib.request.urlopen(request, timeout=timeout) as response:
            response.read()
            status = str(response.status)
            ok = 200 <= response.status < 300
    except urllib.error.HTTPError as exc:
        raw = exc.read()
        status = str(exc.code)
        detail = truncate_detail(raw.decode("utf-8", errors="replace") if raw else str(exc))
    except Exception as exc:  # noqa: BLE001 - summarized as a status bucket for load tests.
        status = type(exc).__name__
        detail = truncate_detail(str(exc))
    return status, time.monotonic() - started, ok, detail


def request_task(args: argparse.Namespace, user: dict[str, Any], offered_rpm: float, stats: Stats, semaphore: threading.Semaphore) -> None:
    try:
        stream = choose_stream(args)
        stream_mode = "stream" if stream else "non_stream"
        status, latency, ok, detail = post_chat_completion(
            args.base_url,
            str(user["token"]),
            args.model,
            args.prompt,
            args.timeout,
            stream,
        )
        stats.record(
            status,
            latency,
            offered_rpm,
            stream_mode,
            ok,
            str(user.get("username") or user.get("id") or ""),
            detail,
        )
    finally:
        semaphore.release()


def user_scheduler(
    args: argparse.Namespace,
    user: dict[str, Any],
    offered_rpm: float,
    stats: Stats,
    executor: concurrent.futures.ThreadPoolExecutor,
    end_at: float,
) -> None:
    if offered_rpm <= 0:
        return
    interval = 60.0 / offered_rpm
    semaphore = threading.Semaphore(args.max_in_flight_per_user)
    time.sleep(random.uniform(0, args.start_jitter))
    next_at = time.monotonic()
    while time.monotonic() < end_at:
        if semaphore.acquire(blocking=False):
            stats.mark_sent()
            executor.submit(request_task, args, user, offered_rpm, stats, semaphore)
        else:
            stats.mark_skipped()
        next_at += interval
        sleep_for = next_at - time.monotonic()
        if sleep_for > 0:
            time.sleep(sleep_for)
        else:
            next_at = time.monotonic()


def reporter(stats: Stats, interval: float, stop_event: threading.Event) -> None:
    while not stop_event.wait(interval):
        snap = stats.snapshot()
        print(
            "[queue-load] "
            f"elapsed={snap['elapsed']:.1f}s sent={snap['sent']} completed={snap['completed']} "
            f"ok={snap['ok']} errors={snap['errors']} skipped={snap['skipped']} "
            f"p50={snap['p50']:.3f}s p95={snap['p95']:.3f}s max={snap['max']:.3f}s "
            f"statuses={snap['statuses']} streams={snap['streams']}",
            flush=True,
        )


def load_users(tokens_file: str, limit: int | None) -> tuple[dict[str, Any], list[dict[str, Any]]]:
    with open(tokens_file, "r", encoding="utf-8") as handle:
        payload = json.load(handle)
    users = payload.get("users")
    if not isinstance(users, list) or not users:
        raise RuntimeError(f"no users with tokens found in {tokens_file}")
    clean_users = [user for user in users if isinstance(user, dict) and user.get("token")]
    if limit is not None:
        clean_users = clean_users[:limit]
    if not clean_users:
        raise RuntimeError(f"no usable token records found in {tokens_file}")
    return payload, clean_users


def build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(description="Run concurrent queue load-test traffic.")
    parser.add_argument("--base-url", default="http://127.0.0.1:3000")
    parser.add_argument("--tokens-file", default=".codex-deploy/queue-loadtest/tokens.json")
    parser.add_argument("--duration", type=float, default=120.0)
    parser.add_argument("--rpm-pattern", default="5", help="Offered RPM pattern assigned across users")
    parser.add_argument("--model", default=None, help="Defaults to the model in the token file")
    parser.add_argument("--prompt", default="Reply with a short queue test acknowledgement.")
    parser.add_argument("--timeout", type=float, default=900.0)
    parser.add_argument("--report-interval", type=float, default=10.0)
    parser.add_argument("--start-jitter", type=float, default=2.0)
    parser.add_argument("--max-in-flight-per-user", type=int, default=8)
    parser.add_argument("--max-workers", type=int, default=256)
    parser.add_argument("--limit-users", type=int, default=None)
    parser.add_argument("--stream-mode", choices=["random", "stream", "non-stream", "non_stream"], default="random")
    parser.add_argument("--stream-ratio", type=float, default=0.5, help="Probability of stream requests when --stream-mode=random")
    parser.add_argument("--stream", action="store_true", help="Compatibility alias for --stream-mode=stream")
    parser.add_argument("--error-sample-limit", type=int, default=10)
    return parser


def main() -> int:
    parser = build_parser()
    args = parser.parse_args()
    if args.duration <= 0:
        parser.error("--duration must be positive")
    if args.max_in_flight_per_user <= 0:
        parser.error("--max-in-flight-per-user must be positive")
    if args.stream:
        args.stream_mode = "stream"
    if args.stream_mode == "non-stream":
        args.stream_mode = "non_stream"
    if args.stream_ratio < 0 or args.stream_ratio > 1:
        parser.error("--stream-ratio must be between 0 and 1")
    if args.error_sample_limit < 0:
        parser.error("--error-sample-limit cannot be negative")

    rpm_pattern = parse_rpm_pattern(args.rpm_pattern)
    token_payload, users = load_users(args.tokens_file, args.limit_users)
    if args.model is None:
        args.model = token_payload.get("model") or "gpt-4o-mini"

    assignments: list[tuple[dict[str, Any], float]] = []
    for index, user in enumerate(users, start=1):
        offered_rpm = rpm_pattern[(index - 1) % len(rpm_pattern)]
        assignments.append((user, offered_rpm))

    total_offered = sum(item[1] for item in assignments)
    print(
        f"[queue-load] users={len(assignments)} duration={args.duration:.1f}s "
        f"model={args.model} total_offered_rpm={total_offered:g} "
        f"stream_mode={args.stream_mode} stream_ratio={args.stream_ratio:g}",
        flush=True,
    )

    stats = Stats(args.error_sample_limit)
    stop_event = threading.Event()
    reporter_thread = threading.Thread(target=reporter, args=(stats, args.report_interval, stop_event), daemon=True)
    reporter_thread.start()
    end_at = time.monotonic() + args.duration
    scheduler_threads: list[threading.Thread] = []
    max_workers = max(1, args.max_workers)
    with concurrent.futures.ThreadPoolExecutor(max_workers=max_workers) as executor:
        for user, offered_rpm in assignments:
            thread = threading.Thread(
                target=user_scheduler,
                args=(args, user, offered_rpm, stats, executor, end_at),
                name=f"scheduler-{user.get('username')}",
                daemon=True,
            )
            scheduler_threads.append(thread)
            thread.start()
        for thread in scheduler_threads:
            thread.join()
    stop_event.set()
    reporter_thread.join(timeout=2)

    snap = stats.snapshot()
    print(
        "[queue-load] final "
        f"elapsed={snap['elapsed']:.1f}s sent={snap['sent']} completed={snap['completed']} "
        f"ok={snap['ok']} errors={snap['errors']} skipped={snap['skipped']} "
        f"p50={snap['p50']:.3f}s p95={snap['p95']:.3f}s max={snap['max']:.3f}s "
        f"statuses={snap['statuses']} streams={snap['streams']}",
        flush=True,
    )
    print("[queue-load] by offered rpm", flush=True)
    for rpm, summary in stats.grouped_summary():
        print(
            f"  rpm={rpm:g} count={summary['count']} ok={summary['ok']} errors={summary['errors']} "
            f"p50={summary['p50']:.3f}s p95={summary['p95']:.3f}s max={summary['max']:.3f}s",
            flush=True,
        )
    print("[queue-load] by stream mode", flush=True)
    for mode, summary in stats.stream_summary():
        print(
            f"  mode={mode} count={summary['count']} ok={summary['ok']} errors={summary['errors']} "
            f"p50={summary['p50']:.3f}s p95={summary['p95']:.3f}s max={summary['max']:.3f}s",
            flush=True,
        )
    error_samples = stats.sampled_errors()
    if error_samples:
        print("[queue-load] error samples", flush=True)
        for sample in error_samples:
            print(
                "  "
                f"status={sample['status']} user={sample['username']} rpm={sample['rpm']:g} "
                f"mode={sample['stream']} latency={sample['latency']:.3f}s "
                f"detail={sample['detail'] or '-'}",
                flush=True,
            )
    if snap["errors"] > 0:
        return 2
    return 0


if __name__ == "__main__":
    try:
        raise SystemExit(main())
    except Exception as exc:
        print(f"[queue-load] ERROR: {exc}", file=sys.stderr)
        raise SystemExit(1)
