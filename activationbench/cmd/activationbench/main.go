//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Command activationbench runs the local, no-container ActivationBench-Lite
// suite against an explicitly configured OpenAI-compatible provider. The
// fixture tools are local and safe; only the model adapter may access a network
// endpoint. Library callers can supply another real provider through
// runner.Config.ModelFactory.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/metrics"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/runner"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/tasks"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
)

type report struct {
	Benchmark string `json:"benchmark"`
	Mode      string `json:"mode"`
	// Status describes the harness lifecycle, not task quality. A complete
	// report ran every requested arm/repetition; an incomplete report retains
	// partial results and RunErrors when cancellation, timeout, or pairing
	// validation stopped the experiment.
	Status    string   `json:"status"`
	RunErrors []string `json:"run_errors,omitempty"`
	// TokenSource makes it explicit that report values came from provider usage;
	// the CLI only runs with a provider model and reads model.Response.Usage.
	TokenSource   string `json:"token_source"`
	SkillSource   string `json:"skill_source"`
	ModelSource   string `json:"model_source"`
	QualitySource string `json:"quality_source"`
	// QualityMeasured indicates that every task has a final-state evaluator and
	// the reported pass rate is an empirical provider run.
	QualityMeasured bool `json:"quality_measured"`
	// Streaming is the effective request mode used by the runner. The default
	// is true so real-provider TTFT samples represent an adapter-visible first
	// response rather than a full non-streaming completion.
	Streaming           bool      `json:"streaming"`
	Runs                int       `json:"runs"`
	Skills              int       `json:"skills"`
	ToolSets            int       `json:"tool_sets"`
	Tools               int       `json:"tools"`
	Distractors         int       `json:"distractor_tools"`
	Tasks               int       `json:"tasks"`
	GeneratedAt         time.Time `json:"generated_at"`
	ElapsedNanos        int64     `json:"elapsed_nanos,omitempty"`
	StaticElapsedNanos  int64     `json:"static_elapsed_nanos,omitempty"`
	DynamicElapsedNanos int64     `json:"dynamic_elapsed_nanos,omitempty"`
	// ArmOrder records the actual order in which each experiment arm ran.
	// Compare mode alternates the order across repetitions to reduce warm-up
	// and host-cache bias in latency measurements.
	ArmOrder   []string            `json:"arm_order,omitempty"`
	Comparison *metrics.Comparison `json:"comparison,omitempty"`
	Aggregate  *metrics.Aggregate  `json:"aggregate,omitempty"`
	// Partial compare runs cannot produce a paired Comparison safely. Keep each
	// available arm's aggregate instead of silently dropping its observations.
	StaticAggregate  *metrics.Aggregate  `json:"static_aggregate,omitempty"`
	DynamicAggregate *metrics.Aggregate  `json:"dynamic_aggregate,omitempty"`
	Results          []metrics.RunResult `json:"results"`
}

type modelSelection struct {
	factory         runner.ModelFactory
	modelSource     string
	tokenSource     string
	qualitySource   string
	qualityMeasured bool
}

func main() {
	modeFlag := flag.String("mode", "compare", "run mode: compare | static | dynamic | dynamic-session")
	taskFlag := flag.String("task", "", "run only one task by id (default: all suite tasks)")
	runsFlag := flag.Int("runs", 1, "number of repetitions (paired in compare mode; >= 1)")
	lifetimeFlag := flag.String("lifetime", "invocation", "dynamic activation lifetime: invocation | session")
	outputFlag := flag.String("output-dir", "./results", "directory for JSON and text summaries")
	maxLLMFlag := flag.Int("max-llm-calls", 32, "maximum LLM calls per task (0 disables the limit)")
	maxToolFlag := flag.Int("max-tool-iterations", 16, "maximum tool iterations per task (0 disables the limit)")
	requestTraceFlag := flag.String("request-trace", "", "write before-model request contexts as JSONL to this path (diagnostic; disabled by default)")
	skillsFlag := flag.Int("skills", 8, "number of catalog Skills (>=8; added Skills are local distractors)")
	toolsFlag := flag.Int("tools", 64, "number of catalog tools (>=64; added tools are local distractors)")
	skillRootFlag := flag.String("skill-root", "", "caller-owned local Skill root; default uses ./skills for the fixed 8-Skill suite")
	noStreamFlag := flag.Bool("no-stream", false, "disable framework streaming (TTFT then measures full-response latency)")
	modelSourceFlag := flag.String("model-source", "openai-compatible", "model source: openai-compatible (real network)")
	modelFlag := flag.String("model", "", "provider model name (or MODEL_NAME when using openai-compatible)")
	baseURLFlag := flag.String("base-url", "", "provider base URL (or OPENAI_BASE_URL when using openai-compatible)")
	apiKeyEnvFlag := flag.String("api-key-env", defaultAPIKeyEnv, "environment variable containing the provider API key")
	timeoutFlag := flag.Duration("timeout", 10*time.Minute, "timeout for each benchmark arm; 0 disables the timeout")
	flag.Parse()

	if *runsFlag < 1 {
		fatalf("-runs must be >= 1")
	}
	if *toolsFlag < 64 {
		fatalf("-tools must be >= 64")
	}
	if *skillsFlag < 8 {
		fatalf("-skills must be >= 8")
	}
	if *timeoutFlag < 0 {
		fatalf("-timeout must be non-negative")
	}
	if *toolsFlag < *skillsFlag {
		fatalf("-tools (%d) must be at least -skills (%d)", *toolsFlag, *skillsFlag)
	}
	lifetime, err := parseLifetime(*lifetimeFlag)
	if err != nil {
		fatalf("%v", err)
	}
	mode, compare, err := parseMode(*modeFlag)
	if err != nil {
		fatalf("%v", err)
	}
	// `dynamic-session` is an explicit convenience alias. It must not silently
	// run with invocation-scoped activation when the caller leaves the default
	// lifetime flag unchanged.
	if strings.EqualFold(strings.TrimSpace(*modeFlag), "dynamic-session") {
		lifetime = llmagent.ToolActivationLifetimeSession
	}
	suite, err := tasks.ScaledSuiteWithSkills(*skillsFlag, *toolsFlag)
	if err != nil {
		fatalf("build default suite: %v", err)
	}
	if selectedTask := strings.TrimSpace(*taskFlag); selectedTask != "" {
		suite, err = suiteWithTask(suite, selectedTask)
		if err != nil {
			fatalf("select task: %v", err)
		}
	}
	skillRoot := strings.TrimSpace(*skillRootFlag)
	// The checked-in local Skill catalog is the default for the fixed baseline.
	// Scaled suites add generated Skills, so they use one generated filesystem
	// catalog for the arm unless the caller supplies a matching root explicitly.
	if skillRoot == "" && *skillsFlag == 8 {
		skillRoot = "skills"
	}
	selection, err := selectModel(*modelSourceFlag, *modelFlag, *baseURLFlag, *apiKeyEnvFlag, suite)
	if err != nil {
		fatalf("configure model: %v", err)
	}
	traceWriter, err := newRequestTraceWriter(*requestTraceFlag)
	if err != nil {
		fatalf("open request trace: %v", err)
	}
	if traceWriter != nil {
		defer func() { _ = traceWriter.Close() }()
	}
	var requestObserver runner.ModelRequestObserver
	if traceWriter != nil {
		requestObserver = traceWriter.Observe
	}
	r, err := runner.New(runner.Config{
		Suite:                suite,
		SkillRoot:            skillRoot,
		ModelFactory:         selection.factory,
		Lifetime:             lifetime,
		MaxLLMCalls:          *maxLLMFlag,
		MaxToolIterations:    *maxToolFlag,
		DisableStreaming:     *noStreamFlag,
		ModelRequestObserver: requestObserver,
	})
	if err != nil {
		fatalf("build runner: %v", err)
	}

	ctx := context.Background()
	runStarted := time.Now()
	var staticElapsed, dynamicElapsed time.Duration
	staticResults := make([]metrics.RunResult, 0, *runsFlag*len(suite.Tasks))
	dynamicResults := make([]metrics.RunResult, 0, *runsFlag*len(suite.Tasks))
	armOrder := make([]string, 0, 2*(*runsFlag))
	var runErrors []string
	runArm := func(runMode bench.Mode, repetition int) error {
		started := time.Now()
		armCtx := ctx
		cancel := func() {}
		if *timeoutFlag > 0 {
			armCtx, cancel = context.WithTimeout(ctx, *timeoutFlag)
		}
		results, runErr := r.RunMode(armCtx, runMode)
		cancel()
		elapsed := time.Since(started)
		results = metrics.AnnotateRepetition(results, repetition)
		armOrder = append(armOrder, string(runMode))
		if runMode == bench.ModeStaticAll {
			staticElapsed += elapsed
			staticResults = append(staticResults, results...)
		} else {
			dynamicElapsed += elapsed
			dynamicResults = append(dynamicResults, results...)
		}
		return runErr
	}
	stop := false
	for repetition := 0; repetition < *runsFlag; repetition++ {
		armModes := []bench.Mode{mode}
		if compare {
			// Alternate the arm order for paired repetitions. This does not
			// change the task pairing, but makes wall-clock/TTFT comparisons
			// less sensitive to one arm always being cold or warm.
			if repetition%2 == 0 {
				armModes = []bench.Mode{bench.ModeStaticAll, bench.ModeDynamicActivation}
			} else {
				armModes = []bench.Mode{bench.ModeDynamicActivation, bench.ModeStaticAll}
			}
		}
		for _, armMode := range armModes {
			if err := runArm(armMode, repetition); err != nil {
				runErrors = append(runErrors, fmt.Sprintf("%s repetition %d: %v", armMode, repetition, err))
				stop = true
				break
			}
		}
		if stop {
			break
		}
	}

	modeName := strings.ToLower(strings.TrimSpace(*modeFlag))
	output := report{
		Benchmark:           suite.Name,
		Mode:                modeName,
		Status:              "complete",
		TokenSource:         selection.tokenSource,
		SkillSource:         "generated-temp-files",
		ModelSource:         selection.modelSource,
		QualitySource:       selection.qualitySource,
		QualityMeasured:     selection.qualityMeasured,
		Streaming:           !*noStreamFlag,
		Runs:                *runsFlag,
		Skills:              len(suite.Skills),
		ToolSets:            suiteToolSetCount(suite),
		Tools:               len(suite.Tools),
		Distractors:         suiteDistractorCount(suite),
		Tasks:               len(suite.Tasks),
		GeneratedAt:         time.Now().UTC(),
		ElapsedNanos:        runElapsed(runStarted),
		StaticElapsedNanos:  staticElapsed.Nanoseconds(),
		DynamicElapsedNanos: dynamicElapsed.Nanoseconds(),
		ArmOrder:            armOrder,
	}
	if skillRoot != "" {
		output.SkillSource = "local-files"
	}
	if compare && len(runErrors) == 0 && len(staticResults) > 0 && len(dynamicResults) > 0 {
		comparison, err := metrics.NewCheckedComparison(suite.Name, staticResults, dynamicResults)
		if err != nil {
			runErrors = append(runErrors, fmt.Sprintf("pair benchmark results: %v", err))
		} else {
			output.Comparison = &comparison
		}
	} else if compare && len(runErrors) == 0 {
		runErrors = append(runErrors, fmt.Sprintf("compare produced incomplete arms: static=%d dynamic=%d", len(staticResults), len(dynamicResults)))
	}
	if output.Comparison == nil {
		if compare {
			if len(staticResults) > 0 {
				aggregate := metrics.NewAggregate(string(bench.ModeStaticAll), staticResults)
				output.StaticAggregate = &aggregate
			}
			if len(dynamicResults) > 0 {
				aggregate := metrics.NewAggregate(string(bench.ModeDynamicActivation), dynamicResults)
				output.DynamicAggregate = &aggregate
			}
			output.Results = append(append([]metrics.RunResult(nil), staticResults...), dynamicResults...)
		} else if mode == bench.ModeStaticAll {
			aggregate := metrics.NewAggregate(string(bench.ModeStaticAll), staticResults)
			output.Aggregate = &aggregate
			output.Results = staticResults
		} else {
			aggregate := metrics.NewAggregate(string(bench.ModeDynamicActivation), dynamicResults)
			output.Aggregate = &aggregate
			output.Results = dynamicResults
		}
	}
	if output.Results == nil {
		output.Results = append(staticResults, dynamicResults...)
	}
	if len(runErrors) > 0 {
		output.Status = "incomplete"
		output.RunErrors = append([]string(nil), runErrors...)
	}
	if traceWriter != nil {
		if err := traceWriter.Close(); err != nil {
			fatalf("close request trace: %v", err)
		}
	}
	if err := writeReport(*outputFlag, output); err != nil {
		fatalf("write report: %v", err)
	}
	printSummary(output)
	if len(output.RunErrors) > 0 {
		fatalf("benchmark incomplete; partial report was written: %s", strings.Join(output.RunErrors, "; "))
	}
}

// suiteWithTask returns a copy of suite containing only the requested task.
// Skills and tools remain unchanged so a single-task run exercises the same
// capability menu as the corresponding full-suite run and only shortens the
// task loop. An empty task id is intentionally handled by the caller so this
// helper has one unambiguous selection contract.
func suiteWithTask(suite bench.Suite, taskID string) (bench.Suite, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return bench.Suite{}, fmt.Errorf("task id must not be blank")
	}
	for _, task := range suite.Tasks {
		if strings.TrimSpace(task.ID) != taskID {
			continue
		}
		suite.Tasks = []bench.Task{task}
		return suite, nil
	}
	return bench.Suite{}, fmt.Errorf("unknown task %q", taskID)
}

func selectModel(source, modelName, baseURL, apiKeyEnv string, suite bench.Suite) (modelSelection, error) {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "openai", "openai-compatible":
		if !suiteHasEvaluators(suite) {
			return modelSelection{}, fmt.Errorf("real provider mode requires a final-state evaluator for every task")
		}
		config, err := openAIConfigFromEnv(modelName, baseURL, apiKeyEnv)
		if err != nil {
			return modelSelection{}, err
		}
		if err := validateOpenAISuite(suite); err != nil {
			return modelSelection{}, err
		}
		return modelSelection{
			factory:         config.Factory(),
			modelSource:     "openai-compatible:" + config.model,
			tokenSource:     "provider",
			qualitySource:   "final-state-evaluator",
			qualityMeasured: true,
		}, nil
	default:
		return modelSelection{}, fmt.Errorf("unsupported -model-source %q (want openai-compatible)", source)
	}
}

func suiteHasEvaluators(suite bench.Suite) bool {
	for _, task := range suite.Tasks {
		if task.Evaluate == nil {
			return false
		}
	}
	return true
}

func parseMode(value string) (bench.Mode, bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "static", "static-all":
		return bench.ModeStaticAll, false, nil
	case "dynamic", "dynamic-activation", "dynamic-invocation", "dynamic-session":
		return bench.ModeDynamicActivation, false, nil
	case "compare", "comparison", "":
		return "", true, nil
	default:
		return "", false, fmt.Errorf("unsupported -mode %q", value)
	}
}

func parseLifetime(value string) (llmagent.ToolActivationLifetime, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "invocation":
		return llmagent.ToolActivationLifetimeInvocation, nil
	case "session":
		return llmagent.ToolActivationLifetimeSession, nil
	default:
		return "", fmt.Errorf("unsupported -lifetime %q", value)
	}
}

func writeReport(dir string, value report) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return fmt.Errorf("-output-dir must not be empty")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "report.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "summary.txt"), []byte(summaryText(value)), 0o644)
}

func printSummary(value report) {
	fmt.Print(summaryText(value))
}

func summaryText(value report) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "ActivationBench-Lite\nbenchmark=%s mode=%s status=%s runs=%d skills=%d tool_sets=%d tools=%d distractors=%d tasks=%d model_source=%s token_source=%s skill_source=%s quality_source=%s quality_measured=%t streaming=%t arm_order=%s\n", value.Benchmark, value.Mode, value.Status, value.Runs, value.Skills, value.ToolSets, value.Tools, value.Distractors, value.Tasks, value.ModelSource, value.TokenSource, value.SkillSource, value.QualitySource, value.QualityMeasured, value.Streaming, strings.Join(value.ArmOrder, ","))
	fmt.Fprintf(&builder, "wall_total_ms=%.1f wall_static_ms=%.1f wall_dynamic_ms=%.1f\n", durationMillis(value.ElapsedNanos), durationMillis(value.StaticElapsedNanos), durationMillis(value.DynamicElapsedNanos))
	if len(value.RunErrors) > 0 {
		fmt.Fprintf(&builder, "run_errors=%s\n", strings.Join(value.RunErrors, " | "))
	}
	if value.Comparison != nil {
		comparison := value.Comparison
		writeAggregateSummary(&builder, "static", comparison.Static)
		writeAggregateSummary(&builder, "dynamic", comparison.Dynamic)
		fmt.Fprintf(&builder, "delta:    token_total_abs=%.0f token_rel=%.3f token_saving=%s token_delta_comparable=%t token_avg_abs=%.0f pass_abs=%.3f quality_delta_comparable=%t score_abs=%.3f recall_abs=%.3f precision_abs=%.3f wrong_abs=%.3f invalid_abs=%.3f\n",
			comparison.TokenDelta.Absolute, comparison.TokenDelta.Relative,
			tokenSavingText(*comparison), comparison.TokenDeltaComparable, comparison.AverageTokenDelta.Absolute,
			comparison.PassRateDelta.Absolute, comparison.QualityDeltaComparable, comparison.ScoreDelta.Absolute,
			comparison.ToolRecallDelta.Absolute, comparison.ToolPrecisionDelta.Absolute,
			comparison.WrongToolCallDelta.Absolute, comparison.InvalidToolCallDelta.Absolute)
		fmt.Fprintf(&builder, "latency_delta: ttft_request_avg_abs_ms=%.3f ttft_task_first_avg_abs_ms=%.3f ttft_task_first_p95_abs_ms=%.3f task_total_abs_ms=%.3f task_avg_abs_ms=%.3f task_p95_abs_ms=%.3f wall_arm_abs_ms=%.3f\n",
			signedDurationMillis(comparison.Dynamic.AverageTTFTNanos-comparison.Static.AverageTTFTNanos),
			signedDurationMillis(comparison.Dynamic.AverageTaskFirstTTFTNanos-comparison.Static.AverageTaskFirstTTFTNanos),
			signedDurationMillis(float64(comparison.Dynamic.P95TaskFirstTTFTNanos-comparison.Static.P95TaskFirstTTFTNanos)),
			signedDurationMillis(float64(comparison.Dynamic.TotalDurationNanos-comparison.Static.TotalDurationNanos)),
			signedDurationMillis(comparison.Dynamic.AverageDurationNanos-comparison.Static.AverageDurationNanos),
			signedDurationMillis(float64(comparison.Dynamic.P95DurationNanos-comparison.Static.P95DurationNanos)),
			signedDurationMillis(float64(value.DynamicElapsedNanos-value.StaticElapsedNanos)))
	} else if value.Aggregate != nil {
		writeAggregateSummary(&builder, "aggregate", *value.Aggregate)
	} else {
		if value.StaticAggregate != nil {
			writeAggregateSummary(&builder, "static_partial", *value.StaticAggregate)
		}
		if value.DynamicAggregate != nil {
			writeAggregateSummary(&builder, "dynamic_partial", *value.DynamicAggregate)
		}
	}
	return builder.String()
}

func runElapsed(start time.Time) int64 {
	if start.IsZero() {
		return 0
	}
	return time.Since(start).Nanoseconds()
}

func durationMillis(nanos int64) float64 {
	if nanos <= 0 {
		return 0
	}
	return float64(nanos) / float64(time.Millisecond)
}

func durationMillisFloat(nanos float64) float64 {
	if nanos <= 0 {
		return 0
	}
	return nanos / float64(time.Millisecond)
}

func signedDurationMillis(nanos float64) float64 {
	return nanos / float64(time.Millisecond)
}

func suiteToolSetCount(suite bench.Suite) int {
	seen := make(map[string]struct{})
	for _, spec := range suite.Tools {
		name := strings.TrimSpace(spec.ToolSet)
		if name == "" && strings.TrimSpace(spec.Skill) != "" {
			name = bench.ToolSetName(spec.Skill)
		}
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	return len(seen)
}

func suiteDistractorCount(suite bench.Suite) int {
	count := 0
	for _, spec := range suite.Tools {
		if spec.Distractor {
			count++
		}
	}
	return count
}

func writeAggregateSummary(builder *strings.Builder, label string, aggregate metrics.Aggregate) {
	fmt.Fprintf(builder, "%s: quality_pass=%.3f observed_pass=%.3f evaluated=%d/%d errors=%d score=%.3f tokens=%d avg_tokens=%.3f tokens_per_success=%.0f p95=%d max_prompt=%d menu=%.1f(initial=%.1f,max=%d) wrong=%.1f invalid=%.1f skill_load=%.1f activations=%.1f usage_source=reported:%d,synthetic:%d,estimated:%d,missing:%d usage_coverage=%d/%d(%.3f) usage_complete=%t\n",
		label, aggregate.PassRate, aggregate.ObservedPassRate, aggregate.EvaluatedRuns, aggregate.Runs, aggregate.ErrorRuns, aggregate.AverageScore, aggregate.Usage.TotalTokens,
		aggregate.AverageTotalTokens, aggregate.TokensPerSuccess, aggregate.P95TotalTokens, aggregate.MaxRequestPromptTokens,
		aggregate.AveragePeakVisibleTools, aggregate.AverageInitialTools, aggregate.MaxRequestVisibleTools,
		aggregate.AverageWrongToolCalls, aggregate.AverageInvalidToolCalls,
		aggregate.AverageSkillLoads, aggregate.AverageActivations,
		aggregate.UsageReportedRequests, aggregate.UsageSyntheticRequests,
		aggregate.UsageEstimatedRequests, aggregate.UsageMissingRequests,
		aggregate.UsageKnownSamples, aggregate.UsageSamples, aggregate.UsageCoverage, aggregate.UsageComplete)
	fmt.Fprintf(builder, "%s_latency: ttft_request_avg_ms=%.3f ttft_request_p50_ms=%.3f ttft_request_p95_ms=%.3f ttft_request_max_ms=%.3f ttft_samples=%d/%d ttft_task_first_avg_ms=%.3f ttft_task_first_p50_ms=%.3f ttft_task_first_p95_ms=%.3f ttft_task_first_max_ms=%.3f task_total_ms=%.3f task_avg_ms=%.3f task_p50_ms=%.3f task_p95_ms=%.3f task_max_ms=%.3f duration_samples=%d\n",
		label, durationMillisFloat(aggregate.AverageTTFTNanos),
		durationMillis(aggregate.P50TTFTNanos), durationMillis(aggregate.P95TTFTNanos),
		durationMillis(aggregate.MaxTTFTNanos), aggregate.TTFTSamples, aggregate.TTFTRequestCount,
		durationMillisFloat(aggregate.AverageTaskFirstTTFTNanos),
		durationMillis(aggregate.P50TaskFirstTTFTNanos), durationMillis(aggregate.P95TaskFirstTTFTNanos),
		durationMillis(aggregate.MaxTaskFirstTTFTNanos),
		durationMillis(aggregate.TotalDurationNanos), durationMillisFloat(aggregate.AverageDurationNanos),
		durationMillis(aggregate.P50DurationNanos), durationMillis(aggregate.P95DurationNanos),
		durationMillis(aggregate.MaxDurationNanos), aggregate.DurationSamples)
}

func tokenSavingText(comparison metrics.Comparison) string {
	if !comparison.TokenDeltaComparable {
		return "unavailable"
	}
	return fmt.Sprintf("%.1f%%", -100*comparison.TokenDelta.Relative)
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "activationbench: "+format+"\n", args...)
	os.Exit(1)
}
