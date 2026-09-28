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
- final-state/final-response pass rate and score, including collateral state
  changes;
- tool recall, precision, wrong calls, invalid calls, Skill loads, and inferred
  ToolSet activations;
- initial, peak, and per-request visible-tool menu sizes.

Task success requires every target predicate, every requested read-only result,
and no state change outside the task's allowed final state. Required tool traces
remain diagnostic so an equivalent valid sequence can pass. Provider or
control-flow errors are reported separately as `error_runs`; token and quality
deltas are marked non-comparable when usage is incomplete or an arm has errors.

The report records the effective activation lifetime, LLM/tool iteration
limits, per-arm timeout, selected task ids, streaming mode, arm order, and
capability counts. The CLI rejects Static-All and compare runs whose first
OpenAI-compatible request would exceed 128 functions; because `skill_load` is
also present, these modes allow at most 127 domain tools.

## Publishing results

A publishable comparison must retain the generated `report.json` and satisfy
all of these report conditions:

- `status=complete` with no `run_errors` or `error_runs`;
- complete provider usage for every request in both arms;
- equal task ids and repetition ids across paired arms;
- the same effective configuration for every reported repetition;
- enough paired repetitions for the stated statistical claim.

Individual failed tool attempts remain in wrong/invalid-call diagnostics. A
recovered run can pass only when its final response is complete and its final
state contains no unrelated change.

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
