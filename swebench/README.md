# SWE-Bench-Verified Benchmark for trpc-agent-go

This benchmark evaluates whether `trpc-agent-go` can build a Go-native
software engineering agent for SWE-Bench-Verified.

The first tracked setup compares:

- Baseline: `mini-SWE-agent + GLM-5`
- Native: `trpc-agent-go SWE Agent + GLM-5`
- Verifier: official SWE-Bench local harness
- Dataset: SWE-Bench-Verified 500 instances

GLM-5 public reference results are cited from the official SWE-Bench
leaderboard: <https://www.swebench.com/index.html>. The core comparison
in this benchmark is still based on our own rerun of both baseline and
native runners under the same model service and verifier.

`sb-cli` remains a useful optional cross-check, but the hosted service has
shown periods where accepted submissions return zero completed rows. The
benchmark therefore treats the official local harness as the primary verifier.

## Layout

```text
swebench/
|-- README.md
|-- data/
|   `-- README.md
|-- results/
|   |-- README.md
|   |-- REPORT.md
|   `-- REPORT.zh_CN.md
`-- trpc-agent-go-impl/
    |-- go.mod
    |-- main.go
    |-- configs/
    |-- dataset/
    |-- prediction/
    |-- miniswe/
    |-- native/
    |-- verifier/
    |-- result/
    `-- report/
```

## Quick Start

### 1. Export a small dataset

```bash
cd swebench/data
python3 -m pip install datasets
python3 export_verified_jsonl.py \
  --limit 3 \
  --output swebench_verified_cases.jsonl
```

### 2. Configure GLM-5

```bash
export GLM5_API_BASE="http://<host>/gateway/v1"
export GLM5_API_KEY="<token>"
export GLM5_ROUTING_KEY="<routing-key>"
export GLM5_AGENT_NAME="trpc-agent-go-benchmark"
export GLM5_MODEL="glm50"
```

### 3. Run a native smoke demo

```bash
cd ../trpc-agent-go-impl

go run . \
  -mode doctor \
  -dataset ../data/swebench_verified_cases.jsonl

go run . \
  -mode run-native \
  -run-id glm5-native-smoke \
  -dataset ../data/swebench_verified_cases.jsonl \
  -instances all \
  -max-instances 3 \
  -model glm5 \
  -native-env docker-testbed \
  -docker-host tcp://localhost:2375 \
  -step-limit 20 \
  -timeout-minutes 30 \
  -output ../results/runs/native
```

The native runner defaults to `-native-env docker-testbed`, which executes
commands inside the official SWE-Bench instance image under `/testbed`. Use
`-native-env local-clone` only for local plumbing/debug runs that do not need the
SWE-Bench dependency environment.

For a plumbing-only run without model calls or repository cloning, add
`-dry-run`.

### 4. Prepare a local verifier node

Run the official SWE-Bench harness in an environment where Docker works. The
minimum smoke check is:

```bash
docker run --rm hello-world
```

Install the official harness in an isolated Python environment:

```bash
mkdir -p /data/swebench-local
python3 -m venv /data/swebench-local/.venv
/data/swebench-local/.venv/bin/python -m pip install --upgrade pip setuptools wheel
/data/swebench-local/.venv/bin/python -m pip install swebench
```

On some verifier nodes, Docker is exposed through TCP rather than
`/var/run/docker.sock`. In that case export `DOCKER_HOST` or pass
`-docker-host` to the Go wrapper:

```bash
export DOCKER_HOST=tcp://localhost:2375
```

### 5. Verify with the local harness

The Go wrapper executes a harness command template and parses the produced
official report:

```bash
cd swebench/trpc-agent-go-impl

go run . \
  -mode verify \
  -verifier local-harness \
  -run-id glm5-native-smoke-local-verify \
  -predictions ../results/runs/native/glm5-native-smoke/predictions.jsonl \
  -model glm5 \
  -docker-host tcp://localhost:2375 \
  -local-report ../results/runs/verifier/glm5-native-smoke-local-verify/report.json \
  -local-command 'python -m swebench.harness.run_evaluation --dataset_name princeton-nlp/SWE-bench_Verified --split test --predictions_path {predictions} --run_id {run_id} --max_workers 1 --report_dir {output_dir} && cp {output_dir}/*report*.json {report}' \
  -output ../results/runs/verifier
```

The command is intentionally a template because official harness flags can
change. The wrapper expands `{predictions}`, `{run_id}`, `{output_dir}`, and
`{report}`. The command must leave a JSON report at `{report}`; the wrapper
then parses it into normalized `cases.jsonl` and `summary.json`.

For remote verifier nodes, copy `predictions.jsonl` to the verifier machine,
run `python -m swebench.harness.run_evaluation` there, copy the report back,
and then import it with the same wrapper by setting `-local-command true` and
`-local-report <report.json>`.

Example report import:

```bash
go run . \
  -mode verify \
  -verifier local-harness \
  -run-id glm5-native-smoke-local-import \
  -predictions ../results/runs/native/glm5-native-smoke/predictions.jsonl \
  -model glm5 \
  -local-command true \
  -local-report ../results/runs/verifier/raw-harness-report.json \
  -output ../results/runs/verifier
```

Runs with at most 10 cases may be executed serially without extra
confirmation. Runs above 10 cases require confirming the concurrency plan
first; the current approved upper bound is 5, and serial execution is
preferred when stability matters.
