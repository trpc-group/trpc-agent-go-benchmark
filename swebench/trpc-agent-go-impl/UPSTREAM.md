# Native runner compatibility source

The model-facing compatibility target is `SWE-agent/mini-swe-agent` tag
`v2.1.0`, commit `3a9b8e874d322a9cfb1f391ff4f4df67721c108c`.

The native runner checks the behavior that affects an agent trajectory against
the following upstream sources:

| Behavior | Upstream source |
| --- | --- |
| System and instance prompts | `src/minisweagent/config/benchmarks/swebench.yaml` |
| Tool declaration and call parsing | `src/minisweagent/models/utils/actions_toolcall.py` |
| Model request attempt budget | `src/minisweagent/models/litellm_model.py`, `src/minisweagent/models/utils/retry.py` |
| Docker execution and submission | `src/minisweagent/environments/docker.py` |
| Step limit and format-error recovery | `src/minisweagent/agents/default.py` |

This directory deliberately does not copy the upstream explicit control loop.
It adapts the pinned prompts and tool protocol to the public tRPC-Agent-Go
framework lifecycle:

```text
llmagent.New -> runner.NewRunner -> runner.Run
```

The framework owns model invocation, event delivery, state updates, and tool
dispatch. Runner-local callbacks preserve the pinned format-error recovery,
sequential `bash` calls, observation encoding, submission marker, and the
250-call limit. Framework events, resumable progress, manifests, and atomic
artifacts are benchmark adapters; they are not upstream mini-SWE-agent
components.

The native adapter keeps the same ten-attempt budget by configuring nine SDK
retries after the initial request. Retry classification and backoff remain
owned by `openai-go`; they are not a byte-for-byte copy of LiteLLM's policy.

The default configuration exposes no repository-search tool. A run therefore
has only the `bash` tool available to the model.
