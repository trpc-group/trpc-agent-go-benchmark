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

The optional workspace-retrieval lane is benchmark-local and default-off. It
uses only public tRPC-Agent-Go knowledge, document-reader, embedding and tool
interfaces plus a benchmark-owned deterministic BM25/hybrid store. Workspace
snapshotting, representation selection, preload injection, embedding cache,
telemetry and replay provenance belong to the benchmark adapter; they do not
change the root framework or claim a new framework default. AST modes use the
public Python reader. Structured embedding text is assembled locally because
the public reader intentionally returns source-oriented documents rather than
the frozen benchmark representation schema.

Frozen historical RAG/AST experiments were behavior-mined to specify the
benchmark contract, but their implementation is not copied wholesale and their
results are not attributed to this rebuilt tree. Revalidation of this code is a
separate evidence step. In particular, the pinned public model adapter emits
tools as `bash,code_search` and the public generic knowledge tool leaves
invocation deduplication disabled; the historical lane used the reverse tool
order and invocation-scoped deduplication. Those differences are recorded in
portable run/case/index identity rather than hidden behind an equivalence
claim.

The optional clean-room mode is benchmark-local execution policy. Its
network isolation, Git sanitation, offline fixtures and assets, image
attestation, preflight, and provenance gates are not copied from
mini-SWE-agent and are not tRPC-Agent-Go framework defaults. The default-off
Native path keeps the source-aligned prompts and Docker behavior described
above; enabling clean-room mode adds an accurate offline capability notice to
the model-facing prompt and records the distinct protocol identity.

The optional exact tool-loop warning is also benchmark-local and default-off.
It uses runner callbacks and persisted telemetry for a controlled SWE-Bench
ablation; it does not add loop policy to the root tRPC-Agent-Go framework. The
offline shadow-replay command consumes supported immutable warning-off
trajectories, including the frozen V12 TAG layout, and shares the runtime
detector implementation, so reported would-warn positions use the same frozen
semantics.
