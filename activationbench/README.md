# ActivationBench-Lite

ActivationBench-Lite is a local benchmark for measuring the effect of tRPC
Agent's dynamic Skill-to-ToolSet activation. It compares two otherwise
identical runs:

- **Static-All**: all registered domain tools are visible from the first model
  request.
- **Dynamic-Activation**: the initial menu contains the framework's
  `skill_load` operation; loading a Skill activates its mapped ToolSet.

The benchmark uses the framework's top-level `runner.Run`, filesystem
`FSRepository`, `SessionService`, Skill activation APIs, and the framework
OpenAI-compatible model. The benchmark owns only its local task world, tool
handlers, final-state evaluators, request-level observation, and paired report
aggregation.

## Scope and safety

The checked-in baseline contains:

- 8 local Skills and 8 ToolSets;
- 64 tools;
- 18 stateful tasks across mail, calendar, documents, spreadsheets, inventory,
  CRM, files, and research.

The fixture world is process-local and task-isolated. Tool handlers do not use
HTTP, DNS, databases, browsers, shells, Docker, MCP, or external SaaS systems.
The only network activity in a real run is the explicitly configured model
endpoint.

The fixed eight Skills are stored as normal local files under [`skills/`](skills/).
When `-skills` is greater than 8, the catalog creates additional
production-shaped, read-only scale fixtures and writes their `SKILL.md` files
to a temporary local directory for that run. These extra capabilities are not
required by the 18 tasks; they exist to increase the Skill/Tool menu size. They
are ordinary, non-overlapping capability descriptions, not malformed tools or
special model instructions. The temporary directory is removed after the arm.

## Quick start

Run the local tests first:

```bash
go test ./...
go test -race ./...
go vet ./...
```

Configure a real OpenAI-compatible provider. The CLI does not contain a mock
model fallback:

```bash
export OPENAI_API_KEY="<your-key>"
export MODEL_NAME="<model-name>"
# Optional: export OPENAI_BASE_URL="https://api.example.com/v1"
```

Use a single task for a fast smoke test:

```bash
go run ./cmd/activationbench \
  -model-source openai-compatible \
  -mode compare \
  -task files-archive-meeting \
  -runs 1 \
  -skills 8 \
  -tools 64 \
  -timeout 2m \
  -output-dir /tmp/activationbench-smoke
```

For the main experiment, use the same model and task suite in both arms:

```bash
MODEL_NAME='gpt-5.5' go run ./cmd/activationbench \
  -model-source openai-compatible \
  -mode compare \
  -runs 3 \
  -skills 32 \
  -tools 127 \
  -timeout 20m \
  -output-dir /tmp/activationbench-main
```

`compare` alternates the arm order across repetitions and pairs the same task
and repetition. `-task` only shortens the task list; it does not reduce the
Skill or Tool menu. Keep streaming enabled when measuring TTFT. Use
`-request-trace <path>` only for diagnosis: it writes complete before-model
requests, including prompt and tool declarations.

The command writes `report.json` and `summary.txt` below `-output-dir`.

## Metrics and validity rules

The runner reads provider-reported `model.Response.Usage` directly. It does not
tokenize prompts or estimate missing provider usage. The report includes:

- prompt, completion, total, cached, and reasoning token usage;
- request TTFT and task-first TTFT (average, p50, p95, and max);
- task duration and total arm wall-clock time;
- final-state pass rate and score;
- tool recall, precision, wrong calls, invalid calls, Skill loads, and inferred
  ToolSet activations;
- initial, peak, and per-request visible-tool menu sizes.

The final state is the quality metric. Required tool traces are diagnostic: a
task may be completed through an equivalent valid sequence. Provider or
control-flow errors are reported separately as `error_runs`; they are not
silently counted as successful or failed task evaluations. Token and quality
deltas are marked non-comparable when usage is incomplete or an arm has errors.

## Real-provider results

The benchmark has been run with two model tiers on the full 32-Skill/127-Tool
scale. Each table reports the aggregate values emitted by the benchmark; the
two arms use the same 18-task suite.

### Weaker model: `gpt-4.1-mini`

This run used five paired repetitions (90 task samples per arm). Provider token
usage was complete in both arms. One Dynamic-Activation task ended with a
`max tool iterations` control-flow error, so its quality and token deltas are
shown as raw observations and are **not** a clean paired comparison.

| Metric | Static-All | Dynamic-Activation | Dynamic − Static |
| --- | ---: | ---: | ---: |
| Final-state pass rate (`quality_pass`) | 78.9% | 82.0% | +3.1 pp* |
| Observed pass rate | 78.9% | 81.1% | +2.2 pp* |
| Average score | 0.817 | 0.846 | +0.030* |
| Evaluated samples / errors | 90 / 0 | 89 / 1 | — |
| Total tokens | 1,772,790 | **795,818** | **−976,972*** |
| Average tokens / task | 19,698 | **8,842** | **−10,855*** |
| Request TTFT average | 3,180.3 ms | **1,970.8 ms** | **−1,209.5 ms** |
| Task-first TTFT average | 3,379.7 ms | **1,902.9 ms** | **−1,476.8 ms** |
| Task duration average | 11,791.7 ms | **11,014.9 ms** | **−776.8 ms** |
| Task duration p95 | 18,480.6 ms | **15,851.6 ms** | **−2,629.0 ms** |
| Arm wall-clock time | 1,061.5 s | **991.6 s** | **−69.9 s** |
| Average visible-tool menu | 128.0 | **12.6** | **−115.4** |

\* The report marks quality and token deltas as non-comparable because one
Dynamic-Activation sample had a control-flow error. The latency and menu-size
figures are still useful operational observations, but this run should not be
used alone to claim a statistically reliable quality improvement.

### Stronger model: `gpt-5.5`

This run used three paired repetitions (54 task samples per arm), completed all
evaluations without errors, and had complete provider-reported usage in both
arms.

| Metric | Static-All | Dynamic-Activation | Dynamic − Static |
| --- | ---: | ---: | ---: |
| Final-state pass rate | 100.0% | 100.0% | 0.0 pp |
| Average score | 1.000 | 1.000 | 0.000 |
| Total tokens | 1,578,066 | **491,504** | **−68.9%** |
| Average tokens / task | 29,223 | **9,102** | **−68.9%** |
| Request TTFT average | 4,156.5 ms | **2,622.1 ms** | **−36.9%** |
| Task-first TTFT average | 4,079.9 ms | **2,471.6 ms** | **−39.4%** |
| Task duration average | 21,416.5 ms | **13,785.9 ms** | **−35.6%** |
| Task duration p95 | 30,272.3 ms | **19,091.7 ms** | **−36.9%** |
| Arm wall-clock time | 1,156.6 s | **744.6 s** | **−35.6%** |
| Average visible-tool menu | 128.0 | **11.3** | **−116.7** |

This stronger-model run demonstrates equal task quality with substantially lower
resource use under Dynamic-Activation: total provider-reported tokens fell by
68.9%, request TTFT by 36.9%, and task wall-clock time by 35.6%. Static-All was
already at the 100% quality ceiling, so this experiment does not demonstrate a
positive quality delta; the valid claim is equal quality with lower cost and
latency.

These are empirical observations for one task suite, scale, and a small number
of repetitions. They are not guarantees for every model or menu size. At a
small 8-Skill/64-Tool menu, Skill-loading and model-retry overhead can outweigh
the savings from the smaller initial menu. Provider/control-flow error runs
(for example, repeated empty responses that hit the LLM-call guard) must not be
presented as benchmark quality results; rerun with a stable provider and require
complete usage and evaluation.

The terminal may still show individual tool execution errors such as an invalid
identifier or an incorrect file path. If the agent recovers and the final state
is correct, the run remains evaluated successfully; those attempts are retained
in wrong/invalid-call diagnostics rather than removed from the report.

## Extending the benchmark

Add fixed Skills and tools through [`catalog/`](catalog/) and tasks/evaluators
through [`tasks/`](tasks/). Skill bodies, tool descriptions, schemas, and task
prompts should use `{{tool:raw_name}}` references. The runner resolves those
references to the framework-qualified names, such as
`files-tools_files_move`, from the same `ToolSpec` metadata.

Keep added capabilities production-shaped: each Skill and ToolSet should have a
distinct responsibility, and an unrelated capability must not be made
semantically interchangeable with a task's required tool. Evaluators should
assert final state and forbidden side effects, not one exact call sequence
unless the workflow truly requires it.
