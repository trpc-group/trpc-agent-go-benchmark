# mini-SWE-agent Go implementation

This directory contains a Go port of mini-SWE-agent. Its compatibility target
is mini-SWE-agent v2.1.0 at commit
`3a9b8e874d322a9cfb1f391ff4f4df67721c108c`; see
[`UPSTREAM.md`](UPSTREAM.md). The implementation uses tRPC-Agent-Go's model,
message, and tool types inside an explicit source-aligned control loop:

- one `bash` tool and a linear 250-step limit;
- one official SWE-Bench Docker testbed per instance, rooted at `/testbed`;
- golden-tested prompt, format-error, command-observation, and loop behavior;
- sequential execution of all bash calls returned in one model response;
- submission only when successful stdout begins with
  `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT`;
- LiteLLM-compatible ten-attempt model retry timing;
- concurrent cases, atomic predictions, per-case trajectories, live progress,
  and resumable runs.

Docker is required only when cases are executed. Unit tests use fake
environments and a mock model. Provider transport, progress reporting, and
shard orchestration are explicit adapters around the source-aligned loop.

This is a reference-implementation lane, not the native tRPC-Agent-Go Agent
lane: it does not use `llmagent` or the framework runner lifecycle.

The provider-neutral CLI does not enforce mini-SWE-agent's default USD cost
limit because tRPC-Agent-Go responses expose usage, not a model-specific price
table. It records `cost_limit: null` and omits `instance_cost` instead of
inventing rates. The reusable loop can enforce a cost limit when a caller
supplies an explicit `ResponseCost` callback.

## Run

Create a local model configuration from
[`../config/models/openai-compatible.yaml.example`](../config/models/openai-compatible.yaml.example),
then run from `swebench/`:

```bash
go run ./mini-swe-agent-go-impl \
  --run-id mini-go-smoke \
  --cases data/generated/cases.jsonl \
  --model-config /path/to/model.local.yaml \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/mini-go-smoke/raw/mini-go \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1
```

The model configuration accepts a standard OpenAI-compatible base URL, API
key, model name, and arbitrary `extra_headers`. Secrets, endpoint URLs, and
header values are not copied into the runner manifest; the full config file is
bound by SHA-256.

Useful runtime controls are `--command-timeout`, `--case-timeout`, and
`--docker-host`. The default `--resume-policy upstream` follows
mini-SWE-agent's key-presence policy, but skips an existing `preds.json` key
only after its trajectory provenance matches the current run ID, source and
binary, model/environment/case configuration, timeouts, codec, and exact
selected instance set. The optional `--resume-policy retryable` extension
retries endpoint/environment failures and incomplete results after the same
validation. `--redo-existing` reruns every selected case but does not permit a
different run provenance to reuse the output directory.

`--observation-codec` accepts `xml`, `json`, or `text` and defaults to `xml`.
The XML-like renderer is the source-aligned compatibility behavior; the other
codecs retain the same control loop so formatting can be evaluated separately.

The output directory contains:

```text
preds.json
mini-go-runner-manifest.json
mini-go-runner-progress.json
<instance_id>/<instance_id>.traj.json
<instance_id>/<instance_id>.trpc-responses.json
```

`preds.json` is directly consumable by the official SWE-Bench harness. The
manifest records the pinned mini-SWE-agent commit, observation codec,
tRPC-Agent-Go module version, source revision and dirty bit, binary hash, case
and model/environment-config hashes, the sorted newline-delimited selected-case
hash, concurrency, timeouts, and terminal-status counts.

## Sharded runs

The optional supervisor creates deterministic 50-case shards, builds the runner
once, executes one shard at a time, and preserves completed artifacts across
restarts:

```bash
./mini-swe-agent-go-impl/run-sharded.sh \
  --run-prefix mini-go-full \
  --model-config /path/to/model.local.yaml \
  --observation-codec xml
```

After a stable shard, concurrency increases by one. Transient endpoint errors
or a no-progress window reduce concurrency and retry only incomplete/retryable
cases. Permanent endpoint/configuration errors pause the supervisor. This AIMD
policy is an orchestration extension, not part of the v2.1 agent core. Every
attempt is appended to `attempts.jsonl`; final shard and merged-prediction
artifacts are written under `results/runs/<run-prefix>/`.

## Validate without Docker

```bash
go test ./mini-swe-agent-go-impl/...
go test -race ./mini-swe-agent-go-impl/...
```

On a Docker host, run the deterministic official-image smoke without sending a
model request:

```bash
SWE_DOCKER_SMOKE_INSTANCE=astropy__astropy-12907 \
  go test -count=1 -run TestDockerMiniAgentSmoke -v \
  ./mini-swe-agent-go-impl/internal/sweagent
```
