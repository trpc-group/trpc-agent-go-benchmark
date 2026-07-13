# mini-SWE-agent compatibility source

The compatibility target is the upstream `SWE-agent/mini-swe-agent` tag
`v2.1.0`, commit `3a9b8e874d322a9cfb1f391ff4f4df67721c108c`.

The Go implementation must be checked against these source files rather than
against the documentation alone:

| Behavior | Upstream source |
| --- | --- |
| Agent loop and limits | `src/minisweagent/agents/default.py` |
| SWE-Bench prompt and model settings | `src/minisweagent/config/benchmarks/swebench.yaml` |
| Tool-call parsing and observations | `src/minisweagent/models/utils/actions_toolcall.py` |
| Model request and retry behavior | `src/minisweagent/models/litellm_model.py`, `src/minisweagent/models/utils/retry.py` |
| Docker execution and submission | `src/minisweagent/environments/docker.py` |
| Prediction and trajectory writing | `src/minisweagent/run/benchmarks/swebench.py` |

Compatibility is defined by deterministic behavior tests generated with that
exact commit. Provider transport, tRPC events, progress reporting, and shard
orchestration are adapters or extensions and must not change the core message,
action, observation, submission, or exit-status semantics.

The checked-in golden file is regenerated from a pinned checkout with:

```bash
python internal/sweagent/testdata/generate_upstream_v2_1_golden.py \
  /path/to/mini-swe-agent-v2.1.0 \
  > internal/sweagent/testdata/upstream_v2_1_golden.json
```

Until the parity suite passes, manifests must identify this runner as
`trpc-agent-go-native-experimental`, not as mini-SWE-agent compatible.
