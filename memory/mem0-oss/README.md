# Reproducible Mem0 OSS Environment

This directory owns the Mem0 OSS environment used by the memory benchmark. It
builds and serves the unmodified official Mem0 REST API from one locked source
commit. Benchmark-specific code is not imported into the Mem0 application and
no benchmark-only HTTP routes are added.

## Locked Components

[`environment.lock.json`](environment.lock.json) is the authoritative lock. It
records:

- the official `mem0ai/mem0` repository, full commit, and source archive hash;
- the exact `mem0ai` distribution and CPython patch versions;
- hashes of the official REST handler and installed memory-core implementation;
- Python and pgvector image index, `linux/amd64` manifest, and config digests;
- every Python package version and distribution hash;
- the `mem0ai[nlp]` extra, exact spaCy version, and checksum-pinned official
  `en_core_web_sm` model wheel;
- exact PostgreSQL and pgvector runtime versions;
- the official server module and API identity;
- LLM, embedder, split-proxy, collection, and vector-store configuration; and
- the API capabilities that preflight must exercise.

At the locked commit, the official `MemoryCreate.prompt` field is forwarded to
`Memory.add`. Mem0 retains `ADDITIVE_EXTRACTION_PROMPT` as the base system
protocol and renders `prompt` in the generated user prompt as custom
instructions. It does not replace the fact-extraction protocol. This behavior is
part of the environment lock, not an assumption made by the benchmark runner.

The platform is intentionally fixed to `linux/amd64`. The Docker build verifies
the official source archive before extraction and retains it for runtime hash
verification. Dependencies are installed once with `--require-hashes` and
`--only-binary=:all:`. The `mem0ai==2.0.11` wheel remains in that dependency
closure, but it is not treated as source identity: `PYTHONPATH` places the
verified `/opt/mem0` commit tree first, and the build asserts that
`mem0.__file__` resolves there. The extracted tree is made read-only before the
image is finalized. Nothing installs or upgrades packages at service startup.

## Benchmark Profile

The maintained Mem0 baseline exercises the native Mem0 V3 path rather than a
semantic-only degradation:

- synchronous, inferred, ADD-only extraction through the official OSS API;
- the observation date supplied as extraction custom instructions;
- ordered turn-pair fragments with no backend-specific transcript truncation;
- hybrid retrieval combining semantic candidates, lemmatized BM25, and entity
  linking/boosting;
- Mem0's native semantic threshold of `0.1` and the benchmark-wide retrieval
  limit of 20; and
- user-scoped storage and retrieval with a clean case boundary.

The spaCy dependency and English model are mandatory because Mem0 silently
falls back to raw text and empty entity signals when they are unavailable.
Preflight therefore checks both the installed NLP pipeline and non-zero BM25
and entity contributions on a live canary search. Merely installing the Python
packages is not accepted as evidence that hybrid retrieval is active.

Reranking is intentionally excluded from this baseline. It changes the search
method and requires a separately configured reranker, so it belongs in a named
ablation rather than the native baseline. Expiration, decay, procedural memory,
graph memory, categories, and asynchronous ingestion are also outside this
LongMemEval profile.

## Start The Service

Docker with the Compose plugin v2 or later is required. From this directory:

```bash
cp .env.example .env
```

Set strong, non-empty `POSTGRES_PASSWORD` and `MEM0_PROXY_API_KEY` values.
`LLM_NAME`, `MODEL_NAME`, `MEM0_OPENAI_BASE_URL`, and
`MEM0_COLLECTION_NAME` must retain the values in the lock. Changing one is a
lock update, not a local runtime override.

The split proxy owns the actual upstream LLM and embedding credentials.
`MEM0_PROXY_API_KEY` is the bearer credential used by Mem0 to authenticate to
that proxy. Use at least 32 random bytes and provide the same value to the proxy
through its environment. Never commit `.env` or publish
`docker compose config` output because Compose interpolates secrets into that
output.

Start `../adapter/openai_split_proxy.py` as documented in the parent README,
then build and start the environment:

```bash
docker compose --env-file .env -f compose.yaml up --build --detach --wait
```

The Mem0 API binds to loopback only. PostgreSQL is not published to the host.
The Mem0 process runs as UID/GID 10001 with a read-only root filesystem, all
Linux capabilities dropped, and `no-new-privileges` enabled. Its history volume
is the only persistent writable application path.

## Mandatory Preflight

Run preflight before every Mem0 benchmark run:

```bash
python3 preflight.py --env-file .env --output preflight.json
```

Preflight uses Compose to identify the running container and fails closed unless
all of the following match the lock:

- embedded source archive, environment lock, and requirements lock hashes;
- `mem0.__file__` under the read-only locked source tree plus exact REST-handler
  and memory-core source hashes;
- exact Python, `mem0ai`, spaCy, `en_core_web_sm`, PostgreSQL, and pgvector
  versions, plus the model-wheel checksum and required lemmatizer/NER pipelines;
- `linux/amd64`, the official `main:app` process, and one worker;
- authentication, telemetry, proxy URL, models, and collection settings;
- official OpenAPI identity, required REST methods, and bundled providers;
- `MemoryCreate.prompt` on the real `POST /memories` request schema and its
  additive custom-instruction semantics in the installed Mem0 core;
- installed pgvector type, distance operator, and HNSW access method; and
- live LLM generation plus memory create, hybrid search, and delete operations.

The capability check makes one instruction-generation request and one real
`infer=true` `POST /memories` request carrying an observation instruction in
the official `prompt` field. It verifies the structured extracted memory obeys
that instruction, finds the same memory through `explain=true` hybrid search,
verifies non-zero lemmatized BM25 and entity-link contributions, and then
deletes the canary. A failed cleanup is itself a preflight failure and may
require manual inspection of the `mem0-preflight-*` user scope.

`preflight.json` is created with mode `0600` and contains only sanitized
configuration. API keys, passwords, DSNs, URL credentials, query parameters,
and capability payloads are never written. A failed preflight means the run is
not comparable and must not start.

Pass this file to the Go LongMemEval runner with
`-mem0-preflight ../mem0-oss/preflight.json`. Maintained runs fail closed unless
the preflight service URL, source commit, distribution version, LLM model,
embedding model, environment-lock digest, and observation-prompt capability
match the active benchmark configuration. The result stores only the sanitized
identity fields and SHA-256 digests.

Stop the environment with:

```bash
docker compose --env-file .env -f compose.yaml down
```

Add `--volumes` only when an intentional clean database is required.

## Updating The Lock

Lock updates are deliberate benchmark methodology changes:

1. Select an official `mem0ai/mem0` commit and verify its `pyproject.toml`
   version and server API.
2. Pin every direct dependency in `requirements.in`, then regenerate
   `requirements.lock` with the exact command recorded in its header.
3. Verify the source archive hash and image index, platform, and config digests
   through their authoritative registries.
4. Run the pinned images to record exact Python and PostgreSQL versions, and
   verify pgvector source provenance and installed version.
5. Update duplicated immutable values in `Dockerfile`, `compose.yaml`, and
   `environment.lock.json`, including both requirements file hashes.
6. Rebuild from scratch and run the full preflight against the new environment.

Never replace a digest with `latest`, a bare repository name, a version range,
or a startup-time package reinstall.

## Tests

```bash
python3 -m py_compile preflight.py preflight_test.py runtime_probe.py runtime_probe_test.py
python3 -m unittest discover -s . -p '*_test.py'
sh -n entrypoint.sh

POSTGRES_PASSWORD=benchmark-test \
  docker compose --env-file .env.example -f compose.yaml config --quiet
docker build --check -f Dockerfile .
```

The final command requires a Docker CLI with BuildKit/buildx check support.
