# SWE-Bench Verified 评测报告

本文档是 baseline 阶段快照。Go-native `trpc-agent-go` SWE Agent 在同一
evaluator 下完成 SWE-Bench Verified 全量评测后，再补充最终对比报告。

## Baseline

当前接受的 baseline 是 mini-SWE-agent 2.1.0 + MiniMax M2.5，运行在
SWE-Bench Verified test split 全量 500 case 上。该 run 用于对齐
SWE-Bench 官网公开参考结果，确认 baseline 和 evaluator 链路本身可信。

| 指标 | 值 |
| --- | ---: |
| Total cases | 500 |
| Submitted | 500 |
| Completed | 495 |
| Resolved | 380 |
| Unresolved | 115 |
| Empty patch | 1 |
| Error | 4 |
| Resolved rate | 76.00% |

结构化摘要见
[`baseline-mini-swe-agent-m2.5.json`](baseline-mini-swe-agent-m2.5.json)。

## 内部 GLM-5.2 实验

后续实验，包括 mini-SWE-agent 后续 run 和 Go-native `trpc-agent-go` 实现，
默认使用内部 GLM-5.2 endpoint（`glm52`）。该 endpoint 是内部部署版本，
不直接等同于 SWE-Bench leaderboard 上的公开 GLM-5.2。

mini-SWE-agent GLM-5.2 三轮重复实验摘要见
[`experiments/mini-swe-agent-glm52-r3.json`](experiments/mini-swe-agent-glm52-r3.json)。

| Run | Resolved | Unresolved | Empty patch | Error | Resolved rate |
| --- | ---: | ---: | ---: | ---: | ---: |
| r2 | 383 | 116 | 0 | 1 | 76.60% |
| r3 | 382 | 118 | 0 | 0 | 76.40% |
| r4 | 394 | 106 | 0 | 0 | 78.80% |

## Verifier

该结果使用已校准的 SWE-Bench 官方 local harness。`calibrated` 模式仍以官方
harness 为判定路径，同时为当前冻结的本地 evaluator 环境应用必要兼容修正，
包括为 `psf/requests` case 管理本地 `httpbin.org` 依赖，以及为部分旧依赖栈
补充兼容处理。

单 case rerun 仅作为复查证据保留，不覆盖上表采用的 full-500 聚合结果。

## Native Run

Go-native `trpc-agent-go` 实现尚未产出全量 500 case 结果。完成后，最终报告将
补充 native 指标、逐 case 对比、失败分析和复现说明。
