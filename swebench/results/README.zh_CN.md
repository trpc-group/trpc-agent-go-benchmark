# SWE-Bench 实验结果

本目录将被忽略的运行产物与体积小、已脱敏、provenance 完整的实验摘要分开管理。

- `runs/` 与 `artifacts/` 用于本地或外部存储的 predictions、patches、traces、verifier
  logs、workspaces 与批量运行产物，不进入 Git；
- `experiments/` 只保留可通过冻结 commit 与内容 SHA 回溯的 machine-readable 摘要，
  不公开原始模型内容或服务器数据。

每个结果包记录的冻结 source ref 均可在公开的
[历史 fork](https://github.com/hr-chang/trpc-agent-go-benchmark) 中解析；SHA-256 用于固定摘要
所依据的精确源文件。

当前分支中的 public runner 是面向长期维护的重建实现。历史结果证明的是相应方法与冻结实现；
当公开重建的协议与历史实现存在差异时，不自动把历史结果归因给新 runtime。

## 核心结果

| 问题 | 基线 | 候选 | Official-harness RR | Total tokens | 模型侧成本 |
| --- | --- | --- | --- | --- | --- |
| Tool-result observation codec | JSON | XML-like | 77.6% → 79.2%（+1.6pp） | 251.05M → 215.99M（-14.0%） | 626.55 → 553.22（-11.7%） |
| 预注入 workspace context 的表示 | fixed-raw | AST-structured | 79.2% → 80.6%（+1.4pp） | 238.43M → 253.43M（+6.3%） | 598.48 → 638.58（+6.7%） |
| 精确重复工具调用 warning | warning off | warning on | 74.13% → 73.67%（-0.47pp） | 308.51M → 283.44M（-8.1%） | 843.18 → 773.30（-8.3%） |

成本单位是按冻结 rate card 复算的 billing unit，不代表货币支出。Machine-readable 结果同时
提供 prompt/completion/cached/uncached tokens、LLM/tool calls、error 与长尾、重复 run 的
sample SD/range、配对一致性，以及 0%、90%、95%、98%、100% prompt cache hit 的统一成本
敏感性。

## 结果包

- [JSON 与 XML-like observation](experiments/observation-codec/json-vs-xml-like.json)
- [fixed-raw 与 AST-structured workspace representation](experiments/workspace-representation/fixed-raw-vs-ast-structured.json)
- [clean-room Native loop warning on/off](experiments/cleanroom-loop-warning/native-warning-on-vs-off.json)
- [当前 revision 的 Native full-panel 基线](experiments/native-baselines/current-revision-full-panel.json)

## 能支持的工程结论

1. 在该 Coding Agent 协议下，XML-like tool-result observation 是经过验证的可配置能力；
   结果不等于所有模型都应修改默认序列化格式。
2. Full-panel 的 workspace-RAG 实测中，AST-structured 预注入 context 多解决 7 个
   case（+1.4pp）。两臂的 500 个 case 都注入了 preload，显式 `code_search` 调用较少，
   因此该结果支持在 workspace-RAG 中采用 AST-structured context 表示；它比较的不是
   “是否启用检索”这一开关。
3. Warning-on 的三次 run 在平均 RR 基本持平时减少了 8.1% total tokens 和 8.3%
   模型侧成本；离线 replay 进一步确认 warning-off 轨迹中存在大量精确重复长尾。

## 统计与归档口径

- RR 固定以 500 为分母，质量来源只使用 official SWE-bench harness 的 `resolved`；
- 同一 500-case panel 的重复 run 按 run-level 汇总，不把 1,500 个 case-run 当独立样本；
- quality、tokens、cost、latency、errors、trajectory behavior 分开报告；
- raw trajectories、responses、patches、内部 endpoint、服务器绝对路径、恢复 controller 与
  credential 均不进入 Git。

English: [README.md](README.md)
