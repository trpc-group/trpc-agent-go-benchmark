# tRPC-Agent-Go SWE-Bench Runner

This runner implements the mini-SWE-agent v2.1 model-facing protocol through
tRPC-Agent-Go's `llmagent` and `runner` lifecycles. It is distinct from
`mini-swe-agent-go-impl`, which owns an explicit Go control loop.

The framework-backed lane keeps the behavior that affects model quality and
cost:

- the upstream system/instance prompts and response format-error recovery;
- one callable `bash` tool, with same-response calls executed sequentially;
- XML-like, JSON, and text observation codecs with the same truncation boundary;
- a 250 LLM-call limit and non-streaming model requests;
- submission through a controlled framework stop, with no remaining action in
  the same response and no following model call;
- OpenAI SDK retry with nine retries after the initial request;
- one official SWE-Bench Docker testbed and one framework runner per case.

Framework events are retained as run artifacts. They are not converted to the
explicit-loop trajectory format.

## Run

From `swebench/`:

```bash
go run ./trpc-agent-go-impl \
  --run-id native-smoke \
  --cases data/generated/cases.jsonl \
  --model-config config/models/openai-compatible.yaml.example \
  --environment-config config/environments/swebench-testbed.yaml \
  --output results/runs/native-smoke/raw/native \
  --filter '^(astropy__astropy-12907)$' \
  --agent-workers 1 \
  --observation-codec xml
```

`--observation-codec` accepts `xml`, `json`, or `text`. Runs resume by
skipping instance IDs already present in `preds.json` only after the complete
per-case bundle matches the run ID, source/binary, model/environment/cases,
codec, timeout, worker-count, and selected-case fingerprints. Pass
`--redo-existing` to rerun a matching bundle. Model configuration supports arbitrary
OpenAI-compatible HTTP headers through the shared `modelconfig.HTTPHeaders`
normalization.

Case bundles created before the worker-count and response-artifact fingerprints
were added are rejected during resume. Keep them as historical evidence and use
a new output directory (or explicitly rerun them) instead of mixing old and new
bundle schemas.

Each output directory contains:

```text
preds.json
native-runner-manifest.json
native-runner-progress.json
<instance_id>/<instance_id>.native.json
<instance_id>/<instance_id>.responses.json
```

The manifest reads the linked tRPC-Agent-Go module version from Go build
information. It does not hardcode a development revision.

## Validate

```bash
go test ./trpc-agent-go-impl/...
go test -race ./trpc-agent-go-impl/...
```
