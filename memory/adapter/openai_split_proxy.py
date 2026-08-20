#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent. All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
"""Small OpenAI-compatible split proxy for local Mem0 OSS benchmarks."""

import argparse
import hmac
import ipaddress
import json
import os
import re
import sys
import threading
import time
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from pathlib import Path
from socketserver import ThreadingMixIn


_DEFAULT_MAX_REQUEST_BYTES = 16 * 1024 * 1024
_DEFAULT_MAX_CONCURRENCY = 16
_RUN_ID_PATTERN = re.compile(r"^[A-Za-z0-9._-]{1,128}$")


def _env(name):
    return os.environ.get(name, "").strip()


def _join_url(base, path):
    base = base.rstrip("/")
    if base.endswith("/v1") and path.startswith("/v1/"):
        return base + path[3:]
    return base + path


def _usage_from_response(data):
    try:
        payload = json.loads(data.decode("utf-8"))
    except Exception:
        return {}, False
    usage = payload.get("usage")
    if not isinstance(usage, dict):
        return {}, False
    out = {
        "prompt_tokens": int(usage.get("prompt_tokens") or 0),
        "completion_tokens": int(usage.get("completion_tokens") or 0),
        "total_tokens": int(usage.get("total_tokens") or 0),
    }
    details = usage.get("prompt_tokens_details")
    if isinstance(details, dict):
        out["cached_tokens"] = int(details.get("cached_tokens") or 0)
    return out, True


def _is_loopback_host(host):
    host = host.strip().lower()
    if host == "localhost":
        return True
    try:
        return ipaddress.ip_address(host).is_loopback
    except ValueError:
        return False


def _bearer_token(headers):
    authorization = headers.get("Authorization", "")
    scheme, separator, token = authorization.partition(" ")
    if separator and scheme.lower() == "bearer":
        return token.strip()
    return ""


class SplitProxyHandler(BaseHTTPRequestHandler):
    server_version = "OpenAISplitProxy/1.0"

    def do_GET(self):
        if self.path == "/health":
            self._write_json(200, {"ok": True})
            return
        self._write_json(404, {"error": "not found"})

    def do_POST(self):
        if not self._authorized():
            self._write_json(
                401,
                {"error": "unauthorized"},
                {"WWW-Authenticate": "Bearer"},
            )
            return
        semaphore = self.server.request_semaphore
        if not semaphore.acquire(blocking=False):
            self._write_json(429, {"error": "proxy concurrency limit reached"})
            return
        try:
            if self.path == "/v1/chat/completions":
                self._forward("chat", _env("LLM_BASE_URL"), _env("LLM_API_KEY"))
                return
            if self.path == "/v1/embeddings":
                self._forward("embedding", _env("OPENAI_BASE_URL"), _env("OPENAI_API_KEY"))
                return
            self._write_json(404, {"error": "unsupported path"})
        finally:
            semaphore.release()

    def log_message(self, fmt, *args):
        if getattr(self.server, "quiet", False):
            return
        BaseHTTPRequestHandler.log_message(self, fmt, *args)

    def _authorized(self):
        supplied = _bearer_token(self.headers)
        expected = self.server.auth_token
        return bool(supplied) and hmac.compare_digest(supplied, expected)

    def _forward(self, kind, base_url, api_key):
        if not base_url:
            self._write_json(500, {"error": "%s base url is not configured" % kind})
            return
        raw_length = self.headers.get("Content-Length")
        if raw_length is None:
            self._write_json(411, {"error": "Content-Length is required"})
            return
        try:
            length = int(raw_length)
        except ValueError:
            self._write_json(400, {"error": "invalid Content-Length"})
            return
        if length < 0:
            self._write_json(400, {"error": "invalid Content-Length"})
            return
        if length > self.server.max_request_bytes:
            self._write_json(413, {"error": "request body is too large"})
            return
        body = self.rfile.read(length)
        target = _join_url(base_url, self.path)
        headers = {
            "Content-Type": "application/json",
            "Accept": "application/json",
        }
        if api_key:
            headers["Authorization"] = "Bearer %s" % api_key
        req = urllib.request.Request(target, data=body, headers=headers, method="POST")
        status = 502
        resp_body = b""
        resp_headers = {}
        started = time.time()
        try:
            with urllib.request.urlopen(req, timeout=getattr(self.server, "upstream_timeout", 600)) as resp:
                status = resp.status
                resp_body = resp.read()
                resp_headers = dict(resp.headers.items())
        except urllib.error.HTTPError as exc:
            status = exc.code
            resp_body = exc.read()
            resp_headers = dict(exc.headers.items())
        except Exception:
            resp_body = json.dumps({"error": "upstream request failed"}).encode("utf-8")
        usage, tokens_known = _usage_from_response(resp_body)
        self._record_usage(kind, body, status, usage, tokens_known, int((time.time() - started) * 1000))
        self.send_response(status)
        self.send_header("Content-Type", resp_headers.get("Content-Type", "application/json"))
        self.send_header("Content-Length", str(len(resp_body)))
        self.end_headers()
        self.wfile.write(resp_body)

    def _record_usage(self, kind, request_body, status, usage, tokens_known, latency_ms):
        usage_log = getattr(self.server, "usage_log", "")
        if not usage_log:
            return
        model = ""
        try:
            payload = json.loads(request_body.decode("utf-8"))
            model = str(payload.get("model") or "")
        except Exception:
            pass
        entry = {
            "ts": time.time(),
            "run_id": self.server.run_id,
            "kind": kind,
            "path": self.path,
            "model": model,
            "status": status,
            "usage": usage,
            "tokens_known": tokens_known,
            "latency_ms": latency_ms,
        }
        path = Path(usage_log)
        if path.parent:
            path.parent.mkdir(parents=True, exist_ok=True)
        with self.server.usage_lock:
            with path.open("a", encoding="utf-8") as f:
                f.write(json.dumps(entry, ensure_ascii=False) + "\n")

    def _write_json(self, status, payload, headers=None):
        data = json.dumps(payload).encode("utf-8")
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        for name, value in (headers or {}).items():
            self.send_header(name, value)
        self.end_headers()
        self.wfile.write(data)


class ThreadingHTTPServer(ThreadingMixIn, HTTPServer):
    daemon_threads = True


def main(argv):
    parser = argparse.ArgumentParser()
    parser.add_argument("--host", default="127.0.0.1")
    parser.add_argument("--port", type=int, default=18080)
    parser.add_argument("--usage-log", default="")
    parser.add_argument("--run-id", default="")
    parser.add_argument("--auth-token-env", default="MEM0_PROXY_API_KEY")
    parser.add_argument("--max-request-bytes", type=int, default=_DEFAULT_MAX_REQUEST_BYTES)
    parser.add_argument("--max-concurrency", type=int, default=_DEFAULT_MAX_CONCURRENCY)
    parser.add_argument("--allow-non-loopback", action="store_true")
    parser.add_argument("--upstream-timeout", type=float, default=600)
    parser.add_argument("--quiet", action="store_true")
    args = parser.parse_args(argv)

    if not _is_loopback_host(args.host) and not args.allow_non_loopback:
        parser.error("non-loopback binding requires --allow-non-loopback")
    auth_token = _env(args.auth_token_env)
    if not auth_token:
        parser.error("%s must contain the split-proxy bearer token" % args.auth_token_env)
    if args.max_request_bytes <= 0:
        parser.error("--max-request-bytes must be positive")
    if args.max_concurrency <= 0:
        parser.error("--max-concurrency must be positive")
    run_id = args.run_id.strip()
    if args.usage_log and not run_id:
        parser.error("--run-id is required when --usage-log is configured")
    if run_id and not _RUN_ID_PATTERN.fullmatch(run_id):
        parser.error("--run-id must match [A-Za-z0-9._-]{1,128}")

    server = ThreadingHTTPServer((args.host, args.port), SplitProxyHandler)
    server.usage_log = args.usage_log
    server.usage_lock = threading.Lock()
    server.run_id = run_id
    server.auth_token = auth_token
    server.max_request_bytes = args.max_request_bytes
    server.request_semaphore = threading.BoundedSemaphore(args.max_concurrency)
    server.upstream_timeout = args.upstream_timeout
    server.quiet = args.quiet
    print("split proxy listening on http://%s:%d" % (args.host, args.port))
    try:
        server.serve_forever()
    except KeyboardInterrupt:
        return 130
    return 0


if __name__ == "__main__":
    raise SystemExit(main(sys.argv[1:]))
