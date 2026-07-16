# SWE-Bench Verified 评测报告

本文档汇总 baseline、mini-SWE-agent 重复实验和 TAG runner 在同一 calibrated
evaluator 下的 SWE-Bench Verified 全量结果。

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

## GLM-5.2-Internal 实验

mini-SWE-agent GLM-5.2-Internal 三轮重复实验摘要见
[`experiments/mini-swe-agent-glm-5.2-internal-r3.json`](experiments/mini-swe-agent-glm-5.2-internal-r3.json)。

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

## TAG Runner

`trpc-agent-go-impl` 使用 TAG 的 `llmagent`、runner、session 和 tool callback
生命周期承载 mini-SWE-agent 兼容行为。两轮实验使用相同的 GLM-5.2 模型配置、
500-case 列表、XML observation、15 个 agent workers 和 calibrated official
local harness。XML 与 mini-go XML 轮保持一致，不作为 TAG 特性或实验变量。

| Run | Source revision | Resolved | Unresolved | Empty patch | Error | Resolved rate |
| --- | --- | ---: | ---: | ---: | ---: | ---: |
| TAG e1 | `61b6e46` | 383 | 115 | 2 | 0 | 76.60% |
| TAG e2 | `c5504d8` | 399 | 95 | 5 | 1 | 79.80% |
| mini-go XML 参考 | `02e0796` | 396 | 103 | 0 | 1 | 79.20% |

e2 的 5 个 empty patch 中，4 个 case 达到 250 次 LLM 调用上限，1 个是合法
空提交。e2 唯一 verifier error 为 `sympy__sympy-19040` 的官方测试达到
1,800 秒超时；mini-go XML 轮的 verifier error 也是该 case。上述 error 均
保留在未解决侧，没有从 500-case 分母中剔除。

### 效果与成本

费用和 token 采用后台按 `X-SMG-Agent-Name` 聚合的真实计费记录。两轮 TAG
均使用 `BenchSWE-tag-llmagent-runner-v1`。

| Run | LLM calls | Tool calls | Input tokens | Output tokens | Total tokens | Cached tokens | Cost | Cost / resolved |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| TAG e1 | 13,090 | 14,048 | 206,203,477 | 3,600,312 | 209,803,789 | 201,521,216 | 541.3093 | 1.4133 |
| TAG e2 | 13,829 | 14,707 | 254,957,626 | 3,842,970 | 258,800,596 | 250,148,160 | 646.3752 | 1.6200 |
| mini-go XML 参考 | 13,364 | 14,211 | 212,437,059 | 3,550,905 | 215,987,964 | 207,616,384 | 553.2235 | 1.3970 |

两轮 TAG 合计费用为 1,187.6845，平均单轮费用为 593.8423，平均 resolve
率为 78.20%，合并 cost / resolved 为 1.5188。e2 相比 e1 的 resolve 率高
3.2 个百分点，但费用高 19.4%，cost / resolved 高 14.6%。

两轮 TAG 的效果相差 16 case，说明单轮采样波动明显。e2 已达到 mini-go XML
的效果区间，但两轮平均 resolve 率比 mini-go XML 低 1.0 个百分点，平均费用
高 7.3%。当前结果没有显示稳定的框架侧效果劣化，也不足以证明 TAG 能降低
成本；后续对比应同时报告 resolve 率、总费用和 cost / resolved。

完整运行产物位于 ignored runtime 目录：

- `results/runs/tag-impl-full500-20260715-e1/`
- `results/runs/tag-impl-full500-20260716-e2/`
