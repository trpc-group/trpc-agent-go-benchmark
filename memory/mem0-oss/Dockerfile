# syntax=docker/dockerfile:1.7

# Tencent is pleased to support the open source community by making
# trpc-agent-go-benchmark available.
#
# Copyright (C) 2026 Tencent. All rights reserved.
#
# trpc-agent-go-benchmark is licensed under the Apache License Version 2.0.

FROM docker.io/library/python:3.12-slim-bookworm@sha256:d50fb7611f86d04a3b0471b46d7557818d88983fc3136726336b2a4c657aa30b

ARG MEM0_SOURCE_COMMIT=3b9aed866ae70d29043388ed0ae5cc4e1844f3e8
ARG MEM0_SOURCE_ARCHIVE_SHA256=b0531bb5cbc313d94e4b566aa6f0d91c434515a0bbe7fe4c9bb53955105a611c
ARG SPACY_MODEL_VERSION=3.8.0
ARG SPACY_MODEL_SHA256=1932429db727d4bff3deed6b34cfc05df17794f4a52eeb26cf8928f7c1a0fb85

LABEL org.opencontainers.image.source="https://github.com/mem0ai/mem0" \
      org.opencontainers.image.revision="${MEM0_SOURCE_COMMIT}" \
      org.opencontainers.image.version="2.0.11" \
      org.opencontainers.image.title="Mem0 OSS benchmark environment" \
      trpc.group.benchmark.mem0.source-archive-sha256="${MEM0_SOURCE_ARCHIVE_SHA256}"

ENV MEM0_SOURCE_COMMIT="${MEM0_SOURCE_COMMIT}" \
    MEM0_SOURCE_ARCHIVE_SHA256="${MEM0_SOURCE_ARCHIVE_SHA256}" \
    HOME=/tmp \
    PIP_DISABLE_PIP_VERSION_CHECK=1 \
    PIP_ROOT_USER_ACTION=ignore \
    PYTHONPATH=/opt/mem0:/opt/mem0/server \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

WORKDIR /opt/mem0

RUN install -d -m 0755 /opt/benchmark /opt/mem0 \
    && install -d -o 10001 -g 10001 -m 0700 /app/history

ADD https://github.com/mem0ai/mem0/archive/3b9aed866ae70d29043388ed0ae5cc4e1844f3e8.tar.gz /opt/benchmark/mem0-source.tar.gz
ADD --checksum=sha256:1932429db727d4bff3deed6b34cfc05df17794f4a52eeb26cf8928f7c1a0fb85 \
    https://github.com/explosion/spacy-models/releases/download/en_core_web_sm-3.8.0/en_core_web_sm-3.8.0-py3-none-any.whl \
    /opt/benchmark/en_core_web_sm-3.8.0-py3-none-any.whl
RUN printf '%s  %s\n' "${MEM0_SOURCE_ARCHIVE_SHA256}" /opt/benchmark/mem0-source.tar.gz \
    | sha256sum --check --strict - \
    && tar --extract --gzip --file /opt/benchmark/mem0-source.tar.gz \
        --strip-components=1 --directory /opt/mem0

COPY requirements.lock /opt/benchmark/requirements.lock
RUN python -m pip install --no-cache-dir --require-hashes \
        --only-binary=:all: -r /opt/benchmark/requirements.lock \
    && python -m pip install --no-cache-dir --no-deps \
        /opt/benchmark/en_core_web_sm-${SPACY_MODEL_VERSION}-py3-none-any.whl \
    && python -m pip check \
    && test "$(python -c 'import importlib.metadata; print(importlib.metadata.version("mem0ai"))')" = "2.0.11" \
    && test "$(python -c 'import importlib.metadata; print(importlib.metadata.version("spacy"))')" = "3.8.14" \
    && test "$(python -c 'import importlib.metadata; print(importlib.metadata.version("en-core-web-sm"))')" = "${SPACY_MODEL_VERSION}" \
    && test "$(sha256sum /opt/benchmark/en_core_web_sm-${SPACY_MODEL_VERSION}-py3-none-any.whl | cut -d' ' -f1)" = "${SPACY_MODEL_SHA256}" \
    && test "$(python -c 'import mem0; print(mem0.__file__)')" = "/opt/mem0/mem0/__init__.py"

COPY environment.lock.json /opt/benchmark/environment.lock.json
COPY entrypoint.sh runtime_probe.py /opt/benchmark/
RUN chmod 0444 /opt/benchmark/environment.lock.json /opt/benchmark/mem0-source.tar.gz \
        /opt/benchmark/requirements.lock /opt/benchmark/en_core_web_sm-3.8.0-py3-none-any.whl \
    && chmod 0555 /opt/benchmark/entrypoint.sh /opt/benchmark/runtime_probe.py \
    && chmod -R a-w /opt/mem0

WORKDIR /opt/mem0/server

EXPOSE 8000

USER 10001:10001

ENTRYPOINT ["/opt/benchmark/entrypoint.sh"]
