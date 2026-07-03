[English](README.md) | [中文](README.zh_CN.md)

# trpc-agent-go-benchmark

Benchmark suites for `trpc-agent-go`.

## Repository Layout

- `anthropic_skills`: Agent Skills compatibility and token usage benchmarks.
- `gaia`: GAIA benchmark implementation and assets.
- `knowledge`: Knowledge-system evaluation assets and scripts.
- `memory`: Long-context and memory-backend benchmarks.
- `skillcraft`: SkillCraft task benchmark for baseline vs. evolution skill reuse.
- `summary`: Session summary evaluation assets and runner.
- `swebench`: SWE-Bench-Verified benchmark for Go-native SWE agents.
- `toolsearch`: Tool-search evaluation assets and runner.

## SWE-Bench GLM-5 Configuration

The SWE-Bench native GLM-5 runner sends `reasoning_effort=high` by default. Override it with `GLM5_REASONING_EFFORT` or `-glm-reasoning-effort` when running `swebench/trpc-agent-go-impl`.

## Source Repository

The main framework repository lives at https://github.com/trpc-group/trpc-agent-go.

## License

This repository is licensed under Apache License 2.0. See [LICENSE](LICENSE).
