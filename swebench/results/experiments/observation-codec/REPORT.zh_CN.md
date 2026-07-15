# mini-go Observation Codec JSON/XML 对照实验

## 要支持的决策

本实验用于判断 TAG 是否值得提供内置、显式 opt-in、可按 tool 配置的
`json2xml` tool-result codec。这里的建议是增加标准化转换能力，而不是把
XML 改为 TAG 的全局默认格式。

## 初步结论

在 mini-go + GLM-5.2 的 SWE-Bench Verified 500-case 实验中，XML 相比
JSON 的 resolve 率高 1.6 个百分点，同时 total tokens 低约 14.0%、真实
费用低约 11.7%。本轮结果支持 TAG 提供内置的 JSON 到 XML tool-result
codec，使应用能够按 tool 显式启用 XML，而不必重复实现转换、转义和失败
回退逻辑。

该结果不支持把 XML 改为 TAG 默认格式，也不足以证明 XML 对所有模型和
工具都更优。JSON 默认行为应保持兼容。

## 实验口径

| 项目 | 值 |
| --- | --- |
| Dataset | `princeton-nlp/SWE-bench_Verified` |
| Split | `test` |
| Cases | 500 |
| Case list SHA256 | `a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9` |
| Agent | mini-go（source-aligned mini-SWE-agent 2.1.0） |
| Model | GLM-5.2 |
| Experiment run revision | `02e07966e2946d8955df64253b96d080634c574c` |
| Rebased equivalent revision | `23fcef4` |
| Source modified | `false` |
| Verifier | calibrated official local harness |
| Verifier workers | 8 |
| Verifier cleanup | `clean=true` |
| 并发 | 沿用 runner 原有自适应策略，不作为实验变量 |

JSON 与 XML 使用相同字段、warning、10,000-rune 截断阈值以及 head/tail
各 5,000 rune 的语义。唯一实验变量是 model-facing observation 的编码格式。

## 效果与成本

Resolve 率统一以全部 500 case 为分母。XML 的一个 verifier timeout 仍保留
在未解决侧，没有从分母剔除。

| Codec | Resolved | Resolve 率 | Input tokens | Output tokens | Total tokens | Cached tokens | Cost | Cost / resolved |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| JSON | 388/500 | 77.6% | 247,438,154 | 3,610,350 | 251,048,504 | 242,340,736 | 626.5506 | 1.6148 |
| XML | 396/500 | 79.2% | 212,437,059 | 3,550,905 | 215,987,964 | 207,616,384 | 553.2235 | 1.3970 |
| XML 相对 JSON | +8 | +1.6 pp | -14.1% | -1.6% | -14.0% | -14.3% | -11.7% | -13.5% |

费用和本表 token 采用后台按 `X-SMG-Agent-Name` 聚合的真实计费记录：

- JSON：`BenchSWE-codec-json-e1`。
- XML：`BenchSWE-codec-xml-e1`，合并 2026-07-14 和 2026-07-15 两个
  `statis_day` 的记录。

JSON 的后台 token 与本地 response usage 完全一致。XML 后台比本地多
795,053 total tokens；本地存在缺失 usage 的超时请求，且生成期间发生过一次
retry，因此成本比较以后台记录为准。原始按日账单与 reconciliation delta
保存在 [`json-vs-xml-e1.json`](json-vs-xml-e1.json)。

## 运行有效性

| Codec | Accepted | Missing | Invalid | Duplicate | Completed | Empty patch | Verifier error |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| JSON | 500 | 0 | 0 | 0 | 499 | 1 | 0 |
| XML | 500 | 0 | 0 | 0 | 499 | 0 | 1 |

- JSON：`django__django-11265` 达到 agent 限制，产生空 patch。
- XML：`sympy__sympy-19040` 的测试运行达到 1,800 秒超时。该结果未重跑，
  原样保留为 verifier error。
- 两组 verifier 的 harness exit code 均为 0，最终没有未停止容器或未移除
  实验镜像。

## Codec 正确性审计

| Codec | Tool observations | Short | Truncated | Exception | 格式不一致 |
| --- | ---: | ---: | ---: | ---: | ---: |
| JSON | 14,047 | 13,942 | 105 | 48 | 0 |
| XML | 13,711 | 13,622 | 89 | 45 | 0 |

审计从 trajectory 中的原始 output、return code 和 exception 重建 observation，
再与实际 model-facing tool message 逐条比较。两组的字段、warning 和截断语义
均未发现不一致。

## 对 TAG 的工程含义

TAG 当前在 `internal/flow/processor/functioncall.go` 的
`buildDefaultToolMessage` 中把 tool 返回值默认序列化为 JSON。`tool.Callbacks`
已经提供 `ToolResultMessages` 扩展点，业务可以自行替换发送给模型的 tool
message，但仍需自行解决 JSON 到 XML 的确定性映射、XML escaping、按 tool
选择和失败策略。

因此本实验支持的最小框架能力是：

- 默认仍使用现有 JSON 序列化，保持完全兼容；
- 提供内置、显式 opt-in 的 JSON 到 XML codec/helper；
- 支持按 tool name 配置，而不是全局强制转换；
- 只改变发送给模型的 `RoleTool.Content`；
- 不改变 `CallableTool.Call` 返回值、MCP wire payload、原始事件、telemetry
  或持久化结果；
- 固定 object、array、primitive、null、非法 XML key 和 escaping 的映射；
- 明确非 JSON 结果及转换失败时的回退或报错行为。

本实验比较的是语义等价的 JSON/XML observation，并没有直接验证某个通用
`json2xml` 实现。因此，generic mapping 的正确性和兼容性仍应由 TAG 单元测试
与集成测试覆盖。

## 可复核产物

完整 predictions、trajectory、response usage 和 verifier 日志位于 ignored
runtime 目录，不提交 Git：

- `results/runs/codec-json-e1/`
- `results/runs/codec-xml-e1/`

结构化实验记录见 [`json-vs-xml-e1.json`](json-vs-xml-e1.json)。
