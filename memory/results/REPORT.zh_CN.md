# Memory Benchmark 结果报告

本报告按实验发生的时间顺序组织：先给出基于 LoCoMo
的长期对话记忆评测，以及随后在同一口径下补充的 update policy
对比；再给出基于 LongMemEval 的跨 session 用户记忆评测。
两部分各自独立说明实验设置、结果与分析。

| 实验 | 基准 | 评测对象 | 规模 | 模型 |
| --- | --- | --- | --- | --- |
| 一 | LoCoMo-10 | 内部场景、Python Agent 框架、外部记忆系统横向对比 | 1,986 QA | `gpt-4o-mini` |
| 二 | LoCoMo-10 | 三种 Auto update policy | 1,986 QA | `gpt-4o-mini` |
| 三 | LongMemEval | 三种 update policy × assistant-episode 开关，以及 Mem0 OSS | 50 case | `glm52` |

**结果状态**

- LoCoMo 部分是当时运行的真实记录。仓库只保留报告文本，数据、
  日志和 trace 等运行产物不随仓库发布，
  因此这些数字应按历史运行记录引用；
  按报告给出的设置重跑应当能够复现同样的结论。
- LongMemEval 一节是开发阶段在 50-case 子集上的评测结果，
  子集的构成与完整清单见该节第 1 节和附录 D。
  它用于配置之间的相对比较，不代表该基准上的最终成绩。

---

## 基于 LoCoMo 基准的长期对话记忆评估

### 1. 引言

本报告使用 **LoCoMo** 基准（Maharana et al., 2024）评估
**trpc-agent-go** 的长期对话记忆能力。报告涵盖两个版本：

- **trpc-agent-go (原版)**：基线版本（Auto 提取 + pgvector）
- **trpc-agent-go (优化版)**：经过多轮优化，包括情境化记忆提取、
  情景记忆分类、混合检索、多轮检索等（详见 2.3 节）

以上两个版本与四个 Python Agent 框架（AutoGen、Agno、ADK、
CrewAI）和十个外部记忆系统（Mem0、Zep 等）进行对比。

### 2. 实验设置

#### 2.1 基准数据集

| 项目 | 值 |
| --- | --- |
| 数据集 | LoCoMo-10（10 个对话，1,986 个 QA） |
| 类别 | single-hop (282), multi-hop (321), temporal (96), open-domain (841), adversarial (446) |
| 模型 | GPT-4o-mini（推理 + 评判） |
| Embedding | text-embedding-3-small |

#### 2.2 评估场景

| 场景 | 描述 |
| --- | --- |
| **Long-Context** | 完整对话文本作为 LLM 上下文（上界） |
| **原版** | Auto 提取 + pgvector 基线；后台提取器自动生成记忆并在查询时检索 |
| **优化版** | 面向抽取式持久化 memory 的优化记忆提取策略与多轮检索流程 |

#### 2.3 优化项：原版 → 优化版

优化版在原版基线的基础上，围绕记忆提取、存储和检索三个环节
进行了一系列针对性改进：

1. **情境化记忆提取（Contextualized Memory Extraction）**——
   原版提取器生成的记忆为扁平、无结构的文本。优化版使用精心设计
   的提取 prompt，强制要求**原子性**（每条记忆仅包含一个信息点）、
   **完备性**（提取所有说话者、所有细节）和**具体性**（保留
   准确的人名、日期、数量），从而显著提升信息密度和检索召回率。

2. **情景记忆分类（Episodic Memory Classification）**——每条
   提取的记忆被分类为**事实（Fact）**（稳定的属性、偏好、关系）
   或**情景（Episode）**（带时间锚点的事件，包含 `event_time`、
   `participants`、`location` 元数据）。结构化 schema 使检索时
   可按时间范围过滤和按 event_time 排序，这对 multi-hop 和
   temporal 类问题至关重要。

3. **相对时间绝对化（Absolute Date Resolution）**——对话中的
   相对时间表达（如「昨天」「上个月」）在存储前会根据 session
   的参考日期解析为绝对 ISO 8601 日期。这避免了时间漂移，
   使基于日期的查询更加准确。

4. **主题标签（Topic Tagging）**——每条记忆被标注描述性主题
   标签（如 `["hiking", "Mt. Fuji", "travel"]`），且提取器被
   指导优先复用已有的主题名，而非发明同义词。主题标签提升了
   检索相关性，并为未来的主题过滤提供了基础。

5. **混合检索（Hybrid Search：向量 + 关键词）**——原版仅使用
   纯向量相似度搜索。优化版新增**混合检索**，将向量余弦相似度
   与 PostgreSQL 全文检索（`tsvector/tsquery`）通过**倒数排名
   融合（Reciprocal Rank Fusion, RRF）**合并。这显著提升了对
   特定实体名称、书名等精确匹配项的召回率——这些词单靠向量
   embedding 往往无法获得高排名。

6. **多轮检索（Multi-Pass Retrieval）**——QA Agent 不再只做
   一次搜索，而是执行 **2–3 轮搜索**，每轮使用不同的查询策略
   （如关键词式查询、实体聚焦查询、宽泛人名查询），从不同角度
   最大化召回后再综合回答。

7. **类型回退（Kind Fallback）**——当按记忆类型过滤的检索
   （如仅检索 episode）返回结果不足（< 3 条）时，系统自动
   回退为不带类型过滤的检索，并合并两组结果，优先展示匹配
   目标类型的条目。这防止了因分类不确定而遗漏结果。

8. **内容去重（Content Deduplication）**——对检索结果中近重复
   的记忆（词级 Jaccard 相似度 > 80%）进行去重，仅保留得分
   最高的版本，减少检索结果中的冗余上下文。

### 3. 结果

#### 3.1 内部场景对比

**表 1：总体指标**

| 场景 | F1 | BLEU | LLM Score | Tokens/QA | 调用/QA | 延迟 | 总耗时 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Long-Context | 0.469 | 0.426 | 0.526 | 18,776 | 1.0 | 2,607ms | 1h26m |
| Session Recall | **0.549** | **0.511** | **0.609** | 3,694 | 1.0 | 6,430ms | 3h33m |
| 优化版 | **0.469** | **0.431** | **0.532** | 17,182 | 3.0 | 8,585ms | 4h44m |
| 原版 | 0.399 | 0.371 | 0.416 | 3,056 | 2.0 | 6,659ms | 3h40m |

> 优化版的 F1 从 0.399 提升到 **0.469**（+17.5%），达到
> Long-Context F1 的 **99.9%**（原版仅为 85.1%）。虽然名义
> Tokens/QA 较高（17,182），但其中 **43.9% 命中 prompt cache**，
> 实际新增 token 成本约为 9,663/QA（详见 4.5 节）。
>
> 作为补充检索路径，Session Recall 现在把总体 F1 推到
> **0.549**，同时将 Tokens/QA 控制在 **3,694**。相比
> Long-Context，token 成本降低 **80.3%**；相比优化版，
> 降低 **78.5%**。

**表 2：各类别 F1**

| 类别 | Count | Long-Context | Session Recall | 优化版 | 原版 |
| --- | ---: | ---: | ---: | ---: | ---: |
| single-hop | 282 | 0.320 | 0.368 | **0.396** | 0.316 |
| multi-hop | 321 | 0.308 | **0.554** | 0.453 | 0.096 |
| temporal | 96 | 0.088 | 0.174 | **0.247** | 0.088 |
| open-domain | 841 | 0.518 | **0.618** | 0.441 | 0.358 |
| adversarial | 446 | 0.667 | 0.610 | 0.626 | **0.814** |

**表 3：加权平均 F1**

| 平均方式 | Long-Context | Session Recall | 优化版 | 原版 |
| --- | ---: | ---: | ---: | ---: |
| 5 类加权 (÷1986) | 0.469 | **0.549** | 0.469 | 0.399 |
| 4 类加权 (÷1540，排除 adversarial) | 0.411 | **0.531** | 0.423 | 0.279 |

> 优化版依然在四类知识型问题上相比原版有全面提升，其中
> **multi-hop** 从 0.096 提升到 0.453（+372%）最为显著，
> **temporal** 从 0.088 提升到 0.247（+181%）次之。adversarial
> 从 0.814 降到 0.626，主要是因为原版有更强的拒答倾向。
>
> 作为补充方案，Session Recall 现在更大幅地改变了整体权衡：它在
> **multi-hop** 和 **open-domain** 上表现最佳，**temporal** 也
> 提升到 0.174，并将 4 类加权 F1 推到 **0.531**。优化版依然在
> **single-hop** 和 **temporal** 上更强，而 Long-Context 与优化版
> 在 **adversarial** 上仍略有优势。

**表 4：各样本 F1**

| 样本 | QA 数 | Long-Context | Session Recall | 优化版 | 原版 |
| --- | ---: | ---: | ---: | ---: | ---: |
| locomo10_1 | 199 | 0.455 | **0.530** | 0.432 | 0.331 |
| locomo10_2 | 105 | 0.496 | **0.636** | 0.422 | 0.302 |
| locomo10_3 | 193 | 0.527 | **0.644** | 0.521 | 0.432 |
| locomo10_4 | 260 | 0.466 | **0.482** | 0.447 | 0.378 |
| locomo10_5 | 242 | 0.433 | **0.542** | 0.436 | 0.451 |
| locomo10_6 | 158 | 0.511 | **0.553** | 0.505 | 0.455 |
| locomo10_7 | 190 | 0.461 | **0.530** | 0.487 | 0.407 |
| locomo10_8 | 239 | 0.453 | **0.563** | 0.492 | 0.404 |
| locomo10_9 | 196 | 0.450 | **0.508** | 0.464 | 0.383 |
| locomo10_10 | 204 | 0.471 | **0.562** | 0.478 | 0.407 |
| **平均** | **199** | 0.469 | **0.549** | 0.469 | 0.399 |

> 优化版相较原版在全部 10 个样本上都有提升，并在其中 6 个样本上
> 超过 Long-Context。
>
> 作为补充方案，Session Recall 现在在 10 个样本里全部超过
> Long-Context，也在 10 个样本里全部超过优化版，提升最大的样本是
> `locomo10_2`、`locomo10_3` 和 `locomo10_5`。

#### 3.2 检索策略 vs Long-Context

Long-Context 将完整对话历史放入单次 LLM 调用，在短单 session
场景中有效；两种检索式方案则体现出不同的生产权衡：

| 维度 | Long-Context | Session Recall | 优化版 |
| --- | --- | --- | --- |
| **跨 session 来源** | 无 | 直接在 query 时搜索历史 session 原始事件 | 搜索抽取后的持久化 memory |
| **上下文窗口** | 受模型限制（GPT-4o-mini 128K） | 无上限——仅注入召回的事件片段 | 无上限——仅注入检索到的 memory |
| **可扩展性** | 成本随转录长度线性增长 | 成本近似常量（top-K 召回） | 成本受 tool-call 步数和 memory payload 影响 |
| **总体 F1** | 0.469 | **0.549** | 0.469 |
| **4 类加权 F1** | 0.411 | **0.531** | 0.423 |
| **Tokens/QA** | 18,776 | **3,694** | 17,182 |
| **突出优势** | adversarial 更稳 | 总体准确率、open-domain 与 multi-hop 最强 | temporal / adversarial 更均衡 |

---

#### 3.3 SQLite vs SQLiteVec（子集实验）

本小节对比 `sqlite`（关键词/Token 匹配）与 `sqlitevec`（sqlite-vec 语义向量检索）
在若干个可控的子集实验上的表现，用于观察 token 成本与检索差异。

**子集实验 A：端到端 QA（Auto / 全类别）**

该实验保持端到端流程与主要实验一致，但仅评估单个样本以控制成本。

**实验配置**：

- 数据集：LoCoMo `locomo10.json`
- 样本：`locomo10_1`（199 个 QA，包含全部类别）
- 场景：`auto`
- 模型：`gpt-4o-mini`
- LLM 评判：启用
- SQLiteVec embedding 模型：`text-embedding-3-small`
- SQLiteVec 检索 top-k：10（默认值）

**端到端结果：总体指标与 token 消耗（Auto / 199 QA）**

| 后端 | #QA | F1 | BLEU | LLM Score | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | 平均延迟 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | 199 | 0.327 | 0.301 | 0.370 | 1,287,813 | 5,624 | 1,293,437 | 398 | 5,805ms |
| SQLiteVec | 199 | 0.307 | 0.285 | 0.325 | 407,969 | 5,556 | 413,525 | 396 | 6,327ms |

**解读（locomo10_1）**：

- **SQLiteVec 的 prompt token 约减少 3.2x**（top-k 有界检索），但在该样本上
  **F1/BLEU/LLM Score 略低**（默认 top-k=10）。
- 类别层面的表现存在差异：`sqlitevec` 在 `adversarial` 上更好（更多正确拒答），
  但当关键信息未进入 top-k 时，其他类别会出现召回不足导致的下降。

我们也在另一个代表性样本上复现相同配置。

- 样本：`locomo10_6`（158 个 QA，包含全部类别）

**端到端结果：总体指标与 token 消耗（Auto / 158 QA）**

| 后端 | #QA | F1 | BLEU | LLM Score | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | 平均延迟 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | 158 | 0.269 | 0.243 | 0.289 | 1,296,580 | 5,103 | 1,301,683 | 340 | 6,359ms |
| SQLiteVec | 158 | 0.274 | 0.254 | 0.295 | 362,903 | 4,773 | 367,676 | 324 | 6,928ms |

**总体结论（locomo10_1 + locomo10_6）**：

- SQLiteVec 在我们的子集实验中稳定地将 prompt token 降低到约 1/3 到 1/4。
- 默认 top-k=10 下，答案质量的变化与样本相关；调大 top-k 可能提升召回，
  但也会增加 prompt token。

> 注：`Prompt Tokens`、`LLM Calls` 仅统计 QA 阶段 Agent 的模型调用，
> 不包含 embedding 请求与 LLM-as-Judge 调用。`平均延迟` 为端到端总耗时
> 按 #QA 平均（包含 embedding、LLM-as-Judge 以及 auto extraction）。

**子集实验 B：Temporal-only token 成本微基准**

**实验配置**：

- 数据集：LoCoMo `locomo10.json`
- 样本：`locomo10_1`
- 类别过滤：`temporal`（13 个 QA）
- 场景：`auto`
- 模型：`gpt-4o-mini`
- LLM 评判：关闭
- SQLiteVec embedding 模型：`text-embedding-3-small`

**表 5：总体指标与 token 消耗（Auto / Temporal / 13 QA）**

| 后端 | F1 | BLEU | Prompt Tokens | Completion Tokens | Total Tokens | LLM Calls | 平均延迟 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | 0.116 | 0.082 | 80,184 | 352 | 80,536 | 26 | 12,352ms |
| SQLiteVec | 0.116 | 0.082 | 26,483 | 353 | 26,836 | 26 | 17,817ms |

**子集实验 C：向量 top-k 扫参 + 多次检索消融（Auto / 全类别）**

**表 6：Top-k 与多次检索扫参结果（Auto / locomo10_1 / 199 QA）**

| 后端 | vector-topk | qa-search-passes | F1 | BLEU | Prompt Tokens | Avg Prompt/QA | 平均延迟 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| SQLite | - | 1 | 0.299 | 0.283 | 1,322,360 | 6,645 | 3,316ms |
| SQLiteVec | 5 | 1 | 0.320 | 0.296 | 346,253 | 1,740 | 4,182ms |
| SQLiteVec | 10 | 1 | 0.343 | 0.315 | 398,751 | 2,004 | 4,352ms |
| SQLiteVec | 20 | 1 | 0.329 | 0.308 | 621,790 | 3,125 | 4,180ms |
| SQLiteVec | 40 | 1 | 0.327 | 0.303 | 965,423 | 4,851 | 4,460ms |
| SQLiteVec | 10 | 2 | 0.342 | 0.312 | 659,981 | 3,316 | 5,198ms |

**解读**：

- **top-k 并非越大越好**：top-k=20/40 虽然增加了 prompt token，但 F1/BLEU
  略有下降。QA Agent 对检索噪声较敏感。
- `qa-search-passes=2` 在部分类别上有改善（如 multi-hop），但总体 F1 无提升。

---

### 4. 与 Python Agent 框架对比

我们在四个 Python Agent 框架——**AutoGen**、**Agno**、**ADK**、
**CrewAI**——上运行了相同的 LoCoMo 基准，均使用 GPT-4o-mini、
相同的 10 个样本（1,986 QA）及 LLM-as-Judge 评估。

#### 4.1 框架配置

| 框架 | 记忆后端 | 检索方式 | Embedding |
| --- | --- | --- | --- |
| **trpc-agent-go** | pgvector | 向量相似度（top-K）+ 多轮检索 | text-embedding-3-small |
| **AutoGen** | ChromaDB | 向量相似度（top-30） | text-embedding-3-small |
| **Agno** | SQLite | LLM 事实提取 → system prompt | 无 |
| **ADK** | 纯内存 | Agent 工具调用（LoadMemoryTool） | 内置 |
| **CrewAI** | 内置向量 | Crew 自动检索 | 内置 |

#### 4.2 各框架记忆方案详解

以下按记忆存储、检索、QA 调用流程三个维度，对比五个框架的具体
实现方案。所有框架的 benchmark 代码均使用相同的 system prompt
策略（五类 QA 分策略回答）和相同的评估流水线。

**trpc-agent-go（优化版）— Auto 提取 + pgvector 混合检索：**

- **存储**：对话 turn 经 LLM 自动提取为结构化 fact/episode（包含
  content、metadata、event_time 字段），写入 pgvector。
- **存储消息角色**：后台提取器的 `ExtractionContext.Messages`
  **同时包含 user 和 assistant 两种角色的消息**（不含 tool call），
  因此对话双方的内容均可用于 LLM 记忆提取
- **检索**：Agent 通过 `memory_search` 工具调用发起 pgvector
  混合检索（向量相似度 + 关键词匹配），返回 top-30 条结构化记忆
- **QA 流程**：3 次 LLM 调用（Step 1 生成搜索 #1 的 tool call →
  Step 2 生成搜索 #2 的 tool call → Step 3 读取全部检索结果后回答）
- **优势**：提取后的记忆更精准、信息密度高；混合检索兼顾语义和
  关键词匹配
- **Token 特征**：tool-call 模式导致每步重读前序上下文，名义
  prompt token 为 ~17,182/QA。但**其中 43.9% 命中了提供商的
  prompt cache**（OpenAI `cached_tokens`），实际*新增* prompt
  成本仅 ~9,663 tokens/QA——按计费口径（大多数提供商 cache
  token 按 50% 计费）已可与单次调用方案相当
- **问题**：结构化 JSON 格式增加序列化开销；多步延迟高于
  单次调用模式

**AutoGen — ChromaDB 原始 turn 存储 + 单次 LLM 调用：**

- **存储**：原始对话 turn 以 `[SessionDate: ...] Speaker: text`
  格式直接存入 ChromaDB，仅做 embedding，不做 LLM 提取。
- **存储消息角色**：框架不自动存储——`ChromaDBVectorMemory.add()`
  是纯手动 API，由调用方决定存储内容。本评测中我们手动逐条
  `add()`，不区分 role
- **检索**：`AssistantAgent.run()` 前，`ChromaDBVectorMemory.
  update_context()` 自动以 question 为 query 检索 top-30 结果
  （score ≥ 0.3），作为 `SystemMessage` 注入 model context
- **QA 流程**：**1 次 LLM 调用**——检索结果在调用前已预注入，
  无需 tool call
- **优势**：调用次数最少（1 call/QA），token 效率最高
  （1,943 tokens/QA）
- **问题**：adversarial F1 仅 0.272（所有框架最低），对抗鲁棒性
  严重不足；依赖 ChromaDB 纯向量搜索，缺少关键词/BM25 补充

**CrewAI — ShortTermMemory + Crew 两步调用：**

- **存储**：原始对话 turn 存入 CrewAI 内置
  `ShortTermMemory`（底层为 ChromaDB 向量库），不做 LLM 提取。
- **存储消息角色**：框架存储的是**任务级执行摘要**（task
  description + agent role + expected output + 最终结果文本），
  而非逐条消息。本评测中我们绕过了框架的自动存储，手动逐条
  `stm.save()` 存入
- **检索**：通过 monkey-patch `ContextualMemory._fetch_stm_context`
  扩大检索窗口至 top-30（默认仅 top-5），格式化为
  `- [content]` 列表注入 agent 上下文
- **QA 流程**：2 次 LLM 调用——Call 1 为 Crew 内部
  formatting/planning，Call 2 带记忆上下文回答
- **优势**：存储简单（无 LLM 提取成本），检索结果格式紧凑
- **问题**：向量检索召回不足；Crew 的 Call 1（planning 步骤）
  是纯框架开销，贡献了 ~140 completion tokens/QA 但无 F1
  收益；adversarial 和 temporal 类别丢失率分别达 44.6% 和 39.6%

**ADK — InMemoryMemoryService + LoadMemoryTool 全量加载：**

- **存储**：对话 turn 作为 `Event` 存入 ADK
  `InMemoryMemoryService`（纯内存，无持久化）。
- **存储消息角色**：`add_session_to_memory()` 存储**所有**含
  `content.parts` 的 event，不按 author 过滤——**user、model、
  tool 等全类型 event 均被存储**
- **检索**：Agent 通过 `LoadMemoryTool` 工具调用加载记忆——
  **不做任何选择性检索，将全部记忆无差别注入上下文**
- **QA 流程**：2 次 LLM 调用（Step 1 调用 LoadMemoryTool →
  Step 2 读取全部记忆后回答）
- **优势**：不丢失任何记忆信息
- **问题**：**token 消耗灾难性膨胀**（49,224 tokens/QA，
  是优化版的 2.9 倍）；9 个 QA 超过 128K tokens 导致上下文
  溢出；10 个 QA 返回空预测；最大单 QA 达 252,849 tokens

**Agno — LLM 事实提取 + SQLite 全量注入：**

- **存储**：每个对话 turn 经 `MemoryManager` 调用 LLM 提取
  事实/偏好，存入 SQLite 数据库（有 LLM 提取成本，但不计入
  QA token 统计）。
- **存储消息角色**：`make_memories()` **仅处理 user message**，
  不含 assistant 或 tool 消息。`create_or_update_memories()` 内部
  也显式过滤 `m.role == 'user'`
- **检索**：`add_memories_to_context=True` 将**所有**已存储记忆
  无差别注入 system prompt 的
  `<memories_from_previous_interactions>` 标签中，不做向量搜索或
  相似度过滤
- **QA 流程**：1 次 LLM 调用（记忆已在 system prompt 中）
- **优势**：LLM 提取保留了关键事实
- **问题**：**全量注入导致 10,436 tokens/QA**；延迟最高
  （14,127ms/QA，总耗时 7h47m）；底层 DB 预留的
  `limit`/`topics` 过滤参数从未
  被 `MemoryManager` 使用，是设计缺陷

**方案对比总结：**

| 维度 | Session Recall | trpc-agent-go (优化版) | AutoGen | CrewAI | ADK | Agno |
| --- | --- | --- | --- | --- | --- | --- |
| 存储消息角色 | user + assistant 原始 session event | user + assistant 抽取成结构化 memory | 不自动存储（手动 API） | 任务级摘要（输入+输出） | 全部 event（user+model+tool） | 仅 user（assistant 被排除） |
| 评测 turn 映射 | Speaker[0]→user, [1]→assistant | Speaker[0]→user, [1]→assistant | 逐条 turn 手动 add() | 逐条 turn 手动 save() | 逐条 turn→Event, 整 session 写入 | 逐条 turn→create_user_memories() |
| 存储方式 | 原始 session events | LLM 提取结构化 memory | 原始 turn | 原始 turn | 原始 turn | LLM 提取事实 |
| 检索方式 | 对 session events 做 hybrid RRF，单次 preload | 向量+关键词 hybrid + tool call | 纯向量 top-30 | 纯向量 top-30 | **全量加载** | **全量注入** |
| LLM 调用/QA | 1（preload） | 3（tool call） | **1**（预注入） | 2（Crew 内部） | 2（tool call） | 1（预注入） |
| Tokens/QA | 3,694（有效 3,567†） | 17,182（有效 9,663‡） | **1,943** | 2,839 | 49,224 | 10,436 |

> † Session Recall 的 cache 命中率为 3.7%，实际*新增* token
> 成本约为 3,567/QA。
>
> ‡ 优化版有 43.9% 的 prompt tokens 命中提供商 prompt
> cache，实际*新增* token 成本约为 9,663/QA。
>
> 核心发现：**检索策略是区分效果的关键**。全量加载（ADK/Agno）
> 浪费 token 且效果不佳；选择性检索（Session Recall / 优化版 /
> AutoGen / CrewAI）的效果显著更好。在这些选择性检索方案里，
> Session Recall 现在在保持低 token 档位的同时给出了最高的总体
> 质量，而优化版则仍是更偏抽取式、tool-driven 的另一条路线。

#### 4.3 总体结果

**表 7：Memory 场景——总体指标**

| 框架 | F1 | BLEU | LLM Score | Tokens/QA | 调用/QA | 延迟 | 总耗时 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| **trpc-agent-go (Session Recall)** | **0.549** | **0.511** | **0.609** | 3,694† | 1.0 | 6,430ms | 3h33m |
| trpc-agent-go (优化版) | 0.469 | 0.431 | 0.532 | 17,182‡ | 3.0 | 8,585ms | 4h44m |
| AutoGen | 0.457 | 0.414 | 0.540 | 1,943 | 1.0 | 3,816ms | 2h06m |
| CrewAI | 0.427 | 0.385 | 0.479 | 2,839 | 2.0 | 8,081ms | 4h27m |
| ADK | 0.362 | 0.309 | 0.476 | 49,224 | 2.0 | 5,578ms | 3h04m |
| trpc-agent-go (原版) | 0.399 | 0.371 | 0.416 | 3,056 | 2.0 | 6,659ms | 3h40m |
| Agno | 0.332 | 0.289 | 0.494 | 10,436 | 1.0 | 14,127ms | 7h47m |

> † Session Recall 的 cache 命中率为 3.7%，实际新增 token 成本
> 约为 ~3,567/QA。
>
> ‡ 优化版有 43.9% 的 prompt tokens 命中提供商 prompt
> cache，实际新增 token 成本仅 ~9,663/QA。详见 4.5 节。

> **LLM Score 聚合口径说明。** 所有框架均使用全样本分母
>（accuracy 口径：`sum(llm_score) / total_qa`）。Python 框架
> 的原始报告使用了 precision 口径（仅除以有评分的 QA 数），
> 因此 0.93 左右的值并不可直接对比，这里已统一修正。

```
Memory F1 (10 samples, 1986 QA)

trpc-agent-go (Session Recall) |====================================================| 0.549
trpc-agent-go (优化版)         |============================================        | 0.469
AutoGen                        |=========================================           | 0.457
CrewAI                         |========================================            | 0.427
trpc-agent-go (原版)           |=====================================               | 0.399
ADK                            |==================================                  | 0.362
Agno                           |===============================                     | 0.332
                               +----------------------------------------------------+
                               0.0      0.1      0.2      0.3      0.4      0.5
```

#### 4.4 各类别 F1

**表 8：各类别 F1 对比**

| 类别 | Count | Session Recall | trpc-agent-go (优化版) | AutoGen | CrewAI | trpc-agent-go (原版) | ADK | Agno |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| single-hop | 282 | 0.368 | **0.396** | 0.377 | 0.322 | 0.316 | 0.299 | 0.240 |
| multi-hop | 321 | **0.554** | 0.453 | 0.512 | 0.380 | 0.096 | 0.418 | 0.283 |
| temporal | 96 | 0.174 | **0.247** | 0.176 | 0.140 | 0.088 | 0.120 | 0.076 |
| open-domain | 841 | **0.618** | 0.441 | 0.594 | 0.501 | 0.358 | 0.494 | 0.292 |
| adversarial | 446 | 0.610 | 0.626 | 0.272 | 0.448 | **0.814** | 0.163 | 0.556 |

**表 9：加权平均 F1**

| 平均方式 | Session Recall | trpc-agent-go (优化版) | AutoGen | CrewAI | trpc-agent-go (原版) | ADK | Agno |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| 5 类加权 (÷1986) | **0.549** | 0.469 | 0.457 | 0.427 | 0.399 | 0.362 | 0.332 |
| 4 类加权 (÷1540) | **0.531** | 0.423 | 0.511 | 0.420 | 0.279 | 0.420 | 0.267 |

> 优化版相较原版依然有明显进步，尤其在 **single-hop** 和
> **temporal** 上改善显著；Session Recall 应被看作在这一内部
> 演进基础上的补充检索路径。
>
> 5 类加权 F1 方面，**Session Recall 以 0.549 排名第一**，
> 领先优化版（0.469）0.080，领先 AutoGen（0.457）0.092。
> 4 类加权 F1 也以 **0.531 排名第一**，超过 AutoGen 的 0.511
> 达 0.020，并显著领先其他 trpc-agent-go 方案和专用记忆系统。

#### 4.5 Token 效率与延迟

**表 10：Token 效率对比**

| 框架 | F1 | Total Tokens | Tokens/QA | Cache 命中率 | 有效 Tokens/QA† | F1/十亿 Tokens |
| --- | ---: | ---: | ---: | ---: | ---: | ---: |
| AutoGen | 0.457 | 3,859,412 | 1,943 | n/a | 1,943 | 118.4 |
| trpc-agent-go (Session Recall) | **0.549** | 7,353,057 | 3,694 | 3.7% | 3,567 | 74.6 |
| CrewAI | 0.427 | 5,639,085 | 2,839 | n/a | 2,839 | 75.7 |
| trpc-agent-go (原版) | 0.399 | 6,068,802 | 3,056 | n/a | 3,056 | 65.7 |
| trpc-agent-go (优化版) | 0.469 | 34,123,774 | 17,182 | **43.9%** | **9,663** | 13.7 |
| Agno | 0.332 | 20,725,728 | 10,436 | n/a | 10,436 | 16.0 |
| ADK | 0.362 | 97,759,453 | 49,224 | n/a | 49,224 | 3.7 |

> † **有效 Tokens/QA** = prompt tokens 减去 cached prompt tokens，
> 加上 completion tokens。Cached tokens 命中提供商的自动 prompt
> cache（如 OpenAI `cached_tokens`），通常按**标准 prompt 费率
> 的 50%** 计费。Python 框架的 SDK 不报告 `cached_tokens`，因此
> 它们的实际成本可能也低于表中所示；`n/a` 表示数据不可获取而非
> 无缓存。
>
> 从原始 token 数看，AutoGen 效率最高（118.4 F1/十亿 Tokens）。
> 优化版虽然名义 token 成本更高，但相较原版仍然代表了质量上的稳定
> 提升。**Session Recall 是当前 trpc-agent-go 内部最优的质量/成本
> 折中**：它以 3,694 tokens/QA 达到 0.549 F1，在显著低于
> Long-Context 和优化版 token 成本的同时，大幅超过它们的准确率。
> 优化版因为多步 tool-call 模式需要反复重读上下文，名义 token
> 显著更高；虽然 prompt cache 能缓解这部分成本，但在当前配置下
> Session Recall 仍然明显更轻量。ADK 效率最低——49,224 tokens/QA
> 仅获得 0.362 的 F1。

```
Total Evaluation Time (memory scenario, 1986 QA)

AutoGen            |====                                   | 2h06m
ADK                |======                                 | 3h04m
Session Recall     |=======                                | 3h33m
trpc (原版)        |========                               | 3h40m
CrewAI             |==========                             | 4h27m
trpc (优化版)      |==========                             | 4h44m
Agno               |===============================        | 7h47m
                   +----------------------------------------+
                   0h       2h       4h       6h       8h
```

**优化版为何更慢（4h44m vs 3h40m）：**

优化版消耗 5.6 倍的 tokens/QA（17,182 vs 3,056），单 QA 延迟增长
1.29 倍（8,585ms vs 6,659ms）。根因在于三步 Agent 工作流：

1. **Step 1 — 工具调用 #1**（~1,650 prompt tokens）：LLM 读取系统
   指令和问题后，发出第一次 `memory_search` 工具调用。这会产生一次
   LLM 往返加一次 pgvector 混合搜索（向量 + 关键词），包含 embedding
   生成。

2. **Step 2 — 工具调用 #2**（~5,900 prompt tokens）：LLM 重新读取
   所有前序上下文（系统 prompt + 问题 + 第一次工具调用 + 第一次工具
   结果），然后发出第二次 `memory_search` 工具调用以细化检索。

3. **Step 3 — 最终回答**（~10,000 prompt tokens）：LLM 重新读取完整
   对话历史（所有前序上下文 + 第二次工具调用 + 第二次工具结果），生成
   最终答案。

核心开销在于**累积上下文重读**：每一步都要重新处理所有前序步骤的内容。
仅 Step 3 就消耗了 ~10,000 prompt tokens。相比之下，原版使用 2 次调用
的 Agent 模式，但每次检索到的记忆条目更少更短（两步总计 ~3,056
tokens），因为原版存储的是原始对话 turn，而非提取后的结构化
fact/episode。

**Prompt cache 显著降低了实际成本：** 多步 tool-call 模式虽然反复
重读上下文，但恰恰因此具有极高的 cache 友好性——Step 2 和 Step 3
与前序步骤共享大量公共前缀。实际运行中，**43.9% 的 prompt tokens
（34.01M 中的 14.93M）命中了提供商的自动 prompt cache**，实际
新增 prompt 量仅为 ~19.08M tokens。按照标准 50% cache 定价，
实际可计费的 prompt 成本等效于 ~26.54M tokens 而非 34.01M——
比名义数字**低约 22%**。

尽管 token 成本更高，优化版的 F1/成本权衡显著更优：以 **5.6 倍
名义 token 成本**（计入 cache 折扣后远低于此）换取 **+17.5% F1
提升**（0.399→0.469），在重视回答质量的生产场景中是值得的。

#### 4.6 ADK 失败分析

ADK（Google Agent Development Kit）使用纯内存后端，通过 Agent
工具调用（`LoadMemoryTool`）检索记忆。在本次评估中，ADK 在部分
样本上出现了上下文溢出问题：

**表 11：ADK 上下文溢出详情**

| 样本 | QA 数 | 空预测数 | >128K Tokens QA 数 | 最大单 QA Token |
| --- | ---: | ---: | ---: | ---: |
| conv-26 | 199 | 0 | 0 | 43,887 |
| conv-30 | 105 | 0 | 0 | 59,458 |
| conv-41 | 193 | 4 | 4 | 252,849 |
| conv-42 | 260 | 1 | 1 | 180,603 |
| conv-43 | 242 | 2 | 2 | 162,249 |
| conv-44 | 158 | 1 | 0 | 123,063 |
| conv-47 | 190 | 0 | 0 | 114,912 |
| conv-48 | 239 | 1 | 0 | 105,680 |
| conv-49 | 196 | 0 | 1 | 166,597 |
| conv-50 | 204 | 1 | 1 | 219,026 |
| **合计** | **1,986** | **10** | **9** | **252,849** |

- **10 个 QA（0.5%）返回空预测**，集中在对话历史较长的样本中
- **53 个 QA 的 token 用量超过 100K**，单次 QA 最高达到
  **252,849 tokens**——接近 GPT-4o-mini 的 128K 上下文窗口上限
- ADK 的 `LoadMemoryTool` 将**全部记忆**加载到上下文中，
  不做选择性检索，导致长对话场景下严重的 token 浪费
- 平均 49,224 tokens/QA 是所有框架中最高的，但 F1 仅 0.362

#### 4.7 各样本 F1

**表 12：各样本 F1 对比**

| 样本 | QA 数 | Session Recall | trpc-agent-go (优化版) | AutoGen | CrewAI | trpc-agent-go (原版) | ADK | Agno |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| conv-26 | 199 | **0.530** | 0.432 | 0.384 | 0.355 | 0.331 | 0.337 | 0.296 |
| conv-30 | 105 | **0.636** | 0.422 | 0.451 | 0.439 | 0.302 | 0.379 | 0.334 |
| conv-41 | 193 | **0.644** | 0.521 | 0.513 | 0.440 | 0.432 | 0.335 | 0.387 |
| conv-42 | 260 | **0.482** | 0.447 | 0.439 | 0.408 | 0.378 | 0.343 | 0.338 |
| conv-43 | 242 | **0.542** | 0.436 | 0.486 | 0.413 | 0.451 | 0.355 | 0.341 |
| conv-44 | 158 | **0.553** | 0.505 | 0.491 | 0.509 | 0.455 | 0.384 | 0.289 |
| conv-47 | 190 | **0.530** | 0.487 | 0.496 | 0.405 | 0.407 | 0.374 | 0.321 |
| conv-48 | 239 | **0.563** | 0.492 | 0.463 | 0.432 | 0.404 | 0.392 | 0.328 |
| conv-49 | 196 | **0.508** | 0.464 | 0.418 | 0.407 | 0.383 | 0.371 | 0.302 |
| conv-50 | 204 | **0.562** | 0.478 | 0.475 | 0.487 | 0.407 | 0.363 | 0.374 |
| **平均** | **199** | **0.549** | 0.469 | 0.457 | 0.427 | 0.399 | 0.362 | 0.332 |

> Session Recall 在 10 个样本中的全部 10 个上超过 AutoGen。

---

### 5. 与外部记忆系统对比

数据来源：Mem0 论文 Table 1（Chhikara et al., 2025,
arXiv:2504.19413）。所有系统均使用 GPT-4o-mini。为跨系统可比性，
已排除 adversarial 类别（Mem0 论文未包含该类别）。

> **关于表中"LoCoMo（论文基线）"的说明。** LoCoMo 既是本报告
> 使用的数据集，也是 LoCoMo 论文（Maharana et al., 2024）中
> 提出的一套记忆系统方案。该方案使用 LLM 从对话中提取事件和
> 摘要，在推理时通过 BM25 + 语义搜索组合检索。Mem0 论文在同一
> 数据集上复现了该方案并报告了 F1 数据，因此表中以"LoCoMo
> （论文基线）"标注，表示这是 LoCoMo 论文自带的记忆方案的得分，
> 而非数据集本身。

**表 13：各类别 F1（不含 adversarial）**

| 方法 | Single-Hop | Multi-Hop | Open-Domain | Temporal | 4 类加权 | 来源 |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| AutoGen | 0.377 | 0.512 | 0.594 | 0.176 | 0.511 | 本工作 |
| **trpc-agent-go (Session Recall)** | 0.368 | **0.554** | **0.618** | 0.174 | **0.531** | 本工作 |
| trpc-agent (优化版) | **0.396** | 0.453 | 0.441 | 0.247 | 0.423 | 本工作 |
| Mem0g | 0.381 | 0.243 | 0.493 | **0.516** | 0.422 | Mem0 论文 |
| Mem0 | 0.387 | 0.286 | 0.477 | 0.489 | 0.421 | Mem0 论文 |
| CrewAI | 0.322 | 0.380 | 0.501 | 0.140 | 0.420 | 本工作 |
| trpc-agent (LC) | 0.320 | 0.308 | 0.518 | 0.088 | 0.411 | 本工作 |
| ADK | 0.299 | 0.418 | 0.494 | 0.120 | 0.420 | 本工作 |
| Zep | 0.357 | 0.194 | 0.496 | 0.420 | 0.403 | Mem0 论文 |
| LangMem | 0.355 | 0.260 | 0.409 | 0.308 | 0.362 | Mem0 论文 |
| A-Mem | 0.270 | 0.121 | 0.447 | 0.459 | 0.347 | Mem0 论文 |
| OpenAI Memory | 0.343 | 0.201 | 0.393 | 0.140 | 0.328 | Mem0 论文 |
| MemGPT | 0.267 | 0.092 | 0.410 | 0.255 | 0.308 | Mem0 论文 |
| LoCoMo（论文基线） | 0.250 | 0.120 | 0.404 | 0.184 | 0.303 | Mem0 论文 |
| trpc-agent (原版) | 0.316 | 0.096 | 0.358 | 0.088 | 0.279 | 本工作 |
| Agno | 0.240 | 0.283 | 0.292 | 0.076 | 0.267 | 本工作 |
| ReadAgent | 0.092 | 0.053 | 0.097 | 0.126 | 0.089 | Mem0 论文 |
| MemoryBank | 0.050 | 0.056 | 0.066 | 0.097 | 0.063 | Mem0 论文 |

```
4-Category Weighted F1 (excluding adversarial, 1540 QA)

Session Recall      |============================================| 0.531
AutoGen             |==========================================  | 0.511
trpc-agent (优化版) |==================================          | 0.423
Mem0g               |==================================        | 0.422
Mem0                |==================================        | 0.421
CrewAI              |=================================         | 0.420
ADK                 |=================================         | 0.420
trpc-agent (LC)     |=================================         | 0.411
Zep                 |================================          | 0.403
LangMem             |=============================             | 0.362
A-Mem               |===========================               | 0.347
OpenAI Memory       |==========================                | 0.328
MemGPT              |========================                  | 0.308
LoCoMo (baseline)   |========================                  | 0.303
trpc-agent (原版)   |======================                    | 0.279
Agno                |====================                      | 0.267
                    +------------------------------------------+
                    0.0      0.1      0.2      0.3      0.4   0.5
```

> **含 adversarial 的 5 类加权 F1**（仅限有 adversarial 数据的框架）：
>
> | 方法 | 5 类加权 F1 |
> | --- | ---: |
> | **trpc-agent-go (Session Recall)** | **0.549** |
> | trpc-agent (优化版) | 0.469 |
> | AutoGen | 0.457 |
> | CrewAI | 0.427 |
> | trpc-agent (原版) | 0.399 |
> | ADK | 0.362 |
> | Agno | 0.332 |

**核心发现：**

1. **trpc-agent-go（Session Recall）** 的 4 类加权 F1 达到
   **0.531**，排名 **第一**，超过 AutoGen（0.511）0.020，
   并显著超过 Mem0g（0.422）、Mem0（0.421）、Zep（0.403）、
   LangMem（0.362）、A-Mem（0.347）等专用记忆系统
2. **open-domain 与 multi-hop 成为突出强项。**
   Session Recall 在 **multi-hop**（0.554）和
   **open-domain**（0.618）上都达到第一
3. **优化版仍然是互补方案。** 它在 **temporal**
   （0.247）和 adversarial（0.626）上仍是 trpc-agent-go
   内部最强，但总体 4 类加权 F1（0.423）明显低于 Session Recall
4. **Token 效率显著改善。** Session Recall 将 nominal
   Tokens/QA 从优化版的 17,182 和 Long-Context 的
   18,776 直接降到 **3,694**
5. 相比原始基线，优化版先将 trpc-agent-go 的 4 类加权 F1
   从 0.279 提升到 0.423，而 Session Recall 又进一步将其推到
   0.531

---

### 6. Update Policy 结果

#### 6.1 评测口径

本节将使用默认 Merge Similar policy 的历史 Optimized 结果，与
Preserve History 和 Append Only 进行对比。两组新实验只改变 Auto
update policy。Assistant-episode 实验不纳入报告：LoCoMo
将第二位人类说话人映射到 assistant role，并不适合评估模型生成的
assistant result。

| 项目 | 值 |
| --- | --- |
| 数据集 | 官方 LoCoMo-10；10 个 conversation；1,986 QA |
| Dataset SHA-256 | `79fa87e90f04081343b8c8debecb80a9a6842b76a7aa537dc9fdf651ea698ff4` |
| 场景与后端 | Auto + pgvector |
| 回答与 judge 模型 | `gpt-4o-mini` |
| Embedding 模型 | `text-embedding-3-small` |
| 检索 | top-k 30；两轮 `memory_search` |
| QA 上下文 | 不注入 QA 历史；最大上下文 128,000 token |
| 指标 | F1、BLEU、LLM Score、分类指标、token、调用次数和延迟 |

#### 6.2 总体结果

| Policy 配置 | F1 | 差值 | BLEU | 差值 | LLM Score | 差值 | Active memories |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Optimized / Merge Similar | 0.4690 | - | 0.4310 | - | 0.5320 | - | unavailable |
| **Preserve History** | **0.4865** | **+1.75pp** | **0.4473** | **+1.63pp** | **0.5609** | **+2.89pp** | 2,740 |
| Append Only | 0.4773 | +0.83pp | 0.4397 | +0.87pp | 0.5441 | +1.21pp | 2,627 |

Preserve History 在三个总体指标上都是最强配置。Append Only
同样超过 Optimized baseline，并且比 Preserve History 少保留 113
条 active memory。

#### 6.3 分类指标

每个单元格依次为 `F1 / BLEU / LLM Score`。

| Policy 配置 | Single-hop | Multi-hop | Temporal | Open-domain | Adversarial |
| --- | --- | --- | --- | --- | --- |
| Optimized / Merge Similar | 0.396/0.325/0.395 | 0.453/0.415/0.519 | 0.247/0.192/0.364 | 0.441/0.398/0.552 | 0.626/0.626/0.626 |
| Preserve History | 0.386/0.319/0.387 | 0.530/0.484/0.603 | 0.242/0.196/0.415 | 0.479/0.432/0.607 | 0.586/0.585/0.585 |
| Append Only | 0.381/0.312/0.353 | 0.498/0.456/0.579 | 0.209/0.161/0.348 | 0.464/0.420/0.585 | 0.605/0.605/0.605 |

新 policy 的提升主要集中在 multi-hop 和 open-domain；Optimized
baseline 在 single-hop 和 adversarial F1 上仍然更强。

#### 6.4 可回答类别加权 F1

该口径排除 adversarial，使用固定的 1,540 道可回答 QA 作为分母。

| Policy 配置 | 加权 F1 | 相对 Optimized |
| --- | ---: | ---: |
| Optimized / Merge Similar | 0.4230 | - |
| **Preserve History** | **0.4579** | **+3.49pp** |
| Append Only | 0.4402 | +1.72pp |

#### 6.5 成本与耗时

| Policy 配置 | Prompt tokens | Completion tokens | Total tokens | 差值 | Cached tokens | LLM calls | 平均延迟 | 总耗时 |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Optimized / Merge Similar | 34,007,814 | 115,960 | 34,123,774 | - | unavailable | 5,981 | 8.59s | 4h44m |
| Preserve History | 35,097,721 | 118,823 | 35,216,544 | +3.20% | 15,295,232 | 5,983 | 10.83s | 5.98h |
| Append Only | 34,558,815 | 118,906 | 34,677,721 | +1.62% | 15,111,168 | 5,977 | 10.83s | 5.97h |

#### 6.6 结果完整性

- Preserve History 和 Append Only 都包含 10 个 conversation
  和固定 1,986 道 QA，具有结构化结果文件，并使用独立 pgvector
  表。
- 一次瞬时 embedding 504 已由重试恢复，没有替换 case 或分数。
- Optimized 数字来自报告中已有的历史 baseline。
  仓库内没有保留对应的原始结构化 result artifact，
  因此精确差值来自报告数值，不能描述成由已提交 artifact
  独立复现。

---

### 7. 结论

#### 核心发现

1. **trpc-agent-go 的 Session Recall 已成为当前最强配置。**
   它在 **5 类加权 F1 上排名第一**（**0.549**），4 类加权 F1
   也以 **0.531** 排名第一，并超过 AutoGen。相比 Long-Context
   和优化版，它在更低 token 成本下给出了更高的总体 F1。

2. **不同检索策略的权衡已经非常清晰。** Session Recall 在
   **open-domain** 和 **multi-hop** 上最强，因此适合作为
   跨 session QA 的默认方案；优化版在 **temporal**
   和 adversarial 上仍更强；Long-Context 则仍可作为短单 session
   场景下的上界参考。

3. **trpc-agent-go 已明显超越专用记忆系统。** Session Recall 的
   4 类加权 F1 达到 0.531，显著高于 Mem0g（0.422）、Mem0
   （0.421）、Zep（0.403）、LangMem（0.362）、A-Mem（0.347）、
   OpenAI Memory（0.328）、MemGPT（0.308）等专用记忆系统。

4. **其他 Python 框架的局限性。**

   - **ADK**：token 消耗最为严重（49,224 tokens/QA），是优化版的
     **2.9 倍**，但 F1 仅 0.362。其 `LoadMemoryTool` 将全部记忆
     无差别加载到上下文中，导致长对话场景下严重的 token 浪费和
     上下文溢出（9 个 QA 超过 128K tokens），架构上缺乏选择性
     检索能力
   - **Agno**：F1 最低（0.332），延迟最高（14,127ms/QA，总耗时
     7h47m），且 token 消耗达 10,436/QA。与 ADK 类似，Agno 也采用
     全量加载架构——将用户的所有记忆无差别注入到 system prompt 的
     `<memories_from_previous_interactions>` 标签中，不支持向量检索
     或相似度搜索。虽然底层 DB 接口预留了 `limit`、`topics` 等
     过滤参数，但 `MemoryManager` 在实际运行中从未使用这些能力
   - **CrewAI**：其短期记忆后端存在记忆丢失问题，尤其在
     adversarial（44.6%）和 temporal（39.6%）类别上丢失比例最高
   - **AutoGen**：4 类加权 F1 达到 0.511，但其高分主要依赖
     open-domain 单一类别的突出表现（0.594）；在 adversarial 上
     仅 0.272，为所有框架最低，对抗鲁棒性严重不足

5. **Memory 仍然是生产 Agent 的必需能力。** Long-Context 在单
   session 短对话中有效，但无法跨 session 持久化知识，也无法扩展到
   超过模型上下文窗口的历史。Session Recall 提供了更好的质量/成本
   平衡，而优化版则提供了基于抽取式持久化 memory 的第二种
   路线。

6. **temporal 仍然是下一步重点优化方向。** 优化版在
   temporal 上达到 0.247，但 Session Recall 目前仍是 0.174。
   时间感知检索、temporal query rewrite 和更强的 rerank 仍是后续
   优先方向。

#### 生产建议

| 使用场景 | 推荐方案 |
| --- | --- |
| 短对话单 session（< 50K tokens） | Long-Context（无需记忆） |
| 跨 session QA / 以准确率优先 | Session Recall |
| 长期运行 Agent（数周/数月历史） | 优化版 |
| 历史超出上下文窗口限制 | Session Recall 或优化版 |

---

## LongMemEval

LoCoMo 评测之后，我们换用更强调跨 session 用户记忆的
LongMemEval，补充两个在 LoCoMo 上无法评估的维度：update policy
在长跨度输入下的行为，以及 assistant-episode 提取的影响。

本章结果是开发阶段在 50-case 子集上得到的，
用于配置之间的相对比较，不作为该基准上的正式成绩。

### 1. 数据集与 case 选取

LongMemEval（Wu et al., 2024）以多 session
的用户/助手对话考察长期记忆能力，共 500 道问题，
分为六种问题类型。本章使用 **LongMemEval-S** 的 cleaned 版本
`longmemeval_s_cleaned.json`：每道问题配一份由多个历史 session
组成的 haystack，全集 500 个 case 共 23,867 个 session，单个
case 38 至 62 个 session，中位数 48。
六类问题中都可能出现**拒答题**（question ID 带 `_abs` 后缀），
其正确行为是判断历史中没有相应证据并拒绝作答，全集共 30 道。

| 问题类型 | 全集题数 | 其中拒答题 | 本章 case 数 |
| --- | ---: | ---: | ---: |
| knowledge-update | 78 | 6 | 8 |
| multi-session | 133 | 12 | 13 |
| single-session-assistant | 56 | 0 | 6 |
| single-session-preference | 30 | 0 | 3 |
| single-session-user | 70 | 6 | 7 |
| temporal-reasoning | 133 | 6 | 13 |
| 合计 | 500 | 30 | 50 |

单个配置跑完一遍需要约 30 小时，在本轮的时间与 token
预算下无法对 500 个 case 逐一评测，因此**按问题类型分层，
每一类等比例抽取 10%**：取整后为 8、13、6、3、7、13，合计 50 个
case，各类占比与全集保持一致。拒答题不单独设配额，
随分层抽样自然带入 3 道，分别落在 knowledge-update、
multi-session 和 single-session-user。

抽出的 50 个 case 以 case ID 固定下来，本章所有实验共用这一批
case 和同一顺序。该清单没有记录随机种子，
无法由数据集顺序重新推导，复现时请直接使用**附录 D** 的清单。

### 2. 实验设置

| 项目 | 值 |
| --- | --- |
| 数据集 | LongMemEval-S cleaned（`longmemeval_s_cleaned.json`） |
| Case | 按问题类型等比例抽取的 50 case，覆盖全部六类问题 |
| 分布 | knowledge-update 8；multi-session 13；assistant 6；preference 3；user 7；temporal 13 |
| 输入规模 | 2,353 sessions；24,370 turns；12,280 user/assistant pairs |
| 建库协议 | 有序 turn-pair fragments；各场景共用同一份回放输入 |
| Build chunk 上限 | 6,000 `cl100k_base` tokens；一个超限 pair 的内容被无损拆到多个独立 extraction 边界 |
| 回答、提取与评判模型 | `glm52` |
| Embedding 模型 | `text-embedding-ada-002` |
| 检索 | 标准 `memory_search`，固定 `top-k=20` |
| Benchmark revision | `c8c305c4c50594e3d083e06a5248cfeb81b15823` |
| trpc-agent-go revision | `1b3adb2f4bb8` |
| 主要指标 | 固定分母的 LLM-judge Accuracy |

各场景使用相同的回放输入和 case 顺序。建库通常对每个
user/assistant pair 执行一次；超过 chunk 上限的 pair
会被无损拆分，但每个 fragment 是独立的 Runner 调用和 extraction
边界，受影响的 case ID 记录在 provenance 中。同一来源 session
中的 pair 保持原 session 身份，不同来源 session 在同一 case
的用户级 memory 下相互隔离。Auto 通过 extractor reference-date
API 获得 observation date；Mem0 OSS
2.0.11（`3b9aed866ae70d29043388ed0ae5cc4e1844f3e8`）通过受支持的
extraction `prompt` 字段获得相同日期。QA 使用 fresh session，
只能看到当前问题、问题日期和 `memory_search` 返回结果；gold
answer 和 evidence 仅用于评测与诊断。

三个 Auto 场景只改变 update policy，并使用独立 pgvector 表；Mem0
保留其原生提取和 reconcile 行为。未能完成的 case 保留在固定 50
分母中并记为错误。

### 3. Update Policy 主结果

| 场景 | Policy | 成功执行 | 失败 | 正确 | Accuracy | F1 | BLEU | ROUGE-L | 耗时 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Auto | Merge Similar | 48/50 | 2 | 14 | 0.2800 | 0.0961 | 0.0635 | 0.0885 | 28h34m |
| Auto | Preserve History | 50/50 | 0 | 41 | 0.8200 | 0.1683 | 0.1079 | 0.1614 | 29h51m |
| Auto | Append Only | 50/50 | 0 | 41 | 0.8200 | **0.1730** | **0.1112** | **0.1679** | 28h07m |
| Mem0 OSS | native | 50/50 | 0 | 42 | **0.8400** | 0.1681 | 0.1067 | 0.1588 | 30h15m |

耗时为该场景 50 个 case 的建库与 QA 总时长。
失败列指未能给出答案的 case，仍保留在固定 50 分母中并记为错误；
Merge Similar 的两个失败来自 QA 超过八次工具迭代。

#### 各问题类型 Accuracy

| 问题类型 | 数量 | Merge Similar | Preserve History | Append Only | Mem0 OSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| knowledge-update | 8 | 0.5000 | 0.8750 | **1.0000** | 0.8750 |
| multi-session | 13 | 0.2308 | **0.8462** | 0.7692 | 0.7692 |
| single-session-assistant | 6 | 0.0000 | 0.0000 | 0.1667 | **0.5000** |
| single-session-preference | 3 | 0.0000 | **1.0000** | **1.0000** | **1.0000** |
| single-session-user | 7 | 0.2857 | **1.0000** | 0.8571 | **1.0000** |
| temporal-reasoning | 13 | 0.3846 | **1.0000** | **1.0000** | 0.9231 |

从表格中可以看到，框架相比 Mem0 OSS 的最大不足在
single-session-assistant 上。

### 4. Assistant-Episode 提取消融

本节在同样的 50 个 case 上，对比三种 Auto update policy
在关闭和开启 assistant-episode 提取时的结果，共六组实验。
开启后的行使用当时的条件式两阶段实现：先执行普通用户记忆提取，
仅在当前 user/assistant pair 明显包含结构化结果时，
才发起一次输入受限的 assistant-result 提取。
该实现早于最终合入的版本，此后 eligibility、grounding
和请求构造都有变化，因此开启行只能作为历史参考，
不代表当前实现的结果。Mem0 保持其原生提取行为，
作为外部参照行列出。

七行实验使用完全相同的 50 个 case
及顺序、`glm52`、`text-embedding-ada-002`、相同的有序 turn-pair
fragments、6,000-token chunk 上限和 `top-k=20`。

| Policy / 后端 | Assistant 提取 | QA 成功 | 失败 | 正确 | Accuracy | F1 | BLEU | ROUGE-L |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | 关闭 | 48/50 | 2 | 14 | 0.2800 | 0.0961 | 0.0635 | 0.0885 |
| Merge Similar | 开启 | 45/50 | 5 | 29 | 0.5800 | 0.1626 | 0.1083 | 0.1518 |
| Preserve History | 关闭 | 50/50 | 0 | 41 | 0.8200 | 0.1683 | 0.1079 | 0.1614 |
| Preserve History | 开启 | 48/50 | 2 | **45** | **0.9000** | 0.1876 | 0.1204 | 0.1779 |
| Append Only | 关闭 | 50/50 | 0 | 41 | 0.8200 | 0.1730 | 0.1112 | 0.1679 |
| Append Only | 开启 | 47/50 | 3 | 44 | 0.8800 | **0.1970** | **0.1295** | **0.1865** |
| Mem0 OSS（参照） | 原生 | 50/50 | 0 | 42 | 0.8400 | 0.1681 | 0.1067 | 0.1588 |

失败 QA 仍保留在固定 50-case 分母中并记为错误。Assistant
开启行不是按得分筛选的替换结果，每一行都代表一组完整的实验配置。

#### 各问题类型 Accuracy（开启 assistant 提取）

| 问题类型 | 数量 | Merge Similar | Preserve History | Append Only | Mem0 OSS |
| --- | ---: | ---: | ---: | ---: | ---: |
| knowledge-update | 8 | 0.7500 | **1.0000** | **1.0000** | 0.8750 |
| multi-session | 13 | 0.3077 | 0.7692 | **0.8462** | 0.7692 |
| single-session-assistant | 6 | **0.8333** | **0.8333** | **0.8333** | 0.5000 |
| single-session-preference | 3 | **1.0000** | 0.6667 | 0.6667 | **1.0000** |
| single-session-user | 7 | **1.0000** | **1.0000** | 0.8571 | **1.0000** |
| temporal-reasoning | 13 | 0.3077 | **1.0000** | 0.9231 | 0.9231 |

与第 3 节关闭 assistant 提取时的同类型结果相比，
single-session-assistant 是收益最集中的类型：三种 policy 分别从
0.0000、0.0000 和 0.1667 一致升到 0.8333，也高于 Mem0 的
0.5000。Merge Similar 的提升覆盖面最广，single-session-user 从
0.2857 升到 1.0000，knowledge-update 从 0.5000 升到 0.7500。
开启提取后也出现了个别回退，每一处都只对应一个 case：Preserve
History 与 Append Only 的 single-session-preference 从 1.0000
降到 0.6667，Preserve History 的 multi-session 从 0.8462 降到
0.7692，Append Only 的 temporal-reasoning 从 1.0000 降到
0.9231，Merge Similar 的 temporal-reasoning 从 0.3846 降到
0.3077。assistant 提取会对其他类型记忆产生一定影响，
但影响程度相对有限。建议在实际应用中，仅在 assistant
会返回重要事实等希望保存 assistant 信息时开启，
默认情况下保持关闭。

### 5. 记忆规模与成本

| 场景 | 完整建库 Case | 最终条目总数 | 每 Case 均值 | 中位数 | 范围 |
| --- | ---: | ---: | ---: | ---: | ---: |
| Auto Merge Similar | 50/50 | 2,955 | 59.10 | 58 | 35-87 |
| Auto Preserve History | 50/50 | 15,353 | 307.06 | 305.5 | 257-381 |
| Auto Append Only | 50/50 | 16,280 | 325.60 | 326 | 264-396 |
| Mem0 OSS | 50/50 | 28,041 | 560.82 | 564.5 | 465-602 |

该统计取每个完整建库 case 在建库结束时的记忆条目。通用 snapshot
reader 最多请求 10,000 条，Mem0 OSS adapter 的可观测上限为 1,000
条，两者都高于本轮观测到的最大值 602，因此没有发生条目截断。
这里统计的是 ingestion 完成后的 active memory entries，而非
extraction operation 数量，也不是多个 case
共享数据库中的物理行数。

开启 assistant-episode 提取后的记忆规模对比如下。

| Policy / 后端 | Assistant 提取 | Active memories | 完整建库 | 每完整建库 Case 条目数 |
| --- | --- | ---: | ---: | ---: |
| Merge Similar | 关闭 | 2,955 | 50/50 | 59.10 |
| Merge Similar | 开启 | 6,011 | 50/50 | 120.22 |
| Preserve History | 关闭 | 15,353 | 50/50 | 307.06 |
| Preserve History | 开启 | 17,696 | 49/50 | 361.14 |
| Append Only | 关闭 | 16,280 | 50/50 | 325.60 |
| Append Only | 开启 | 18,728 | 49/50 | 382.20 |
| Mem0 OSS | 原生 | 28,041 | 50/50 | 560.82 |

统计只计入完整建库的 case。开启 assistant 提取后，Preserve
History 与 Append Only 各有一个 case 未完成建库，
这两行的条目总数相应少算了一个 case。

| Policy / 后端 | Assistant 提取 | Build LLM calls | Build LLM tokens | QA+judge calls | QA+judge tokens | Build embedding requests | 远程 embedding calls | Cache hits |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | 关闭 | 12,281 | 96,999,123 | 240 | 2,048,381 | 37,907 | 26,074 | 11,833 |
| Merge Similar | 开启 | 15,647 | 83,758,408 | 201 | 1,619,884 | 40,254 | 28,737 | 11,517 |
| Preserve History | 关闭 | 12,336 | 95,086,050 | 163 | 434,852 | 28,028 | 27,581 | 447 |
| Preserve History | 开启 | 15,602 | 86,783,156 | 153 | 399,363 | 30,579 | 30,122 | 457 |
| Append Only | 关闭 | 12,411 | 84,701,403 | 168 | 467,266 | 28,860 | 28,401 | 459 |
| Append Only | 开启 | 15,496 | 76,326,984 | 153 | 406,198 | 31,044 | 30,583 | 461 |
| Mem0 OSS | 原生 | 12,359 | 113,205,317* | 171 | 410,912 | 23,044 | 23,044 | 0 |

成本统计包含失败或未完整建库 attempt 已经实际发出的请求，
与上面按最后一次成功 snapshot 统计的 memory inventory 口径不同。
开启 assistant 提取后 build LLM 调用从 12,281-12,411 次升到
15,496-15,647 次，新增 3,085-3,366 次，约相当于 12,280 个 pair
中的四分之一触发了第二次提取。

`*` Mem0 proxy 记录了调用和可观测 token 字段，但 provider 记录将
build LLM 与 embedding token 标记为 `tokens_known=false`，
不能将其解释为完整 provider usage。共享 cache
和并发运行也意味着远程调用量与 wall-clock
不能单独作为后端速度排名。该表与第 3 节使用相同的 case 集合。

### 6. 检索归因与记忆结构审计

#### 6.1 Gold session 召回与失败归因

gold session 召回衡量 QA 阶段的检索是否覆盖了数据集标注的答案
session。它按 case 内已命中的答案 session 比例计算，取值在 0 到
1 之间；平均值只在已产出答案的 case 上统计（Merge Similar 关闭
48 个、开启 45 个，Preserve History 开启 48 个，Append Only 开启
47 个，其余配置为 50 个）。「其余 case」一列合并了部分召回、
零召回与未产出答案三种情况。

| 配置 | Assistant 提取 | 平均 gold session 召回 | 完全召回 case | 其中答对 | 其余 case | 其中答对 | memory_search 调用 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | 关闭 | 0.6024 | 18 | 6 | 32 | 8 | 195 |
| Merge Similar | 开启 | 0.9148 | 38 | 26 | 12 | 3 | 116 |
| Preserve History | 关闭 | 0.9700 | 48 | 41 | 2 | 0 | 88 |
| Preserve History | 开启 | 0.9896 | 47 | 44 | 3 | 1 | 75 |
| Append Only | 关闭 | 0.9700 | 48 | 40 | 2 | 1 | 93 |
| Append Only | 开启 | 1.0000 | 47 | 44 | 3 | 0 | 76 |
| Mem0 OSS | 原生 | 0.9500 | 46 | 40 | 4 | 2 | 98 |

检索是回答的必要条件：七组实验合计 11 个零召回 case 中只有 1
个答对。

Merge Similar 的损失同时出现在检索和内容两层。关闭 assistant
提取时它的平均召回只有 0.6024，而且即使在 18 个完全召回的 case
上也只答对 6 个（0.3333），同期 Preserve History 为
41/48（0.8542）。也就是说合并后的条目往往仍指向正确的 session，
却已经不含回答所需的细节。它的 memory_search 调用次数也最高（195
次，其余六组为 75 到 116 次），说明模型反复改写 query
之后仍拿不到证据。

开启 assistant 提取把 Merge Similar 的平均召回提高到 0.9148，
完全召回 case 的正确率提高到 26/38（0.6842），但仍低于 Preserve
History 与 Append Only 开启后的 44/47（0.9362）。

该指标以答案 session 为单位，不是 evidence span，
因此「完全召回但答错」既可能是记忆内容缺失，也可能是同一 session
内的证据没有被选中。

#### 6.2 高相似记忆结构审计

本小节的记忆总体与第 5 节完全一致：同样的六组运行，
同样的恢复建库与未完成建库处理规则，条目数分别为 2,955、15,353、
16,280（关闭 assistant 提取）和 6,011、17,696、18,728（开启）。
审计在同一 case 内取全部 cosine ≥ 0.90 的 memory pair，
按确定性文本判据归入五类：规范化正文完全相同、严格词法近重复、
单向信息包含、高重合但数值或否定词不一致，以及仅向量相似。
前三类合称重复/包含型。判据不调用模型，也不给出语义等价判断。

本地审计脚本为 `memory/adapter/longmemeval_memory_audit.py`。
由于输入快照包含数据集文本，快照及生成产物均不纳入版本控制；
审计总体与第 5 节条目数不一致时，脚本直接报错。

| 配置 | Assistant 提取 | 记忆条目 | ≥0.90 pair | 每千条 pair | 重复/包含型 | 数值或否定不一致 | 仅向量相似 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | 关闭 | 2,955 | 279 | 94.4 | 18（6.5%） | 16（5.7%） | 245（87.8%） |
| Preserve History | 关闭 | 15,353 | 8,472 | 551.8 | 1,422（16.8%） | 733（8.7%） | 6,317（74.6%） |
| Append Only | 关闭 | 16,280 | 8,742 | 537.0 | 407（4.7%） | 1,022（11.7%） | 7,313（83.7%） |
| Merge Similar | 开启 | 6,011 | 1,232 | 205.0 | 58（4.7%） | 66（5.4%） | 1,108（89.9%） |
| Preserve History | 开启 | 17,696 | 8,952 | 505.9 | 1,473（16.5%） | 482（5.4%） | 6,997（78.2%） |
| Append Only | 开启 | 18,728 | 7,795 | 416.2 | 451（5.8%） | 314（4.0%） | 7,030（90.2%） |

pair 数随条目数近似二次增长，绝对值不能横向比较，「每千条 pair」
列是按记忆条目归一后的口径。

cosine ≥ 0.95 区间的类别构成如下。

| 配置 | Assistant 提取 | ≥0.95 pair | 完全相同 | 严格近重复 | 单向包含 | 数值或否定不一致 | 仅向量相似 |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Merge Similar | 关闭 | 11 | 0 | 4 | 1 | 2 | 4 |
| Preserve History | 关闭 | 1,885 | 5 | 496 | 433 | 474 | 477 |
| Append Only | 关闭 | 861 | 1 | 96 | 12 | 445 | 307 |
| Merge Similar | 开启 | 119 | 0 | 17 | 3 | 32 | 67 |
| Preserve History | 开启 | 1,563 | 3 | 514 | 434 | 247 | 365 |
| Append Only | 开启 | 441 | 1 | 137 | 16 | 77 | 210 |

- 归一化后 Merge Similar 的高相似 pair 密度最低（每千条 94.4
  对），Preserve History 与 Append Only 接近（551.8 与 537.0）。
  在 cosine ≥ 0.98 区间，Merge Similar 没有任何 pair，Preserve
  History 有 567 对并覆盖全部 50 个 case，Append Only 有 39 对、
  覆盖 14 个 case。
- 重复/包含型集中在 Preserve History：全区间 16.8%，cosine ≥
  0.95 区间 49.5%；Append Only 在同一区间只有 12.7%。
- 高向量相似不等于文本一致：cosine ≥ 0.95 区间内 Append Only 有
  445/861（51.7%）、Preserve History 有 474/1,885（25.1%）的
  pair 被判为数值或否定词不一致。

本节只描述记忆之间的文本结构。分类是结构信号而不是语义标签，
审计也没有在 case 级别把 pair 分类、update
操作与答案正误关联起来，因此不能据此把某个错误答案归因到某种
policy 的合并行为。开启 assistant 提取的三行与第 4 节一样，
只作历史参考。

### 7. 分析

1. **Update policy 是本轮样本中最主要的影响因素。** Preserve
   History 将原始 Accuracy 从 28% 提升到 82%。Merge Similar
   答对的 case 在 Preserve History 中全部仍然正确，Preserve
   History 额外答对 27 个 case，且没有仅在 Merge Similar
   中答对的回退 case。在完整建库 case 中，Preserve History 每
   case 最终保留的条目约为 Merge Similar 的 5.2 倍，说明 Merge
   Similar reconcile policy 会显著压缩最终证据集合。
2. **Preserve History 与 Append Only 的 Accuracy 相同，
   但保留的信息不同。** 两者共同答对 39 个 case，
   各有两个独有正确 case。Preserve History 在 multi-session 和
   single-session-user 上更强；Append Only 在 knowledge-update、
   assistant 问题和词面重合指标上更强。
3. **Mem0 与两个新 Auto policy 具有互补性。** Mem0 与 Preserve
   History 共同答对 38 个 case；Mem0 有四个独有正确 case，
   Preserve History 有三个。Mem0 在 assistant-memory
   问题上最强，Preserve History 则在 multi-session 和 temporal
   Accuracy 上领先。
4. **Assistant-episode 提取在三种 policy 上都是正向的，
   但收益递减。** 开启后 Merge Similar、Preserve History 和
   Append Only 的 Accuracy 分别提高 30、8 和 6 个百分点：
   基线越弱，补充 assistant 侧证据的收益越大。Preserve History +
   assistant 的 Accuracy 最高（0.9000），Append Only + assistant
   的词面重合指标最高（F1 0.1970、BLEU 0.1295、ROUGE-L
   0.1865）。这些增益来自当时的提取实现，
   需要用当前实现重跑后才能重新归因。
5. **收益伴随记忆规模和失败率的上升。** 开启 assistant 提取后
   active memories 分别增加 3,056、2,343 和 2,448 条，同时 QA
   失败数从 2/0/0 变为 5/2/3，其中两组各有一个 case 未完成建库。
   Mem0 是原生外部 baseline，不启用 Auto 专属的 assistant 选项。

### 8. 有效性限制

- 这 50 个 case 是按问题类型等比例抽取的 10% 样本，不是
  LongMemEval 官方的 dev/holdout 划分。清单固定了精确样本，
  但没有定义 seeded blind split，
  因此结论只适用于配置之间的相对比较，
  不能当作该基准上的最终成绩或盲测 holdout baseline。
- 样本规模较小：单个 case 的正误变化就对应 2 个百分点，policy
  之间 2 个百分点以内的差异不应被解释为稳定差距。
- 第 4 节开启 assistant 提取的两组各有一个 case 未完成建库，
  其记忆规模略有低估，因此这六组结果应作为开发诊断使用。
- 开启 assistant 提取的三组运行使用的是当时的两阶段实现，
  早于最终合入的版本，此后提取条件与请求构造都有变化，
  因此这三行只能作为历史参考，不代表当前实现的结果。
- 6.1 的召回指标以答案 session 为单位，不是 evidence span，
  因此不能把「同 session」直接等同于「可回答」。
- 6.2 的分类是确定性文本判据给出的结构信号，不是语义标签；
  审计没有在 case 级别把 pair 分类与 update 操作或答案正误关联，
  因此不能用来对具体错误答案做因果归因。

---

## 附录

### A. LoCoMo 实验环境

| 组件 | 版本/配置 |
| --- | --- |
| 框架 | trpc-agent-go |
| 模型 | gpt-4o-mini |
| Embedding | text-embedding-3-small |
| PostgreSQL | 15+ with pgvector extension |
| 数据集 | LoCoMo-10（10 样本，1,986 QA） |

### B. LoCoMo 完整类别详情（F1 / BLEU / LLM）

| 场景 | single-hop | multi-hop | temporal | open-domain | adversarial |
| --- | --- | --- | --- | --- | --- |
| Long-Context | 0.320/0.251/0.320 | 0.308/0.273/0.260 | 0.088/0.068/0.165 | 0.518/0.457/0.662 | 0.667/0.667/0.668 |
| Session Recall | 0.368/0.304/0.445 | 0.554/0.512/0.563 | 0.174/0.138/0.311 | 0.618/0.570/0.715 | 0.610/0.610/0.608 |
| 优化版 | 0.396/0.325/0.395 | 0.453/0.415/0.519 | 0.247/0.192/0.364 | 0.441/0.398/0.552 | 0.626/0.626/0.626 |
| 原版 | 0.316/0.250/0.270 | 0.096/0.088/0.060 | 0.088/0.068/0.115 | 0.358/0.319/0.425 | 0.814/0.814/0.814 |

### C. LoCoMo Token 消耗——完整数据

| 场景 | Prompt Tokens | Completion Tokens | Total Tokens | LLM 调用 | 调用/QA |
| --- | ---: | ---: | ---: | ---: | ---: |
| Long-Context | 37,272,167 | 16,104 | 37,288,271 | 1,986 | 1.0 |
| Session Recall | 7,336,165 | 16,892 | 7,353,057 | 1,986 | 1.0 |
| 优化版 | 34,007,814 | 115,960 | 34,123,774 | 5,981 | 3.0 |
| 原版 | 6,011,025 | 57,777 | 6,068,802 | 3,999 | 2.0 |
| AutoGen | 3,842,576 | 16,836 | 3,859,412 | 1,986 | 1.0 |
| CrewAI | 5,360,840 | 278,245 | 5,639,085 | 3,972 | 2.0 |
| Agno | 20,694,534 | 31,194 | 20,725,728 | 1,986 | 1.0 |
| ADK | 97,691,620 | 67,833 | 97,759,453 | 4,028 | 2.0 |

### D. LongMemEval 50-case 清单

LongMemEval 一节的全部实验共用下列 50 个 case。
选取规则是按问题类型分层，每一类从 LongMemEval-S 的 500 个 case
中等比例抽取 10%，取整后各类为 8、13、6、3、7、13，
与全集的类型占比一致（见 LongMemEval 第 1 节）。清单以 case ID
固定，没有记录随机种子，因此复现时应直接使用下表，
而不是重新抽样。Case ID 与 `longmemeval_s_cleaned.json` 一致；带
`_abs` 后缀的三个 case 是拒答题，正确行为是拒绝作答。

| # | Case ID | 问题类型 |
| ---: | --- | --- |
| 1 | `4dfccbf8` | temporal-reasoning |
| 2 | `gpt4_ec93e27f` | temporal-reasoning |
| 3 | `a1eacc2a` | knowledge-update |
| 4 | `gpt4_e072b769` | temporal-reasoning |
| 5 | `3ba21379` | knowledge-update |
| 6 | `gpt4_70e84552` | temporal-reasoning |
| 7 | `f4f1d8a4_abs` | single-session-user |
| 8 | `545bd2b5` | single-session-user |
| 9 | `59524333` | knowledge-update |
| 10 | `0977f2af` | knowledge-update |
| 11 | `60159905` | multi-session |
| 12 | `gpt4_f2262a51` | multi-session |
| 13 | `195a1a1b` | single-session-preference |
| 14 | `gpt4_fa19884c` | temporal-reasoning |
| 15 | `58ef2f1c` | single-session-user |
| 16 | `a346bb18` | multi-session |
| 17 | `ef66a6e5` | multi-session |
| 18 | `7527f7e2` | single-session-user |
| 19 | `3fdac837` | multi-session |
| 20 | `bbf86515` | temporal-reasoning |
| 21 | `f0853d11` | temporal-reasoning |
| 22 | `603deb26` | knowledge-update |
| 23 | `129d1232` | multi-session |
| 24 | `1d4e3b97` | single-session-preference |
| 25 | `gpt4_4cd9eba1` | temporal-reasoning |
| 26 | `faba32e5` | single-session-user |
| 27 | `gpt4_1e4a8aeb` | temporal-reasoning |
| 28 | `2698e78f_abs` | knowledge-update |
| 29 | `gpt4_fa19884d` | temporal-reasoning |
| 30 | `2788b940` | multi-session |
| 31 | `3e321797` | single-session-assistant |
| 32 | `0100672e` | multi-session |
| 33 | `b3c15d39` | multi-session |
| 34 | `d7c942c3` | knowledge-update |
| 35 | `4f54b7c9` | multi-session |
| 36 | `08f4fc43` | temporal-reasoning |
| 37 | `0e5e2d1a` | single-session-assistant |
| 38 | `b01defab` | knowledge-update |
| 39 | `51b23612` | single-session-assistant |
| 40 | `6456829e` | multi-session |
| 41 | `gpt4_2d58bcd6` | temporal-reasoning |
| 42 | `25e5aa4f` | single-session-user |
| 43 | `6456829e_abs` | multi-session |
| 44 | `778164c6` | single-session-assistant |
| 45 | `d851d5ba` | multi-session |
| 46 | `06f04340` | single-session-preference |
| 47 | `bcbe585f` | temporal-reasoning |
| 48 | `c960da58` | single-session-user |
| 49 | `e3fc4d6e` | single-session-assistant |
| 50 | `4baee567` | single-session-assistant |

按问题类型统计：knowledge-update 8；multi-session 13；
single-session-assistant 6；single-session-preference 3；
single-session-user 7；temporal-reasoning 13，合计 50。

---

## 参考文献

1. Maharana, A., Lee, D., Tulyakov, S., Bansal, M., Barbieri, F., and Fang, Y. "Evaluating Very Long-Term Conversational Memory of LLM Agents." arXiv:2402.17753, 2024.
2. Chhikara, P., Khant, D., Aryan, S., Singh, T., and Yadav, D. "Mem0: Building Production-Ready AI Agents with Scalable Long-Term Memory." arXiv:2504.19413, 2025.
3. Hu, C., et al. "Memory in the Age of AI Agents." arXiv:2512.13564, 2024.
4. Wu, D., Wang, H., Yu, W., Zhang, Y., Chang, K.-W., and Yu, D. "LongMemEval: Benchmarking Chat Assistants on Long-Term Interactive Memory." arXiv:2410.10813, 2024.
