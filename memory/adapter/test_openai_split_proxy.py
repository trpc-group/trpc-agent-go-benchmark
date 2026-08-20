#!/usr/bin/env python3
#
# Tencent is pleased to support the open source community by making
# trpc-agent-go available.
#
# Copyright (C) 2025 Tencent. All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
"""Tests for the authenticated OpenAI split proxy."""

import json
import os
import tempfile
import threading
import unittest
import urllib.error
import urllib.request
from http.server import BaseHTTPRequestHandler, HTTPServer
from unittest import mock

from . import openai_split_proxy as proxy


class _UpstreamHandler(BaseHTTPRequestHandler):
    calls = []

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        type(self).calls.append((self.path, self.headers.get("Authorization"), body))
        data = json.dumps(
            {
                "data": [],
                "usage": {"prompt_tokens": 2, "completion_tokens": 1, "total_tokens": 3},
            }
        ).encode("utf-8")
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def log_message(self, _format, *_args):
        return


class SplitProxyTest(unittest.TestCase):
    def setUp(self):
        _UpstreamHandler.calls = []
        self.upstream = HTTPServer(("127.0.0.1", 0), _UpstreamHandler)
        self.upstream_thread = threading.Thread(target=self.upstream.serve_forever, daemon=True)
        self.upstream_thread.start()
        self.temp_dir = tempfile.TemporaryDirectory()
        self.usage_log = os.path.join(self.temp_dir.name, "usage.jsonl")
        self.server = proxy.ThreadingHTTPServer(("127.0.0.1", 0), proxy.SplitProxyHandler)
        self.server.usage_log = self.usage_log
        self.server.usage_lock = threading.Lock()
        self.server.run_id = "run-test-1"
        self.server.auth_token = "local-proxy-secret"
        self.server.max_request_bytes = 1024
        self.server.request_semaphore = threading.BoundedSemaphore(2)
        self.server.upstream_timeout = 5
        self.server.quiet = True
        self.server_thread = threading.Thread(target=self.server.serve_forever, daemon=True)
        self.server_thread.start()
        upstream_url = "http://127.0.0.1:%d" % self.upstream.server_port
        self.env = mock.patch.dict(
            os.environ,
            {
                "LLM_BASE_URL": upstream_url,
                "LLM_API_KEY": "real-upstream-secret",
                "OPENAI_BASE_URL": upstream_url,
                "OPENAI_API_KEY": "real-embedding-secret",
            },
            clear=False,
        )
        self.env.start()

    def tearDown(self):
        self.env.stop()
        self.server.shutdown()
        self.server.server_close()
        self.upstream.shutdown()
        self.upstream.server_close()
        self.temp_dir.cleanup()

    def _request(self, path, body=b"{}", token="local-proxy-secret"):
        headers = {"Content-Type": "application/json"}
        if token is not None:
            headers["Authorization"] = "Bearer " + token
        request = urllib.request.Request(
            "http://127.0.0.1:%d%s" % (self.server.server_port, path),
            data=body,
            headers=headers,
            method="POST",
        )
        return urllib.request.urlopen(request, timeout=5)

    def test_rejects_missing_and_incorrect_bearer_tokens(self):
        for token in (None, "wrong-secret"):
            with self.subTest(token=token):
                with self.assertRaises(urllib.error.HTTPError) as caught:
                    self._request("/v1/chat/completions", token=token)
                self.assertEqual(caught.exception.code, 401)
        self.assertEqual(_UpstreamHandler.calls, [])

    def test_forwards_with_upstream_key_and_records_scoped_usage(self):
        body = json.dumps({"model": "test-model"}).encode("utf-8")
        with self._request("/v1/chat/completions", body=body) as response:
            self.assertEqual(response.status, 200)
        self.assertEqual(len(_UpstreamHandler.calls), 1)
        path, authorization, forwarded_body = _UpstreamHandler.calls[0]
        self.assertEqual(path, "/v1/chat/completions")
        self.assertEqual(authorization, "Bearer real-upstream-secret")
        self.assertEqual(forwarded_body, body)
        with open(self.usage_log, encoding="utf-8") as usage_file:
            usage = json.loads(usage_file.readline())
        self.assertEqual(usage["run_id"], "run-test-1")
        self.assertEqual(usage["kind"], "chat")
        self.assertEqual(usage["usage"]["total_tokens"], 3)

    def test_rejects_oversized_request_before_forwarding(self):
        with self.assertRaises(urllib.error.HTTPError) as caught:
            self._request("/v1/embeddings", body=b"x" * 1025)
        self.assertEqual(caught.exception.code, 413)
        self.assertEqual(_UpstreamHandler.calls, [])

    def test_rejects_request_when_concurrency_is_exhausted(self):
        self.assertTrue(self.server.request_semaphore.acquire(blocking=False))
        self.assertTrue(self.server.request_semaphore.acquire(blocking=False))
        try:
            with self.assertRaises(urllib.error.HTTPError) as caught:
                self._request("/v1/embeddings")
            self.assertEqual(caught.exception.code, 429)
        finally:
            self.server.request_semaphore.release()
            self.server.request_semaphore.release()
        self.assertEqual(_UpstreamHandler.calls, [])

    def test_loopback_detection(self):
        for host in ("127.0.0.1", "::1", "localhost"):
            with self.subTest(host=host):
                self.assertTrue(proxy._is_loopback_host(host))
        for host in ("0.0.0.0", "192.0.2.1", "proxy.example"):
            with self.subTest(host=host):
                self.assertFalse(proxy._is_loopback_host(host))


if __name__ == "__main__":
    unittest.main()
