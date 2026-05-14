#!/usr/bin/env python3
"""Run local OpenAI-compatible mock upstream servers for queue tests."""

from __future__ import annotations

import argparse
import json
import random
import signal
import sys
import threading
import time
import uuid
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from typing import Any


class SharedStats:
    def __init__(self) -> None:
        self._lock = threading.Lock()
        self._counts: dict[int, int] = {}
        self._started = time.time()

    def increment(self, port: int) -> None:
        with self._lock:
            self._counts[port] = self._counts.get(port, 0) + 1

    def snapshot(self) -> tuple[float, dict[int, int]]:
        with self._lock:
            return time.time() - self._started, dict(self._counts)


def parse_ports(values: list[str]) -> list[int]:
    raw = ",".join(values)
    ports: list[int] = []
    for item in raw.replace(" ", ",").split(","):
        item = item.strip()
        if not item:
            continue
        port = int(item)
        if port <= 0 or port > 65535:
            raise argparse.ArgumentTypeError(f"invalid port: {port}")
        ports.append(port)
    if not ports:
        raise argparse.ArgumentTypeError("at least one port is required")
    return ports


def write_json(handler: BaseHTTPRequestHandler, status: int, payload: dict[str, Any]) -> None:
    body = json.dumps(payload, separators=(",", ":")).encode("utf-8")
    handler.send_response(status)
    handler.send_header("Content-Type", "application/json")
    handler.send_header("Content-Length", str(len(body)))
    handler.end_headers()
    handler.wfile.write(body)


def ok_chunks(count: int) -> list[str]:
    return ["OK "] * max(0, count - 1) + (["OK"] if count > 0 else [])


def ok_text(count: int) -> str:
    return "".join(ok_chunks(count))


def token_interval_seconds(args: argparse.Namespace) -> float:
    if args.tokens_per_second <= 0:
        return 0.0
    return 1.0 / args.tokens_per_second


def sleep_completion_tokens(args: argparse.Namespace) -> None:
    if args.tokens_per_second <= 0 or args.ok_count <= 0:
        return
    time.sleep(args.ok_count / args.tokens_per_second)


def make_handler(args: argparse.Namespace, stats: SharedStats) -> type[BaseHTTPRequestHandler]:
    class MockOpenAIHandler(BaseHTTPRequestHandler):
        server_version = "newapi-queue-mock/1.0"
        protocol_version = "HTTP/1.1"

        def log_message(self, fmt: str, *fmt_args: Any) -> None:
            if args.quiet:
                return
            sys.stderr.write(
                "[mock-openai:%s] %s - %s\n"
                % (self.server.server_port, self.address_string(), fmt % fmt_args)
            )

        def do_GET(self) -> None:
            if self.path in {"/health", "/healthz"}:
                write_json(self, 200, {"ok": True, "port": self.server.server_port})
                return
            if self.path.rstrip("/") in {"/v1/models", "/models"}:
                write_json(
                    self,
                    200,
                    {
                        "object": "list",
                        "data": [
                            {
                                "id": args.model,
                                "object": "model",
                                "created": 1_700_000_000,
                                "owned_by": "newapi-queue-loadtest",
                            }
                        ],
                    },
                )
                return
            write_json(self, 404, {"error": {"message": f"unknown path: {self.path}"}})

        def do_POST(self) -> None:
            content_length = int(self.headers.get("Content-Length", "0") or "0")
            raw_body = self.rfile.read(content_length) if content_length else b"{}"
            try:
                parsed = json.loads(raw_body.decode("utf-8") or "{}")
            except json.JSONDecodeError:
                parsed = {}
            request = parsed if isinstance(parsed, dict) else {}

            if args.failure_rate > 0 and random.random() < args.failure_rate:
                write_json(self, 500, {"error": {"message": "injected mock failure"}})
                return

            delay = args.delay_ms / 1000.0
            if args.jitter_ms > 0:
                delay += random.uniform(0, args.jitter_ms / 1000.0)
            if delay > 0:
                time.sleep(delay)

            normalized_path = self.path.split("?", 1)[0].rstrip("/")
            if normalized_path in {"/v1/responses", "/responses"}:
                self._handle_response(request)
                return
            self._handle_chat_completion(request)

        def _handle_chat_completion(self, request: dict[str, Any]) -> None:
            port = int(self.server.server_port)
            stats.increment(port)
            model = str(request.get("model") or args.model)
            created = int(time.time())
            content = ok_text(args.ok_count)
            if bool(request.get("stream")):
                self._write_chat_stream(model, created)
                return
            sleep_completion_tokens(args)

            write_json(
                self,
                200,
                {
                    "id": f"chatcmpl-mock-{uuid.uuid4().hex[:16]}",
                    "object": "chat.completion",
                    "created": created,
                    "model": model,
                    "choices": [
                        {
                            "index": 0,
                            "message": {"role": "assistant", "content": content},
                            "finish_reason": "stop",
                        }
                    ],
                    "usage": {
                        "prompt_tokens": 8,
                        "completion_tokens": args.ok_count,
                        "total_tokens": args.ok_count + 8,
                    },
                },
            )

        def _handle_response(self, request: dict[str, Any]) -> None:
            port = int(self.server.server_port)
            stats.increment(port)
            model = str(request.get("model") or args.model)
            if bool(request.get("stream")):
                self._write_response_stream(model)
                return
            sleep_completion_tokens(args)
            write_json(
                self,
                200,
                {
                    "id": f"resp_mock_{uuid.uuid4().hex[:16]}",
                    "object": "response",
                    "created_at": int(time.time()),
                    "model": model,
                    "status": "completed",
                    "output": [
                        {
                            "type": "message",
                            "role": "assistant",
                            "content": [
                                {
                                    "type": "output_text",
                                    "text": ok_text(args.ok_count),
                                }
                            ],
                        }
                    ],
                    "usage": {
                        "input_tokens": 8,
                        "output_tokens": args.ok_count,
                        "total_tokens": args.ok_count + 8,
                    },
                },
            )

        def _write_chat_stream(self, model: str, created: int) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True

            chunks = [{"role": "assistant", "content": ""}]
            chunks.extend({"content": chunk} for chunk in ok_chunks(args.ok_count))
            interval = token_interval_seconds(args)
            for delta in chunks:
                if delta.get("content"):
                    time.sleep(interval)
                payload = {
                    "id": f"chatcmpl-mock-{uuid.uuid4().hex[:16]}",
                    "object": "chat.completion.chunk",
                    "created": created,
                    "model": model,
                    "choices": [{"index": 0, "delta": delta, "finish_reason": None}],
                }
                self.wfile.write(f"data: {json.dumps(payload, separators=(',', ':'))}\n\n".encode())
                self.wfile.flush()
            done = {
                "id": f"chatcmpl-mock-{uuid.uuid4().hex[:16]}",
                "object": "chat.completion.chunk",
                "created": created,
                "model": model,
                "choices": [{"index": 0, "delta": {}, "finish_reason": "stop"}],
            }
            self.wfile.write(f"data: {json.dumps(done, separators=(',', ':'))}\n\n".encode())
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()

        def _write_response_stream(self, model: str) -> None:
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Cache-Control", "no-cache")
            self.send_header("Connection", "close")
            self.end_headers()
            self.close_connection = True

            response_id = f"resp_mock_{uuid.uuid4().hex[:16]}"
            created = int(time.time())
            events = [
                (
                    "response.created",
                    {
                        "type": "response.created",
                        "response": {
                            "id": response_id,
                            "object": "response",
                            "created_at": created,
                            "model": model,
                            "status": "in_progress",
                        },
                    },
                )
            ]
            events.extend(
                (
                    "response.output_text.delta",
                    {
                        "type": "response.output_text.delta",
                        "item_id": "msg_mock",
                        "output_index": 0,
                        "content_index": 0,
                        "delta": chunk,
                    },
                )
                for chunk in ok_chunks(args.ok_count)
            )
            events.append(
                (
                    "response.completed",
                    {
                        "type": "response.completed",
                        "response": {
                            "id": response_id,
                            "object": "response",
                            "created_at": created,
                            "model": model,
                            "status": "completed",
                        },
                    },
                )
            )

            for event_name, payload in events:
                if event_name == "response.output_text.delta":
                    time.sleep(token_interval_seconds(args))
                self.wfile.write(f"event: {event_name}\n".encode())
                self.wfile.write(f"data: {json.dumps(payload, separators=(',', ':'))}\n\n".encode())
                self.wfile.flush()
            self.wfile.write(b"data: [DONE]\n\n")
            self.wfile.flush()

    return MockOpenAIHandler


def stats_loop(stats: SharedStats, interval: float, stop_event: threading.Event) -> None:
    while not stop_event.wait(interval):
        elapsed, counts = stats.snapshot()
        total = sum(counts.values())
        per_port = " ".join(f"{port}={counts.get(port, 0)}" for port in sorted(counts))
        print(f"[mock-openai] elapsed={elapsed:.1f}s total={total} {per_port}", flush=True)


def main() -> int:
    parser = argparse.ArgumentParser(description="Run local OpenAI-compatible mock upstream servers.")
    parser.add_argument("ports", nargs="+", help="Ports to bind, e.g. 12000 12001 12003")
    parser.add_argument("--model", default="gpt-4o-mini", help="Model ID exposed by /v1/models")
    parser.add_argument("--ok-count", type=int, default=100, help="Number of OK tokens returned per completion")
    parser.add_argument("--tokens-per-second", type=float, default=10.0, help="Mock completion speed; 0 disables token pacing")
    parser.add_argument("--delay-ms", type=float, default=50.0, help="Fixed upstream response delay")
    parser.add_argument("--jitter-ms", type=float, default=0.0, help="Additional random response delay")
    parser.add_argument("--failure-rate", type=float, default=0.0, help="0..1 injected HTTP 500 rate")
    parser.add_argument("--stats-interval", type=float, default=5.0, help="Per-port stats print interval")
    parser.add_argument("--quiet", action="store_true", help="Disable per-request HTTP logs")
    args = parser.parse_args()
    if args.ok_count < 0:
        parser.error("--ok-count cannot be negative")
    if args.tokens_per_second < 0:
        parser.error("--tokens-per-second cannot be negative")

    try:
        ports = parse_ports(args.ports)
    except Exception as exc:
        parser.error(str(exc))

    stats = SharedStats()
    stop_event = threading.Event()
    handler = make_handler(args, stats)
    servers: list[ThreadingHTTPServer] = []
    threads: list[threading.Thread] = []

    def stop(_signum: int | None = None, _frame: Any | None = None) -> None:
        stop_event.set()
        for server in servers:
            server.shutdown()

    signal.signal(signal.SIGINT, stop)
    signal.signal(signal.SIGTERM, stop)

    for port in ports:
        server = ThreadingHTTPServer(("127.0.0.1", port), handler)
        servers.append(server)
        thread = threading.Thread(target=server.serve_forever, name=f"mock-openai-{port}", daemon=True)
        thread.start()
        threads.append(thread)
        print(f"[mock-openai] listening on http://127.0.0.1:{port}", flush=True)

    reporter = threading.Thread(target=stats_loop, args=(stats, args.stats_interval, stop_event), daemon=True)
    reporter.start()

    try:
        while not stop_event.is_set():
            time.sleep(0.5)
    finally:
        stop()
        for thread in threads:
            thread.join(timeout=2)
        for server in servers:
            server.server_close()
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
