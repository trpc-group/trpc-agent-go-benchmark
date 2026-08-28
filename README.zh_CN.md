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

## ActivationBench-Lite

可在不使用容器、不连接外部工具服务的情况下运行本地测试；配置真实 provider
后即可运行 Skill → ToolSet 动态激活 benchmark：

```bash
cd activationbench
go test ./...
# 需要配置 OPENAI_API_KEY 和 MODEL_NAME。
MODEL_NAME='gpt-5.5' go run ./cmd/activationbench \
  -model-source openai-compatible \
  -mode compare -runs 1 -output-dir ./results/smoke
```

`-output-dir` 默认是 `./results`；如果不希望创建默认目录，请显式指定由调用方
管理的临时目录。

ActivationBench 直接 pin 了
`trpc.group/trpc-go/trpc-agent-go@v1.11.2`（包含动态激活 API 的公开 framework
release），因此只 clone benchmark 仓库也可以构建，不必准备同级的框架工作区。若正在开发更新
的本地框架 revision，可由调用方临时添加自己的 `replace` 做兼容性测试；这不是
benchmark 运行时的要求。首次构建若没有 Go module cache，Go 仍可能下载普通构建依赖。

CLI 只支持真实 provider，不包含 mock-model fallback。使用真实模型时，应显式使用
`-model-source=openai-compatible`；该入口只从环境变量读取 key，预先校验 endpoint 和
工具名，关闭隐藏 SDK retry，配置缺失时直接失败，不会回退到 mock。也可以使用库 API
通过 `runner.Config.ModelFactory` 注入其他 provider。真实报告标记为
`token_source=provider`，并直接使用 tRPC Agent Go 返回的
`model.Response.Usage`（benchmark 只按 request 汇总），不应自行估算 token。runner 的
`ModelInput` 已刻意去掉任务 ID、session ID
（它们可能包含领域标签）和其他任务金标准；adapter 也不能通过旁路读取或把任务的
`RequiredSkills`、`RequiredTools` 等 ground-truth 字段放进模型 prompt。
真实报告中的 `quality_measured=true` 只表示每个任务配置了 evaluator。报告会将
`RunResult.Error` 非空样本计入 `error_runs`、从 `evaluated_runs` 排除，并定义
`pass_rate=passed/evaluated_runs`；`observed_pass_rate` 只是全样本诊断。任一臂有错误时
`quality_delta_comparable=false`，对应质量差异不能作为可发表的任务成功率结论。

固定基线 Skill 位于 `activationbench/skills/<name>/SKILL.md`。每个实验臂启动时只打开
一次这个本地 catalog，并通过框架 `FSRepository` 供该臂的所有任务复用；扩展 suite 时，
只有新增的合成 Skill 会在每个 arm 的临时 catalog 中生成。工具 handler 和任务状态仍在
进程内，不会触碰外部系统或用户文件。

需要更大的本地菜单时可加 `-skills 32 -tools 256`；新增项是只读的本地 fixture，
扩展 Skill 文件只在每个 arm 的临时目录中生成，不需要容器或外部服务。

报告同时记录 request-level/task-first TTFT、任务总耗时和实验臂墙钟耗时；测试专用的
scripted model 只用于管线回归，compare 重复会交替臂顺序并记录 `arm_order`，不能替代
真实 provider 测量。

安全边界、指标、与 Toolathlon 的差异，以及真实模型实验结果与复现协议见
[`activationbench/README.md`](activationbench/README.md)。

## 源仓库

主框架仓库地址：

https://github.com/trpc-group/trpc-agent-go

## 许可证

本仓库使用 Apache License 2.0 许可证，详见 [LICENSE](LICENSE)。
