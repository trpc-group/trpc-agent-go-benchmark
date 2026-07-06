# SWE-Bench Verified

本目录用于落地 SWE-Bench Verified 评测。当前先推进 baseline 复现和公共底座校准，后续再接入 tRPC-Agent-Go native SWE agent。

## 当前校准结论

- 数据集：`princeton-nlp/SWE-bench_Verified`，`test` split，500 cases。
- 已观测 dataset revision：`c104f840cc67f8b6eec6f759ebc8b2693d585d4a`。
- 当前 case list hash：`a6b0fd7c8c2969a0eef892e032250adcfa6d32362d395c246930e61b575ac9b9`。
- baseline：mini-SWE-agent `v2.0.0`。
- smoke 模型：`minimax-m2.5`，通过 OpenAI-compatible endpoint 接入。
- mini 模型名：`openai/minimax-m2.5`。
- mini 默认温度：`0.0`。
- high reasoning：当前网关接受并校验 `reasoning_effort=high`。
- agent 生成并发：默认 15；单 case smoke 实际并发为 1。
- verifier：SWE-Bench official local harness；`sb-cli` 不作为主路径。

## 本地配置

复制并填写密钥配置：

```bash
cp swebench/config/minimax-m2.5.env.example swebench/config/minimax-m2.5.env
cp swebench/config/mini-swe-agent.minimax-m2.5.yaml.example \
  swebench/config/mini-swe-agent.minimax-m2.5.local.yaml
```

`*.env` 和 `*.local.yaml` 已被 gitignore。不要提交真实 endpoint provider、API key 或 Authorization。

注意：mini-SWE-agent trajectory 会保存运行时 config。导入或归档 trajectory 前必须 scrub `api_key`、Authorization 和 provider 类 header。

## mini-SWE-agent 配置要点

SWE-Bench instance image 中的测试环境在 conda `testbed` 里。mini-SWE-agent 的 DockerEnvironment 默认直接执行 `bash -c`，会使用容器默认 Python，部分 case 会误入错误环境。

当前采用 `environment.interpreter` wrapper：

```yaml
interpreter:
  - bash
  - -lc
  - source /opt/miniconda3/bin/activate testbed && eval "$@"
  - mini-swe-agent-command
```

在当前 devcloud Docker 19.03 环境下，还需要限制线程并关闭 Git preload index，避免 seccomp 导致的 `pthread_create` / threaded lstat `Operation not permitted`：

```yaml
env:
  OPENBLAS_NUM_THREADS: "1"
  OMP_NUM_THREADS: "1"
  MKL_NUM_THREADS: "1"
  NUMEXPR_NUM_THREADS: "1"
  GIT_CONFIG_COUNT: "1"
  GIT_CONFIG_KEY_0: core.preloadindex
  GIT_CONFIG_VALUE_0: "false"
```

## Go CLI

第一版 baseline 复现编排放在：

```text
swebench/trpc-agent-go-impl/
```

当前提供六个命令：

- `doctor`：检查 Python、mini-SWE-agent、swebench、Docker、dataset 和模型 endpoint。
- `prepare-data`：生成安全 case manifest、case list hash 和 manifest 元信息。
- `run-mini`：调用 mini-SWE-agent batch runner，保存 raw predictions、trajectory 和日志。
- `verify`：调用 SWE-Bench official local harness。
- `import`：导入 baseline predictions、trajectory 和 harness report，输出统一 `cases.jsonl`、patches、scrubbed traces 和 summary。
- `run-config`：聚合 dataset、runner、verifier、import summary 等产物，写出本次 run 的总 manifest。

第一版 `import` 只支持 baseline；native agent 接入后再扩展 native 字段。

`prepare-data` 产物：

```text
swebench/data/
  cases.jsonl
  cases.sha256
  cases.manifest.json
```

`cases.jsonl` 只包含 agent 可见安全字段：`instance_id`、`repo`、`base_commit`、`problem_statement`，以及显式开启时的 `hints_text`。默认不包含 `patch`、`test_patch`、`FAIL_TO_PASS`、`PASS_TO_PASS`。

当前口径下 `hints_text` 不使用，`cases.manifest.json` 中记录为 `hints_text_policy=not-used`。

## Smoke 命令

以下命令用于验证 baseline 生成链路。

```bash
cd swebench/trpc-agent-go-impl

export HF_HOME=/data/swebench-verified/cache/hf
export DOCKER_HOST=tcp://localhost:2375

go run . doctor \
  --run-id mini-batch-astropy-12907-smoke \
  --output /data/swebench-verified/results/baseline-smoke/doctor \
  --model-config /data/swebench-verified/config/minimax-m2.5.env

go run . prepare-data \
  --output /data/swebench-verified/data \
  --python /data/swebench-verified/.venv/bin/python

go run . run-mini \
  --run-id mini-batch-astropy-12907-smoke \
  --subset verified \
  --split test \
  --filter '^astropy__astropy-12907$' \
  --agent-workers 1 \
  --output /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907 \
  --mini-config /data/swebench-verified/config/mini-minimax-m2.5.yaml \
  --redo-existing
```

用 official local harness 验证 smoke prediction：

```bash
go run . verify \
  --run-id mini-batch-astropy-12907-smoke \
  --target baseline \
  --instance astropy__astropy-12907 \
  --predictions /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/preds.json \
  --output /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/harness-report \
  --harness-workers 1 \
  --apply-harness-compat

go run . import \
  --target baseline \
  --cases /data/swebench-verified/data/cases.jsonl \
  --predictions /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/preds.json \
  --raw-dir /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907 \
  --harness-report /data/swebench-verified/openai__minimax-m2.5.mini-batch-astropy-12907-smoke.json \
  --output /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/imported

go run . run-config \
  --run-id mini-batch-astropy-12907-smoke \
  --target baseline \
  --cases-manifest /data/swebench-verified/data/cases.manifest.json \
  --run-mini-manifest /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/run-mini-manifest.json \
  --verifier-manifest /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/harness-report/verifier_manifest.json \
  --import-summary /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/imported/summary/baseline.json \
  --harness-report /data/swebench-verified/openai__minimax-m2.5.mini-batch-astropy-12907-smoke.json \
  --doctor /data/swebench-verified/results/baseline-smoke/doctor/doctor.json \
  --model-name minimax-m2.5 \
  --mini-model-name openai/minimax-m2.5 \
  --temperature 0.0 \
  --reasoning-effort high \
  --output /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/run_config.json
```

当前已完成的 smoke 结果：

- gold patch harness smoke：`astropy__astropy-12907` resolved `1/1`。
- mini-SWE-agent batch smoke：`astropy__astropy-12907` submitted patch，local harness resolved `1/1`。
- mini-SWE-agent 5-case cross-repo smoke：5/5 submitted，local harness completed 5/5，resolved 3/5，unresolved 2/5，error 0。该 run 中观测到公开模型服务 `30/min` 限流和 worker unavailable 重试，full run 前需要基于正式 endpoint 能力重新确认实际吞吐。

## Devcloud Docker 注意事项

当前 devcloud 上 `DOCKER_HOST=tcp://localhost:2375` 指向 Docker server `19.03.15`，API `1.40`。SWE-Bench 4.1.0 harness 在创建容器时会传 `platform`，该参数要求 Docker API `>=1.41`。

在本次临时环境中采用了一个最小兼容补丁：仅当 Docker API `<1.41` 时省略 `platform`。该补丁不改变测试语义，但必须写入 run manifest。更理想的 full run 环境是直接提供 Docker API `>=1.41` 且能正常运行 SWE-Bench instance image。

同一旧 Docker/seccomp 环境下，official local harness 创建的评测容器也需要注入上述线程和 Git 环境变量；否则 gold patch 会因 `pthread_create` / threaded lstat 权限错误被误判为 unresolved。该环境注入只规避平台限制，不改变测试列表或 patch 判定逻辑，也必须写入 run manifest。

同一环境中自启动 Docker 29.3.1 daemon 不可作为主路径：overlayfs/vfs 都受容器权限限制，分别在 layer extract 或 `unshare` 阶段失败。
