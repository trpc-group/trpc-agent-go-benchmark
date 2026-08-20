#!/bin/sh
# Tencent is pleased to support the open source community by making
# trpc-agent-go-benchmark available.
#
# Copyright (C) 2026 Tencent. All rights reserved.
#
# trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

set -eu
umask 077

: "${POSTGRES_PASSWORD:?POSTGRES_PASSWORD is required}"
: "${OPENAI_API_KEY:?OPENAI_API_KEY is required}"
: "${OPENAI_BASE_URL:?OPENAI_BASE_URL is required}"
: "${MEM0_DEFAULT_LLM_MODEL:?MEM0_DEFAULT_LLM_MODEL is required}"
: "${MEM0_DEFAULT_EMBEDDER_MODEL:?MEM0_DEFAULT_EMBEDDER_MODEL is required}"
: "${POSTGRES_COLLECTION_NAME:?POSTGRES_COLLECTION_NAME is required}"

if [ "${AUTH_DISABLED:-}" != "true" ]; then
    echo "AUTH_DISABLED must be true for the locked benchmark environment" >&2
    exit 1
fi
if [ "${MEM0_TELEMETRY:-}" != "false" ]; then
    echo "MEM0_TELEMETRY must be false for the locked benchmark environment" >&2
    exit 1
fi

alembic upgrade head
exec uvicorn main:app --host 0.0.0.0 --port 8000 --workers 1
