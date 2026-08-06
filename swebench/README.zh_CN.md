# SWE-Bench Verified

本目录提供在官方 SWE-Bench Verified 500 个测试实例上运行软件工程 Agent 所需的共享契约和
评测工具。

Core 层不绑定具体 Agent：它生成安全的 case manifest，接收外部 runner 的 predictions，调用
未经修改的 upstream SWE-Bench 本地 harness，并统一整理产物。后续可以在不修改 evaluator
契约的前提下增加 Agent 实现。

可选的 Mini-Go 路径是 mini-SWE-agent v2.1.0 的 source-aligned Go 移植。它复用
tRPC-Agent-Go 的公开 model 与 tool 类型，但刻意保留 mini-SWE-agent 的显式控制循环。因此
它是参考实现路径，而不是原生 `llmagent` runner。

Native TAG 路径通过 tRPC-Agent-Go 的公开生命周期（`llmagent.New`、
`runner.NewRunner` 和 `runner.Run`）执行同一组固定的模型侧协议。默认只向模型提供 `bash`
工具，仓库检索不属于这一层。精确重复的 tool-use/result warning 只作为显式开启、
默认关闭的 benchmark instrumentation 提供，不是 tRPC-Agent-Go 框架默认能力。

## 范围

当前 Core 包含：

- 固定的 500-instance case list 与 checksum；
- 排除 gold patch 和测试元数据的安全数据投影；
- prediction、runner manifest、verifier manifest 与结果契约；
- 外部 mini-SWE-agent 参考实现的适配入口；
- 共享 Docker environment 与 XML-like/JSON/text observation codec；
- 经过 golden 测试的 source-aligned Mini-Go 参考 runner；
- 只使用 upstream 公开 API 的 tRPC-Agent-Go 原生 runner；
- 显式启用的 clean-room 协议：generation 断网、递归 Git 净化、closed-world 离线资产、
  不可变本地镜像身份与 model-free preflight；
- 可选的精确 tool-loop warning telemetry，以及对当前 Native 与冻结 V12 TAG 的
  warning-off 不可变轨迹复用同一 detector 的离线 shadow replay 工具；
- 对未经修改的 upstream official local harness 的调用；
- batch 规划、可恢复 shard 检查和确定性 predictions 合并。

运行时 workspace、模型凭据、原始数据集、predictions、traces、patches 和 harness logs 均不
提交到 Git。

## 目录结构

```text
swebench/
  config/                 # 不包含真实凭据的模型与环境模板。
  data/                   # 固定 case list 和生成的安全元数据。
  evaluator/              # 数据准备、验证和导入 CLI。
  internal/               # artifact、contract、environment 与 codec 包。
  mini-swe-agent-impl/    # 外部参考 runner 说明。
  mini-swe-agent-go-impl/ # source-aligned Mini-Go 参考 runner。
  trpc-agent-go-impl/     # tRPC-Agent-Go 原生 runner。
  results/                # 被忽略的运行产物与后续结果摘要。
```

## 数据契约

| 项目 | 值 |
| --- | --- |
| Dataset | `princeton-nlp/SWE-bench_Verified` |
| Split | `test` |
| Cases | 500 |
| Case list | `data/case-lists/verified-test-500.case_ids.txt` |
| Case-list SHA-256 | `a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9` |

提供给 runner 的数据仅包含 `instance_id`、`repo`、`base_commit`、
`problem_statement`，以及可选的 `hints_text`；绝不包含 `patch`、`test_patch`、
`FAIL_TO_PASS` 或 `PASS_TO_PASS`。

## 快速开始

从 benchmark 仓库根目录执行命令。建议使用具备 Docker、Go 1.21+ 和 Python 3.11+ 的 Linux
主机。

### 1. 安装官方 harness

在隔离的 Python 环境中安装经过确认的 SWE-Bench revision，并在每次运行中记录实际 package
version 和 Git revision。

```bash
python3.11 -m venv swebench/results/runs/.venv
source swebench/results/runs/.venv/bin/activate
pip install -U pip

mkdir -p swebench/results/runs/repos
git clone https://github.com/SWE-bench/SWE-bench.git \
  swebench/results/runs/repos/SWE-bench
pip install -e swebench/results/runs/repos/SWE-bench
```

evaluator 直接调用这个 upstream package，不修改其 Python 源码。

### 2. 配置 OpenAI-compatible 模型

```bash
cp swebench/config/models/openai-compatible.yaml.example \
  swebench/config/models/openai-compatible.local.yaml
```

在本地文件中填写 endpoint、model、credential 和可选 headers。`*.local.yaml` 已被 Git
忽略。

### 3. 检查环境

```bash
cd swebench

go run ./evaluator doctor \
  --run-id swebench-doctor \
  --output results/runs/doctor \
  --model-config config/models/openai-compatible.local.yaml
```

### 4. 生成安全 case manifest

```bash
go run ./evaluator prepare-data --python python
```

当使用固定 dataset 与 split 时，生成结果必须与提交的 case list 和 checksum 完全匹配，否则
命令失败。

如果 case manifest 生成于 cases 内容 checksum 引入之前，请重新执行 `prepare-data`。
`run-config` 会拒绝这类旧 manifest，避免接受无法验证的 case 内容。

### 5. 生成并验证 predictions

可以选择
[`外部 mini-SWE-agent runner`](mini-swe-agent-impl/README.md)、
[`source-aligned Mini-Go runner`](mini-swe-agent-go-impl/README.md)、
[`tRPC-Agent-Go 原生 runner`](trpc-agent-go-impl/README.md)，或提供任何符合共享契约的其他
predictions 文件。

```bash
go run ./evaluator verify \
  --run-id <run-id> \
  --target <target-label> \
  --predictions <path-to-preds.json> \
  --output results/runs/<run-id>/local-harness-report/<target-label> \
  --harness-workers 1 \
  --instance-timeout-seconds 1800
```

默认只验证 predictions 中出现的 instance。verifier manifest 会记录实际 SWE-Bench
version、可发现的 Git revision、package path、完整命令、运行配置，以及官方 report 的
harness run ID、绝对路径和 SHA-256。对于文件形式的 predictions，`verify` 会原子快照
harness 实际使用的输入并记录其 SHA-256；Native finalization 强制核对 runner predictions、
快照、digest 与 harness `-p` 参数。即使 harness 命令成功，只要无法唯一定位该 report，
`verify` 仍会失败。

`<target-label>` 是与 Agent 实现解耦的小写 slug，例如 `baseline`、`mini-go` 或
`tag`。prediction reader 支持 SWE-Bench map JSON、array JSON 与 JSONL；空输入、重复 ID、
不安全 ID，以及 map key 与内部 `instance_id` 不一致都会直接失败。

### 6. 统一整理产物

```bash
go run ./evaluator import \
  --target <target-label> \
  --cases data/generated/cases.jsonl \
  --predictions <path-to-preds.json> \
  --harness-report <verifier_manifest.report.path> \
  --output results/runs/<run-id>/imported
```

generation、verification 和 import 都生成 manifest 后，再使用 `run-config` 汇总。完整 CLI
见 [`evaluator/README.md`](evaluator/README.md)。

import 会为每个输入 case 写入与 target 无关、带版本号的结构；提供 `--cases` 时输入是完整固定
面板，否则输入是 predictions 中的实际选择集：

```json
{
  "schema_version": 1,
  "instance_id": "example__repo-123",
  "target": "tag",
  "result": {
    "main_status": "resolved",
    "patch_stats": {},
    "usage": {}
  }
}
```

对于经过 filter 或 slice 的运行，import 时省略 `--cases`，由 predictions 定义本次需要
归一化的 case。`run-config` 仍接收完整的 `cases.manifest.json`：`dataset` 保留完整面板身份，
`selection` 单独记录实际运行的 case。它只接受一个 runner provenance 来源，并强制核对 run
ID、target、dataset、selection、summary counts、predictions 路径与 harness report；任一项
不一致都会失败。`import` 与 `run-config` 必须使用 `verify` 记录的同一份 report；Native
finalization 强制要求 verify 时记录的 path 与 SHA-256 见证。

## 验证

```bash
cd swebench
go test ./...
go test -race ./...
go vet ./...
```

official harness smoke 需要 Linux、Docker、已安装的 Python package 和至少一个 instance 的
prediction，不属于 unit test。
