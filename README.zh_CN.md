[English](README.md) | 中文

# trpc-agent-go-benchmark

`trpc-agent-go` 的 benchmark 套件仓库。

## 仓库结构

- `anthropic_skills`：Agent Skills 兼容性与 token 使用 benchmark。
- `activationbench`：本地、无容器的 Skill → ToolSet 动态激活 benchmark。
- `gaia`：GAIA benchmark 实现与相关资产。
- `knowledge`：Knowledge system 评测脚本与相关资源。
- `memory`：长上下文与 memory backend benchmark。
- `skillcraft`：基于 SkillCraft 的 baseline/evolution 任务评测。
- `summary`：会话摘要评测资源与 runner。
- `tau`：面向 trpc-agent-go LLMAgent 的 Tau3 text benchmark 接入。
- `toolsearch`：Tool Search 评测资源与 runner。

## 源仓库

主框架仓库地址：

https://github.com/trpc-group/trpc-agent-go

## 许可证

本仓库使用 Apache License 2.0 许可证，详见 [LICENSE](LICENSE)。
