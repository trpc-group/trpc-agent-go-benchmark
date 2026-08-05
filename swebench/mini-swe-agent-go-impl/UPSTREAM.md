# mini-SWE-agent compatibility source

The compatibility target is the upstream `SWE-agent/mini-swe-agent` tag
`v2.1.0`, commit `3a9b8e874d322a9cfb1f391ff4f4df67721c108c`.

The Go implementation is checked against these source files rather than
against documentation alone:

| Behavior | Upstream source |
| --- | --- |
| Agent loop and limits | `src/minisweagent/agents/default.py` |
| SWE-Bench prompt and model settings | `src/minisweagent/config/benchmarks/swebench.yaml` |
| Tool-call parsing and observations | `src/minisweagent/models/utils/actions_toolcall.py` |
| Model request and retry behavior | `src/minisweagent/models/litellm_model.py`, `src/minisweagent/models/utils/retry.py` |
| Docker execution and submission | `src/minisweagent/environments/docker.py` |
| Prediction and trajectory writing | `src/minisweagent/run/benchmarks/swebench.py` |

Compatibility is defined by deterministic behavior tests generated with that
exact commit. Provider transport, tRPC events, progress reporting, observation
codec experiments, and shard orchestration are adapters or extensions and must
not change the core message, action, default XML-like observation, submission,
step-limit, or exit-status semantics.

One limit is deliberately conditional: the upstream USD cost cap depends on
LiteLLM's model price table. The provider-neutral CLI has no such table, so it
records a null cost limit and omits instance cost. The reusable Go loop applies
the upstream default cap only when its caller supplies an explicit response
cost function.

Regenerate the checked-in source-text golden from a pinned checkout with:

```bash
python internal/sweagent/testdata/generate_upstream_v2_1_golden.py \
  /path/to/mini-swe-agent-v2.1.0 \
  > internal/sweagent/testdata/upstream_v2_1_golden.json
```

Generate the normalized control-loop golden by running the real upstream
`DefaultAgent` with deterministic fake model/environment objects:

```bash
python internal/sweagent/testdata/generate_upstream_v2_1_loop_golden.py \
  /path/to/mini-swe-agent-v2.1.0 \
  > internal/sweagent/testdata/upstream_v2_1_loop_golden.json
```

The runner may use the `mini-swe-agent-v2.1-source-aligned-core` protocol label only
while both checked-in golden suites and the opt-in Docker smoke pass. This label
does not claim that tRPC's provider transport, progress reporting, or shard
orchestration are upstream mini-SWE-agent components.
