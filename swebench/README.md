# SWE-Bench Verified

本目录用于落地 SWE-Bench Verified 评测。当前先推进 baseline 复现和公共底座校准，后续再接入 tRPC-Agent-Go native SWE agent。

## 当前校准结论

- 数据集：`princeton-nlp/SWE-bench_Verified`，`test` split，500 cases。
- 已观测 dataset revision：`c104f840cc67f8b6eec6f759ebc8b2693d585d4a`。
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

## Smoke 命令

以下命令用于验证 baseline 生成链路。正式实现会封装到统一 CLI 中。

```bash
export HF_HOME=/data/swebench-verified/cache/hf
export DOCKER_HOST=tcp://localhost:2375

mini-extra swebench \
  --subset verified \
  --split test \
  --filter '^astropy__astropy-12907$' \
  --workers 1 \
  --output /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907 \
  --config swebench.yaml \
  --config /data/swebench-verified/config/mini-minimax-m2.5.yaml \
  --redo-existing
```

用 official local harness 验证 smoke prediction：

```bash
python -m swebench.harness.run_evaluation \
  -d princeton-nlp/SWE-bench_Verified \
  -s test \
  -i astropy__astropy-12907 \
  -p /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/preds.json \
  --max_workers 1 \
  --cache_level instance \
  --clean false \
  --report_dir /data/swebench-verified/results/baseline-smoke/mini-batch-astropy-12907/harness-report \
  -id mini-batch-astropy-12907-smoke
```

当前已完成的 smoke 结果：

- gold patch harness smoke：`astropy__astropy-12907` resolved `1/1`。
- mini-SWE-agent batch smoke：`astropy__astropy-12907` submitted patch，local harness resolved `1/1`。

## Devcloud Docker 注意事项

当前 devcloud 上 `DOCKER_HOST=tcp://localhost:2375` 指向 Docker server `19.03.15`，API `1.40`。SWE-Bench 4.1.0 harness 在创建容器时会传 `platform`，该参数要求 Docker API `>=1.41`。

在本次临时环境中采用了一个最小兼容补丁：仅当 Docker API `<1.41` 时省略 `platform`。该补丁不改变测试语义，但必须写入 run manifest。更理想的 full run 环境是直接提供 Docker API `>=1.41` 且能正常运行 SWE-Bench instance image。

同一旧 Docker/seccomp 环境下，official local harness 创建的评测容器也需要注入上述线程和 Git 环境变量；否则 gold patch 会因 `pthread_create` / threaded lstat 权限错误被误判为 unresolved。该环境注入只规避平台限制，不改变测试列表或 patch 判定逻辑，也必须写入 run manifest。

同一环境中自启动 Docker 29.3.1 daemon 不可作为主路径：overlayfs/vfs 都受容器权限限制，分别在 layer extract 或 `unshare` 阶段失败。
