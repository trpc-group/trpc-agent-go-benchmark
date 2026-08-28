[English](README.md) | [中文](README.zh_CN.md)

# trpc-agent-go-benchmark

Benchmark suites for `trpc-agent-go`.

## Repository Layout

- `anthropic_skills`: Agent Skills compatibility and token usage benchmarks.
- `activationbench`: Local, no-container Skill → ToolSet activation benchmark.
- `gaia`: GAIA benchmark implementation and assets.
- `knowledge`: Knowledge-system evaluation assets and scripts.
- `memory`: Long-context and memory-backend benchmarks.
- `skillcraft`: SkillCraft task benchmark for baseline vs. evolution skill reuse.
- `summary`: Session summary evaluation assets and runner.
- `tau`: Tau3 text benchmark adapter for trpc-agent-go LLMAgent.
- `toolsearch`: Tool-search evaluation assets and runner.

## ActivationBench-Lite

Run the local tests without containers or external tool services, then use a
configured real provider for the activation benchmark:

```bash
cd activationbench
go test ./...
# Requires OPENAI_API_KEY and MODEL_NAME.
MODEL_NAME='gpt-5.5' go run ./cmd/activationbench \
  -model-source openai-compatible \
  -mode compare -runs 1 -output-dir ./results/smoke
```

`-output-dir` defaults to `./results`; use an explicit caller-owned temporary
directory if you do not want the default location to be created.

ActivationBench pins `trpc.group/trpc-go/trpc-agent-go`
`v1.11.2`, the public framework release
that contains the dynamic-activation APIs. A benchmark-only clone therefore works without a
side-by-side framework checkout; Go may still fetch ordinary build dependencies
on the first run. If you are developing a newer local framework revision, you
can add a caller-owned temporary `replace` to test against it. The benchmark
runtime itself does not require containers or external tool services.

The CLI is provider-only: it does not ship a mock-model fallback. Use the explicit
`-model-source=openai-compatible` mode with a real model endpoint; it reads the key only
from an environment variable, validates the endpoint/tool names, disables hidden SDK
retries, and fails closed when the provider configuration is missing. The library runner
with `runner.Config.ModelFactory` remains available for other providers. Real reports
use `token_source=provider` and read `model.Response.Usage` returned by tRPC Agent Go
directly (the benchmark only aggregates it per request) instead of re-estimating tokens.
Do not read the
task's ground-truth fields through a side channel or put them in the model prompt
(`ModelInput` is intentionally sanitized and omits semantic task/session IDs).
In a real report, `quality_measured=true` only means every task has an evaluator. The report
counts non-empty `RunResult.Error` values as `error_runs`, excludes them from `evaluated_runs`,
and defines `pass_rate=passed/evaluated_runs`; `observed_pass_rate` is an all-sample diagnostic.
When either arm has errors, `quality_delta_comparable=false`, so its quality delta is not a
publishable task-success claim.

The fixed baseline Skills live in `activationbench/skills/<name>/SKILL.md`. Each experiment arm
opens that local catalog once through the framework `FSRepository` and reuses it across tasks;
scaled suites generate only their additional synthetic Skills in one temporary catalog per arm.
Tool handlers and task state remain in-process and do not touch external systems or user files.

For a larger local menu sweep, add `-skills 32 -tools 256`; generated entries are
read-only local fixtures (additional Skill files use a temporary per-arm directory)
and do not require containers or external services.
Reports also retain request-level/task-first TTFT, task duration, and per-arm wall-clock
timing; compare repetitions alternate arm order and record `arm_order`. The test-only
scripted-model path is for plumbing regression, not a provider claim.

See [`activationbench/README.md`](activationbench/README.md) for safety guarantees,
metrics, the limits of this Lite fixture compared with Toolathlon, and the recorded
real-provider results and reproduction protocol.

## Source Repository

The main framework repository lives at https://github.com/trpc-group/trpc-agent-go.

## License

This repository is licensed under Apache License 2.0. See [LICENSE](LICENSE).
