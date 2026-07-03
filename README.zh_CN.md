[English](README.md) | 中文

# trpc-agent-go-benchmark

`trpc-agent-go` 的 benchmark 套件仓库。

## 仓库结构

- `anthropic_skills`：Agent Skills 兼容性与 token 使用 benchmark。
- `gaia`：GAIA benchmark 实现与相关资产。
- `knowledge`：Knowledge system 评测脚本与相关资源。
- `memory`：长上下文与 memory backend benchmark。
- `skillcraft`：基于 SkillCraft 的 baseline/evolution 任务评测。
- `summary`：会话摘要评测资源与 runner。
- `swebench`：面向 Go-native SWE Agent 的 SWE-Bench-Verified benchmark。
- `toolsearch`：Tool Search 评测资源与 runner。

## SWE-Bench GLM-5 配置

SWE-Bench native GLM-5 runner 默认发送 `reasoning_effort=high`。运行 `swebench/trpc-agent-go-impl` 时可通过 `GLM5_REASONING_EFFORT` 或 `-glm-reasoning-effort` 覆盖。

## 源仓库

主框架仓库地址：

https://github.com/trpc-group/trpc-agent-go

## 许可证

本仓库使用 Apache License 2.0 许可证，详见 [LICENSE](LICENSE)。
