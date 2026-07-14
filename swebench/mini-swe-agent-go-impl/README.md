# mini-SWE-agent Go Implementation

This directory contains a Go port of mini-SWE-agent. Its compatibility target is
mini-SWE-agent v2.1.0 at commit
`3a9b8e874d322a9cfb1f391ff4f4df67721c108c`; see [`UPSTREAM.md`](UPSTREAM.md).
The implementation uses tRPC-Agent-Go's model, message, and tool abstractions
inside an explicit source-aligned control loop:

- one `bash` tool and a linear 250-step limit;
- one official SWE-Bench Docker testbed per instance, rooted at `/testbed`;
- byte-for-byte golden-tested prompt, FormatError, and command observations;
- sequential execution of all bash calls returned in one model response;
- submission only when successful command stdout starts with
  `COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT`;
- LiteLLM-compatible ten-attempt model retry timing;
- concurrent cases, incremental atomic predictions, and per-case trajectories;
- resumable runs, live LLM/tool progress, and endpoint-error classification.

Docker is only required when cases are executed. Unit tests use fake
environments and a mock model. The source-generated text goldens, normalized
Python/Go loop oracle, and opt-in official-image Docker smoke form the parity
gate. Manifests identify a passing build as
`mini-swe-agent-v2.1-source-aligned`; provider transport and orchestration stay
explicitly separate adapters.

This is not the tRPC-Agent-Go framework lane: it does not use `llmagent` or the
tRPC-Agent-Go runner lifecycle. The name `trpc-agent-go-impl` is reserved for a
future implementation built on those framework components.

## Run

From `swebench/`:

```bash
go run ./mini-swe-agent-go-impl \
  --run-id mini-go-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/<model>.yaml \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/mini-go-smoke/raw/mini-go \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1
```

Useful runtime controls are `--command-timeout`, `--case-timeout`, and
`--docker-host`. The default `--resume-policy upstream` skips every case already
present in `preds.json`, matching mini-SWE-agent. The optional
`--resume-policy retryable` extension retries endpoint/environment failures and
incomplete artifacts; `--redo-existing` reruns every selected case. The output
directory contains:

`--observation-codec` accepts `xml`, `json`, or `text` and defaults to `xml`.
The XML renderer remains the source-aligned compatibility behavior. A tagged
experiment must pass `--billing-tag` and `--experiment-id` together; the runner
appends the tag to the configured `X-SMG-Agent-Name` and records the resolved
agent name, codec, experiment ID, source revision, binary hash, cases hash, and
model-config hash in every manifest and trajectory.

```text
preds.json
mini-go-runner-manifest.json
mini-go-runner-progress.json
<instance_id>/<instance_id>.traj.json
<instance_id>/<instance_id>.trpc-responses.json
```

`preds.json` is directly consumable by the official SWE-Bench harness. The
shared evaluator's import, shard-summary, and run-config commands accept the
mini-go artifacts without conversion.

`mini-go-runner-progress.json` is updated while cases are active. It includes
`last_llm_at`, event/LLM/tool counts, and final error categories. The manifest's
`service_error_counts` contains only model endpoint errors, so agent outcomes
such as `LimitsExceeded`, empty patches, and unresolved patches do not look like
endpoint concurrency failures.

## Sharded 500-case run

The supervisor creates 10 fixed 50-case shards, runs them serially, and resumes
without deleting completed artifacts:

```bash
./mini-swe-agent-go-impl/run-sharded.sh \
  --run-prefix mini-go-glm52-full500-$(date +%Y%m%d) \
  --model-config config/models/glm-5.2.local.yaml
```

After a stable shard, concurrency increases by one. Final transient endpoint
errors or ten minutes with no LLM result reduce concurrency by two and retry
only incomplete/retryable cases. At one worker the no-progress window becomes
30 minutes. Permanent endpoint/configuration errors pause the supervisor. This
AIMD supervisor is an orchestration extension, not part of the v2.1 agent core.
On completion it writes the shard summary and merged predictions under
`results/runs/<run-prefix>/`. It also appends every invocation, including
stalled and retried attempts, to `attempts.jsonl`. The worker policy is run
metadata, not a codec experiment variable.

For the codec experiment, use separate run prefixes and billing tags while
leaving the supervisor defaults unchanged:

```bash
./mini-swe-agent-go-impl/run-sharded.sh --run-prefix codec-json-e1 --observation-codec json --billing-tag codec-json-e1 --experiment-id codec-e1
./mini-swe-agent-go-impl/run-sharded.sh --run-prefix codec-xml-e1  --observation-codec xml  --billing-tag codec-xml-e1  --experiment-id codec-e1
./mini-swe-agent-go-impl/run-sharded.sh --run-prefix codec-text-e1 --observation-codec text --billing-tag codec-text-e1 --experiment-id codec-e1
```

After exporting backend rows with `agent_name`, `input_tokens`,
`output_tokens`, `total_tokens`, `prompt_cached_tokens`, and `cost`, normalize
one experiment and bind it to its shard identity with:

```bash
go run ./evaluator import-billing \
  --input results/runs/codec-json-e1/billing-export.json \
  --manifest results/runs/codec-json-e1/shards.json \
  --output results/runs/codec-json-e1/billing.json
```

`run-config --billing ...` then records both the backend accounting and its
token deltas from locally captured response usage. Backend cost remains a
decimal string so no currency or floating-point precision is invented.

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
