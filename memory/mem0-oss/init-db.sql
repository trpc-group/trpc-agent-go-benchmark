-- Tencent is pleased to support the open source community by making
-- trpc-agent-go-benchmark available.
--
-- Copyright (C) 2026 Tencent. All rights reserved.
--
-- trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

CREATE EXTENSION IF NOT EXISTS vector;

SELECT 'CREATE DATABASE mem0_app'
WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = 'mem0_app')\gexec
