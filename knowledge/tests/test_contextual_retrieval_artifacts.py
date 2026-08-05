#
# Tencent is pleased to support the open source community by making trpc-agent-go available.
#
# Copyright (C) 2025 Tencent.  All rights reserved.
#
# trpc-agent-go is licensed under the Apache License Version 2.0.
#
#
"""Tests for strict contextual-retrieval artifact helpers."""

import unittest

from contextual_retrieval.artifacts import (
    endpoint_identity,
    public_endpoint_identity,
    text_digest,
)


class ArtifactHelpersTest(unittest.TestCase):
    def test_endpoint_identity_is_secret_free_and_path_sensitive(self):
        cases = (
            ("", ""),
            (
                "https://user:password@example.test:8443/secret/path"
                "?token=value#private",
                "https://example.test:8443|path_sha256="
                + text_digest("/secret/path"),
            ),
            (
                "user:password@embedding.test:9443/private/v1"
                "?token=value#private",
                "embedding.test:9443|path_sha256="
                + text_digest("/private/v1"),
            ),
            (
                "http://[2001:db8::1]:8080/v1",
                "http://[2001:db8::1]:8080|path_sha256="
                + text_digest("/v1"),
            ),
            ("https://example.test/", "https://example.test"),
            ("/v1", "invalid_endpoint"),
            ("unix:///var/run/service.sock", "invalid_endpoint"),
            ("file:///tmp/config", "invalid_endpoint"),
            ("https://example.test:bad/v1", "invalid_endpoint"),
        )
        for value, expected in cases:
            with self.subTest(value=value):
                identity = endpoint_identity(value)
                self.assertEqual(expected, identity)
                for secret in (
                    "user",
                    "password",
                    "secret",
                    "private",
                    "token=value",
                ):
                    self.assertNotIn(secret, identity)

    def test_public_endpoint_identity_accepts_raw_or_prehashed_path(self):
        path_hash = text_digest("/private/v1")
        expected = "https://example.test|path_sha256=" + path_hash
        self.assertEqual(
            expected,
            public_endpoint_identity(
                "https://user:password@EXAMPLE.test/private/v1"
                "?token=value#fragment"
            ),
        )
        self.assertEqual(expected, public_endpoint_identity(expected))
        self.assertEqual(
            expected,
            public_endpoint_identity(
                "https://example.test/private/v1?another=value#other"
            ),
        )
        self.assertNotEqual(
            expected,
            public_endpoint_identity("https://example.test/private/v2"),
        )

    def test_public_endpoint_identity_rejects_invalid_values(self):
        for value in (
            "",
            "invalid_endpoint",
            "/relative/path",
            "unix:///var/run/service.sock",
            "https://example.test|path_sha256=not-a-digest",
        ):
            with self.subTest(value=value):
                with self.assertRaises(ValueError):
                    public_endpoint_identity(value)


if __name__ == "__main__":
    unittest.main()
