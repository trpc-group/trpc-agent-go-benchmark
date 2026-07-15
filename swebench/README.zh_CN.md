# SWE-Bench Verified

SWE-Bench Verified 使用真实 GitHub issue 评估软件工程 Agent 的修复能力。
本 benchmark 提供一条可复现路径：运行官方 500 case test split，使用官方
local harness 验证 patch，并对比 mini-SWE-agent 与 Go-native
`trpc-agent-go` 实现。

## 结果

已提交的 baseline 摘要文件为
[`results/baseline-mini-swe-agent-m2.5.json`](results/baseline-mini-swe-agent-m2.5.json)。

| 指标 | 值 |
| --- | ---: |
| Total cases | 500 |
| Resolved | 382 |
| Unresolved | 117 |
| Empty patch | 1 |
| Infra error | 0 |
| Incomplete | 0 |
| Resolved rate | 76.4% |

完整对比报告将在原生 `trpc-agent-go` run 完成后发布到
[`results/REPORT.md`](results/REPORT.md) 和
[`results/REPORT.zh_CN.md`](results/REPORT.zh_CN.md)。

## 仓库结构

```text
swebench/
  config/                  # 共享本地配置模板。
    models/                # 模型 endpoint 模板和 ignored 本地配置。
    environments/          # mini-SWE-agent 运行环境配置。
  data/                    # 固定 case list 和生成的数据集元信息。
  evaluator/               # 共享 dataset、verifier、importer 和 report CLI。
  mini-swe-agent-impl/     # mini-SWE-agent baseline adapter。
  trpc-agent-go-impl/      # Go-native SWE Agent 实现。
  results/                 # 报告和小型结构化摘要。
```

## 数据集

| 项目 | 值 |
| --- | --- |
| Dataset | `princeton-nlp/SWE-bench_Verified` |
| Split | `test` |
| Cases | 500 |
| Case list | `data/case-lists/verified-test-500.case_ids.txt` |
| Case list SHA256 | `a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9` |

`data/` 只保存轻量元信息，不应包含 gold patch、test patch、隐藏测试列表、
克隆仓库、Docker image cache 或原始数据集 dump。

## Quick Start

以下命令从 benchmark 仓库根目录执行。

### 1. 准备 evaluator 环境

使用安装了 Docker、Go 1.21+ 和 Python 3.11+ 的 Linux 机器。

```bash
python3.11 -m venv swebench/results/runs/.venv
source swebench/results/runs/.venv/bin/activate

pip install -U pip

mkdir -p swebench/results/runs/repos
git clone https://github.com/SWE-bench/SWE-bench.git swebench/results/runs/repos/SWE-bench
pip install -e swebench/results/runs/repos/SWE-bench
```

后续 quick start 命令默认继续使用这个已激活的虚拟环境。

mini-SWE-agent baseline runner 的安装和运行见
[`mini-swe-agent-impl/README.md`](mini-swe-agent-impl/README.md)。

### 2. 配置模型访问

```bash
cp swebench/config/models/glm-5.2.yaml.example swebench/config/models/glm-5.2.local.yaml
```

在本地 YAML 中填写 endpoint、API key 和必要的网关 header。本地模型配置已
被 gitignore。

### 3. 检查 evaluator 和模型访问

```bash
cd swebench

go run ./evaluator doctor \
  --run-id swebench-doctor \
  --output results/runs/doctor \
  --model-config config/models/glm-5.2.local.yaml
```

命令会在终端输出简洁的 `ok/fail` 摘要，并将完整细节写入
`results/runs/doctor/doctor.json`。

健康的 evaluator 环境下，`doctor` 应在 Python、SWE-Bench、Docker、数据集
加载、managed httpbin 和模型 smoke 检查上返回 `ok`。按
`mini-swe-agent-impl/README.md` 安装 baseline runner 后，mini-SWE-agent
检查也应返回 `ok`。

### 4. 下载数据

```bash
go run ./evaluator prepare-data --python python
```

该命令会按需下载 SWE-Bench Verified，校验它是否匹配仓库提交的 500 case
列表，并在 `data/generated/` 下写出生成的元信息文件。

### 5. 选择实现并产出 predictions

选择一个实现运行，并保留 SWE-Bench predictions 文件：

- mini-SWE-agent baseline:
  [`mini-swe-agent-impl/README.md`](mini-swe-agent-impl/README.md)
- Go-native `trpc-agent-go` agent:
  [`trpc-agent-go-impl/README.md`](trpc-agent-go-impl/README.md)

后续步骤中的 `<path-to-preds.json>` 指向该文件。

### 6. 验证 predictions

某个实现产出 SWE-Bench predictions 后，使用官方 local harness 验证：

```bash
go run ./evaluator verify \
  --run-id <run-id> \
  --target <baseline-or-native> \
  --predictions <path-to-preds.json> \
  --output results/runs/<run-id>/local-harness-report/<baseline-or-native> \
  --harness-workers 1
```

对于 subset predictions，`verify` 默认会将官方 harness 限定到 predictions
中的 instance ids。

### 7. 整理结果

```bash
go run ./evaluator import \
  --target <baseline-or-native> \
  --cases data/generated/cases.jsonl \
  --predictions <path-to-preds.json> \
  --raw-dir <path-to-raw-run-dir> \
  --harness-report <path-to-harness-report.json> \
  --output results/runs/<run-id>/imported
```

这一步会把 predictions 和 verifier 输出转换成报告使用的逐 case 统一结果。

### 8. 写入 run config

对于单个 runner manifest：

```bash
go run ./evaluator run-config \
  --run-id <run-id> \
  --target <baseline-or-native> \
  --cases-manifest data/generated/cases.manifest.json \
  --runner-manifest <path-to-runner-manifest.json> \
  --verifier-manifest results/runs/<run-id>/local-harness-report/<baseline-or-native>/verifier_manifest.json \
  --import-summary results/runs/<run-id>/imported/summary/<baseline-or-native>.json \
  --harness-report <path-to-harness-report.json> \
  --model-name <model-name> \
  --output results/runs/<run-id>/run_config.json
```

对于 sharded mini-SWE-agent full run，使用 `--shards-manifest` 替代
`--runner-manifest`。
