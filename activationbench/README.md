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
handlers, task evaluators, request-level observation, and paired report
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
  -runs 5 \
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
- final-state/final-response pass rate and score, including collateral state
  changes;
- tool recall, precision, wrong calls, invalid calls, Skill loads, and inferred
  ToolSet activations;
- initial, peak, and per-request visible-tool menu sizes.

Task success requires every target predicate, every requested read-only result,
and no state change outside the task's allowed final state. Required tool traces
remain diagnostic so an equivalent valid sequence can pass. Provider or
control-flow errors are reported as `error_runs` and counted as failed task
samples. Token deltas require complete provider usage in both arms.

The report records the effective activation lifetime, LLM/tool iteration
limits, per-arm timeout, selected task ids, streaming mode, arm order, and
capability counts. The CLI rejects Static-All and compare runs whose first
OpenAI-compatible request would exceed 128 functions; because `skill_load` is
also present, these modes allow at most 127 domain tools.

## Provider results

Both runs below used the same effective configuration: five paired
repetitions, all 18 tasks, 32 Skills, 32 ToolSets, 127 domain tools, streaming,
invocation-scoped activation, 32 maximum LLM calls, 16 maximum tool
iterations, and a 20-minute timeout per arm. Each arm scheduled 90 task
samples. The arm order alternated on every repetition.

### `gpt-4.1-mini`

The report was generated at `2026-09-29T02:18:06Z`. Static-All completed all
90 samples without a run error. Dynamic-Activation recorded one
`max tool iterations` error for `research-save-finding` in repetition 3; that
sample is counted as failed. Provider usage was complete in both arms.

| Metric | Static-All | Dynamic-Activation | Dynamic − Static |
| --- | ---: | ---: | ---: |
| Task samples / errors | 90 / 0 | 90 / 1 | — |
| Quality pass rate | 78.9% | 82.2% | +3.3 pp |
| Average score | 0.818 | 0.858 | +0.041 |
| Total tokens | 1,796,744 | 805,018 | −991,726 (−55.2%) |
| Average tokens / task | 19,964 | 8,945 | −11,019 (−55.2%) |
| Request TTFT average | 3,034.1 ms | 1,808.7 ms | −1,225.4 ms (−40.4%) |
| Task-first TTFT average | 3,239.5 ms | 1,796.6 ms | −1,442.9 ms (−44.5%) |
| Task duration average | 11,273.9 ms | 10,169.8 ms | −1,104.1 ms (−9.8%) |
| Task duration p95 | 16,978.2 ms | 15,650.4 ms | −1,327.8 ms (−7.8%) |
| Arm wall-clock time | 1,014.9 s | 915.5 s | −99.4 s (−9.8%) |
| Average visible-tool menu | 128.0 | 11.9 | −116.1 |

In this run, Dynamic-Activation used 55.2% fewer total tokens, reduced
request-average TTFT by 40.4%, and recorded an 82.2% pass rate after counting
the tool-iteration error as failed. Static-All recorded a 78.9% pass rate.

### `gpt-5.5`

The report was generated at `2026-09-28T16:43:56Z`. Both arms completed all
90 samples without errors, and every request had provider-reported usage.

| Metric | Static-All | Dynamic-Activation | Dynamic − Static |
| --- | ---: | ---: | ---: |
| Evaluated samples / errors | 90 / 0 | 90 / 0 | — |
| Quality pass rate | 97.8% | 100.0% | +2.2 pp |
| Average score | 0.994 | 1.000 | +0.006 |
| Total tokens | 2,636,823 | 836,091 | −1,800,732 (−68.3%) |
| Average tokens / task | 29,298 | 9,290 | −20,008 (−68.3%) |
| Request TTFT average | 3,327.6 ms | 2,180.2 ms | −1,147.4 ms (−34.5%) |
| Task-first TTFT average | 3,447.3 ms | 2,038.7 ms | −1,408.6 ms (−40.9%) |
| Task duration average | 17,358.5 ms | 11,843.7 ms | −5,514.7 ms (−31.8%) |
| Task duration p95 | 23,370.0 ms | 18,049.2 ms | −5,320.8 ms (−22.8%) |
| Arm wall-clock time | 1,562.5 s | 1,066.2 s | −496.3 s (−31.8%) |
| Average visible-tool menu | 128.0 | 11.3 | −116.7 |

For this complete paired run, Dynamic-Activation used 68.3% fewer total tokens,
reduced request-average TTFT by 34.5%, reduced task-first TTFT by 40.9%, and
reduced average task duration by 31.8%. Its observed quality pass rate was
100.0%, compared with 97.8% for Static-All. These measurements describe this
suite, model endpoint, and run configuration; they are not guarantees for
other models or workloads.

## Publishing results

A publishable comparison must retain the generated `report.json` and satisfy
all of these report conditions:

- `status=complete` with no top-level `run_errors`;
- complete provider usage for every request in both arms;
- equal task ids and repetition ids across paired arms;
- the same effective configuration for every reported repetition;
- enough paired repetitions for the stated statistical claim.

Task-level `error_runs` count as failed samples. Individual failed tool attempts
remain in wrong/invalid-call diagnostics. A recovered run can pass only when
its final response is complete and its final state contains no unrelated
change.

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
