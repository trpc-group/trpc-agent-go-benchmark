//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package runner executes ActivationBench-Lite suites against trpc-agent-go.
// It keeps task/tool state in memory, materializes Skills as temporary local
// SKILL.md files, and exposes paired Static-All and Dynamic-Activation runs
// for reproducible local experiments.
package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/catalog"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/metrics"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	frameworkrunner "trpc.group/trpc-go/trpc-agent-go/runner"
	"trpc.group/trpc-go/trpc-agent-go/session"
	"trpc.group/trpc-go/trpc-agent-go/session/inmemory"
	trpcskill "trpc.group/trpc-go/trpc-agent-go/skill"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// ModelInput contains the non-oracle information supplied to a ModelFactory.
//
// Deliberately do not expose the task id or session id (either may contain a
// domain label), required tools/skills, evaluator, initial state, or benchmark
// mode here. Those values are benchmark labels; exposing them to a provider
// adapter would make the Static-All/Dynamic comparison susceptible to oracle
// leakage. A factory can construct a provider request from the prompt; the
// runner supplies the actual tool declarations to the agent separately.
type ModelInput struct {
	Prompt string
}

// ModelFactory creates the model used for one task/mode pair. The input is
// intentionally sanitized (see ModelInput). A factory must return a non-nil
// model; returning nil is an explicit configuration error. A caller provides
// a real provider model here while retaining the same tool surface and metrics.
type ModelFactory func(ModelInput) model.Model

// ModelRequestTrace is a point-in-time snapshot of the request passed to the
// framework's before-model callback. It is intended for diagnostics, not for
// the benchmark's measured metrics. Messages are kept verbatim so a trace can
// answer whether earlier assistant tool calls and tool results were retained
// on subsequent model turns.
type ModelRequestTrace struct {
	TaskID       string                 `json:"task_id"`
	Mode         string                 `json:"mode"`
	RequestIndex int                    `json:"request_index"`
	Messages     []model.Message        `json:"messages"`
	Tools        []ModelToolDeclaration `json:"tools,omitempty"`
	Generation   model.GenerationConfig `json:"generation_config"`
}

// ModelToolDeclaration is the JSON-safe view of one tool exposed in a model
// request. The framework keeps Request.Tools as an interface map, so traces
// flatten each declaration while preserving its name, description, and input
// schema.
type ModelToolDeclaration struct {
	Name        string       `json:"name"`
	Description string       `json:"description,omitempty"`
	InputSchema *tool.Schema `json:"input_schema,omitempty"`
}

// ModelRequestObserver observes a request immediately before the framework
// invokes the model. It must not mutate the trace. Observers are optional and
// are deliberately outside the measured metrics path unless a caller chooses
// to enable one for diagnostics.
type ModelRequestObserver func(ModelRequestTrace)

// Config controls a Runner.
type Config struct {
	Suite        bench.Suite
	ModelFactory ModelFactory
	// SkillRoot optionally points at a caller-owned local Skill directory
	// containing one <skill>/SKILL.md tree. When empty, the runner creates one
	// temporary filesystem repository per arm from the suite metadata. The
	// repository is opened once per arm and reused by all tasks in that arm.
	SkillRoot         string
	AgentName         string
	AppName           string
	UserID            string
	Lifetime          llmagent.ToolActivationLifetime
	MaxLLMCalls       int
	MaxToolIterations int
	// DisableStreaming opts out of the benchmark's default streaming request
	// mode. Streaming is enabled by default so Recorder can observe an
	// adapter-visible first response for real providers; set this only when a
	// provider or a control arm explicitly requires non-streaming behavior.
	DisableStreaming bool
	// EnableFrameworkTracing opts into the framework's OpenTelemetry spans.
	// It is disabled by default so a local benchmark does not inherit an
	// embedding process's exporter or add tracing work to the measured arms.
	// Set it only when tracing itself is part of the experiment.
	EnableFrameworkTracing bool
	// ModelRequestObserver receives a snapshot from the framework's
	// before-model callback for every model turn. Leave nil for normal benchmark
	// runs; the CLI exposes this as -request-trace for troubleshooting context
	// retention and repeated tool calls.
	ModelRequestObserver ModelRequestObserver
}

// Runner executes one or both benchmark arms.
type Runner struct {
	config Config
}

// New validates config and creates a Runner.
func New(config Config) (*Runner, error) {
	if err := config.Suite.Validate(); err != nil {
		return nil, err
	}
	if config.ModelFactory == nil {
		return nil, fmt.Errorf("model factory is required; configure a real provider model")
	}
	// Keep benchmark authors on canonical raw tool names while ensuring every
	// model-facing Skill, Tool, schema, and task prompt uses the framework's
	// ToolSet-qualified spelling. Rendering once here avoids per-task mutation
	// and keeps the model-facing surface identical across both benchmark arms.
	modelSuite, err := bench.RenderModelFacingSuite(config.Suite)
	if err != nil {
		return nil, err
	}
	config.Suite = modelSuite
	if strings.TrimSpace(config.AgentName) == "" {
		config.AgentName = "activationbench-agent"
	}
	if strings.TrimSpace(config.AppName) == "" {
		config.AppName = "activationbench"
	}
	if strings.TrimSpace(config.UserID) == "" {
		config.UserID = "local"
	}
	if config.Lifetime == "" {
		config.Lifetime = llmagent.ToolActivationLifetimeInvocation
	}
	if config.Lifetime != llmagent.ToolActivationLifetimeInvocation &&
		config.Lifetime != llmagent.ToolActivationLifetimeSession {
		return nil, fmt.Errorf("unsupported activation lifetime %q", config.Lifetime)
	}
	if config.MaxLLMCalls < 0 {
		return nil, fmt.Errorf("max llm calls must be non-negative")
	}
	if config.MaxToolIterations < 0 {
		return nil, fmt.Errorf("max tool iterations must be non-negative")
	}
	return &Runner{config: config}, nil
}

// Config returns the runner configuration value. New stores a model-facing
// copy of the supplied Suite, and callers should treat the returned fields as
// read-only while a run is in progress.
func (r *Runner) Config() Config {
	if r == nil {
		return Config{}
	}
	return r.config
}

// Run executes both Static-All and Dynamic-Activation arms and returns paired
// raw results plus aggregates. The arms run sequentially with the same ctx;
// callers doing a paired timeout experiment should pass a context without a
// shared deadline, or call RunMode with equivalent fresh contexts per arm.
// Cancellation is intentionally propagated and stops the remaining arm rather
// than being replaced with context.Background.
func (r *Runner) Run(ctx context.Context) (metrics.Comparison, error) {
	if r == nil {
		return metrics.Comparison{}, fmt.Errorf("runner is nil")
	}
	if ctx == nil {
		return metrics.Comparison{}, fmt.Errorf("context must not be nil")
	}
	static, err := r.RunMode(ctx, bench.ModeStaticAll)
	if err != nil {
		return metrics.Comparison{}, err
	}
	dynamic, err := r.RunMode(ctx, bench.ModeDynamicActivation)
	if err != nil {
		return metrics.Comparison{}, err
	}
	comparison, err := metrics.NewCheckedComparison(
		r.config.Suite.Name,
		metrics.AnnotateRepetition(static, 0),
		metrics.AnnotateRepetition(dynamic, 0),
	)
	if err != nil {
		return metrics.Comparison{}, err
	}
	return comparison, nil
}

// RunMode executes every task in mode.  Results are returned in suite task
// order, which makes paired statistical analysis straightforward.
func (r *Runner) RunMode(ctx context.Context, mode bench.Mode) ([]metrics.RunResult, error) {
	if r == nil {
		return nil, fmt.Errorf("runner is nil")
	}
	if ctx == nil {
		return nil, fmt.Errorf("context must not be nil")
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("unsupported benchmark mode %q", mode)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	skillRepository, err := r.openSkillRepository()
	if err != nil {
		return nil, err
	}
	defer func() { _ = skillRepository.Close() }()
	results := make([]metrics.RunResult, 0, len(r.config.Suite.Tasks))
	// The framework Runner owns Session creation, event persistence, and the
	// invocation lifecycle. A fresh in-memory service per arm prevents state
	// leakage between Static-All and Dynamic-Activation (and between repeats),
	// while tasks that intentionally share SessionID still reuse activation
	// state through the framework's normal session service.
	sessionService := inmemory.NewSessionService()
	defer func() { _ = sessionService.Close() }()
	for _, task := range r.config.Suite.Tasks {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		result, err := r.runTask(ctx, task, mode, sessionService, skillRepository)
		// Keep a task result whenever runTask reached the task execution phase,
		// even if cancellation/deadline turns the error into control flow. The
		// result contains the observed duration, request usage, and error string;
		// dropping it would make a timeout impossible to audit.
		if result.TaskID != "" {
			results = append(results, result)
		}
		if err != nil {
			return results, err
		}
	}
	return results, nil
}

// RunTask executes one task in mode and returns an auditable result.  The task
// state is isolated from every other run.
func (r *Runner) RunTask(ctx context.Context, task bench.Task, mode bench.Mode) (metrics.RunResult, error) {
	if ctx == nil {
		return metrics.RunResult{}, fmt.Errorf("context must not be nil")
	}
	if !mode.Valid() {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: fmt.Sprintf("unsupported benchmark mode %q", mode)}, fmt.Errorf("unsupported benchmark mode %q", mode)
	}
	if err := ctx.Err(); err != nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: err.Error()}, err
	}
	skillRepository, err := r.openSkillRepository()
	if err != nil {
		return metrics.RunResult{}, err
	}
	defer func() { _ = skillRepository.Close() }()
	sessionService := inmemory.NewSessionService()
	defer func() { _ = sessionService.Close() }()
	return r.runTask(ctx, task, mode, sessionService, skillRepository)
}

// runTask executes one task through the framework Runner. The session service
// and Skill repository are owned by the enclosing RunMode/RunTask call so the
// framework can persist assistant/tool events, reuse session-scoped activation
// state, and reuse one fixed Skill catalog across all tasks in that arm. Each
// task gets a distinct event-filter branch so its benchmark prompt does not
// inherit another task's conversation history.
func (r *Runner) runTask(
	ctx context.Context,
	task bench.Task,
	mode bench.Mode,
	sessionService session.Service,
	skillRepository trpcskill.Repository,
) (metrics.RunResult, error) {
	if r == nil {
		return metrics.RunResult{}, fmt.Errorf("runner is nil")
	}
	if ctx == nil {
		return metrics.RunResult{}, fmt.Errorf("context must not be nil")
	}
	if !mode.Valid() {
		return metrics.RunResult{}, fmt.Errorf("unsupported benchmark mode %q", mode)
	}
	// Avoid constructing a provider (which may allocate resources or perform
	// its own setup) when the caller has already cancelled this invocation.
	if err := ctx.Err(); err != nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: err.Error()}, err
	}
	if sessionService == nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: "session service is nil"}, fmt.Errorf("session service is nil")
	}
	if skillRepository == nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: "skill repository is nil"}, fmt.Errorf("skill repository is nil")
	}
	state := bench.NewTaskState(task.InitialState)
	toolSpecs := append([]bench.ToolSpec(nil), r.config.Suite.Tools...)
	skillSpecs := append([]bench.SkillSpec(nil), r.config.Suite.Skills...)
	modelPrompt, err := bench.RenderModelFacingText(task.Prompt, toolSpecs)
	if err != nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: err.Error()}, err
	}
	toolSetSpecs, baseSpecs, _, err := buildToolSets(toolSpecs, skillSpecs, state)
	if err != nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), Error: err.Error()}, err
	}
	qualifiedRequired := qualifiedRequiredTools(task, toolSpecs)
	sessionID := taskSessionID(task)
	modelInput := ModelInput{
		Prompt: modelPrompt,
	}
	baseModel, err := r.makeModel(modelInput)
	if err != nil {
		return metrics.RunResult{TaskID: task.ID, Mode: string(mode), SessionID: sessionID, Error: err.Error()}, err
	}
	recorder := metrics.NewRecorder(baseModel)
	options := []llmagent.Option{
		llmagent.WithModel(recorder),
		llmagent.WithSkills(skillRepository),
		// Keep same-invocation assistant/tool messages intact. The framework's
		// default rewrites same-branch history as user context, which is useful
		// for cross-agent summaries but drops the tool-call/result protocol that
		// a function-calling model needs in its next turn.
		llmagent.WithPreserveSameBranch(true),
		// The benchmark never executes a shell or external command. An explicit
		// allowlist prevents WithSkills from installing any unrelated
		// Skill surface or the convenience workspace executor. skill_load is
		// the only framework Skill operation needed to trigger activation here.
		llmagent.WithAllowedSkillTools(llmagent.SkillToolLoad),
		llmagent.WithEnableCodeExecutionResponseProcessor(false),
		// Task handlers mutate one in-memory state object. Keep framework tool
		// execution serial so the fixture's state transitions are deterministic.
		llmagent.WithEnableParallelTools(false),
	}
	if observer := r.config.ModelRequestObserver; observer != nil {
		requestIndex := 0
		callbacks := model.NewCallbacks()
		callbacks.RegisterBeforeModel(func(_ context.Context, args *model.BeforeModelArgs) (*model.BeforeModelResult, error) {
			if args != nil && args.Request != nil {
				observer(snapshotModelRequest(task.ID, mode, requestIndex, args.Request))
				requestIndex++
			}
			return &model.BeforeModelResult{}, nil
		})
		options = append(options, llmagent.WithModelCallbacks(callbacks))
	}
	if len(baseSpecs) > 0 {
		options = append(options, llmagent.WithTools(baseToolInstances(baseSpecs, state)))
	}
	if mode == bench.ModeStaticAll {
		if len(toolSetSpecs) > 0 {
			options = append(options, llmagent.WithToolSets(toolSetSpecs))
		}
	} else {
		if len(toolSetSpecs) > 0 {
			options = append(options, llmagent.WithActivatableToolSets(toolSetSpecs))
			for _, skillSpec := range skillSpecs {
				sets := skillToolSetNames(skillSpec, toolSetSpecs, toolSpecs)
				if len(sets) == 0 {
					continue
				}
				// WithToolActivationOnSkillLoad defaults to Include: the framework
				// appends the activated set while retaining explicitly registered
				// base tools. The Lite fixture puts all domain tools in ToolSets;
				// retaining the default also keeps custom base-tool suites sound.
				options = append(options, llmagent.WithToolActivationOnSkillLoad(
					skillSpec.Name,
					sets,
					llmagent.WithToolActivationLifetime(r.config.Lifetime),
				))
			}
		}
	}
	if r.config.MaxLLMCalls > 0 {
		options = append(options, llmagent.WithMaxLLMCalls(r.config.MaxLLMCalls))
	}
	if r.config.MaxToolIterations > 0 {
		options = append(options, llmagent.WithMaxToolIterations(r.config.MaxToolIterations))
	}
	agentInstance := llmagent.New(r.config.AgentName, options...)
	runOptions := []agent.RunOption{
		agent.WithDisableTracing(!r.config.EnableFrameworkTracing),
		// Tasks that share a SessionID may reuse activation state, but their
		// conversation histories must remain independent. Event filtering is a
		// public Runner/Session mechanism and avoids mutating persisted events.
		agent.WithEventFilterKey(taskEventFilterKey(r.config.AppName, task)),
	}
	if !r.config.DisableStreaming {
		// Use the framework's per-invocation override rather than rewriting the
		// model request in Recorder. This keeps provider adapters responsible for
		// honoring the public GenerationConfig.Stream contract.
		runOptions = append(runOptions, agent.WithStream(true))
	}
	frameworkInstance := frameworkrunner.NewRunner(
		r.config.AppName,
		agentInstance,
		frameworkrunner.WithSessionService(sessionService),
	)
	defer func() { _ = frameworkInstance.Close() }()
	start := time.Now()
	var runErr error
	events, err := frameworkInstance.Run(
		ctx,
		r.config.UserID,
		sessionID,
		model.NewUserMessage(modelPrompt),
		runOptions...,
	)
	if err != nil {
		runErr = err
	} else {
		runErr = drainRunEvents(events)
	}
	duration := time.Since(start)
	// Resolve required tool aliases against the actual suite metadata.  This
	// keeps the default evaluator correct for custom ToolSet names (for
	// example, research_ops_search), while preserving the conservative legacy
	// fallback exposed by bench.DefaultEvaluation for standalone callers.
	evaluation := bench.DefaultEvaluationWithSpecs(task, state, toolSpecs)
	if task.Evaluate != nil {
		evaluation = task.Evaluate(state)
	}
	requiredCount := evaluation.RequiredCount
	if requiredCount == 0 && len(task.RequiredTools) > 0 {
		requiredCount = len(task.RequiredTools)
	}
	satisfiedCount := evaluation.SatisfiedCount
	if satisfiedCount < 0 {
		satisfiedCount = 0
	}
	if satisfiedCount > requiredCount && requiredCount > 0 {
		satisfiedCount = requiredCount
	}
	toolRecall := ratio(satisfiedCount, requiredCount)
	requests := recorder.Snapshot()
	toolCalls := observedCalls(requests, state.SnapshotCalls(), toolSpecs)
	toolPrecision := successfulRequiredPrecision(toolCalls, qualifiedRequired, toolSpecs)
	activations := inferActivations(requests, qualifiedRequired, toolSpecs, r.config.Lifetime)
	frameworkCallCount, skillLoadCount := frameworkCallCounts(toolCalls)
	result := metrics.RunResult{
		TaskID:             task.ID,
		Mode:               string(mode),
		SessionID:          sessionID,
		Passed:             evaluation.Passed && runErr == nil,
		Score:              evaluation.Score,
		EvaluationMessage:  evaluation.Message,
		RequiredToolCount:  requiredCount,
		SatisfiedToolCount: satisfiedCount,
		CollateralCount:    evaluation.CollateralCount,
		ToolRecall:         toolRecall,
		ToolPrecision:      toolPrecision,
		DurationNanos:      duration.Nanoseconds(),
		Usage:              usageFromRequests(requests),
		Requests:           requests,
		ToolCalls:          toolCalls,
		Activations:        activations,
		RequiredTools:      append([]string(nil), task.RequiredTools...),
		RequiredSkills:     append([]string(nil), task.RequiredSkills...),
		InitialToolCount:   initialToolCount(requests),
		PeakVisibleTools:   peakVisibleTools(requests),
		FinalVisibleTools:  finalVisibleTools(requests),
		WrongToolCalls:     wrongToolCalls(toolCalls, qualifiedRequired, toolSpecs),
		InvalidToolCalls:   invalidToolCalls(toolCalls),
		LLMCalls:           len(requests),
		ToolCallCount:      len(toolCalls),
		FrameworkCallCount: frameworkCallCount,
		SkillLoadCount:     skillLoadCount,
		ActivationCount:    len(activations),
	}
	if runErr != nil {
		result.Error = runErr.Error()
	}
	// Keep caller cancellation/deadline errors as control-flow errors so
	// RunMode stops before starting another task (or the other paired arm).
	// Ordinary provider/flow errors remain auditable in RunResult and do not
	// abort the rest of a benchmark batch.
	if ctxErr := ctx.Err(); ctxErr != nil {
		if result.Error == "" {
			result.Error = ctxErr.Error()
		}
		return result, ctxErr
	}
	if errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
		return result, runErr
	}
	return result, nil
}

func taskSessionID(task bench.Task) string {
	if sessionID := strings.TrimSpace(task.SessionID); sessionID != "" {
		return sessionID
	}
	if taskID := strings.TrimSpace(task.ID); taskID != "" {
		return taskID
	}
	return "activationbench-session"
}

func taskEventFilterKey(appName string, task bench.Task) string {
	appName = strings.Trim(strings.TrimSpace(appName), "/")
	taskID := strings.Trim(strings.TrimSpace(task.ID), "/")
	if appName == "" {
		appName = "activationbench"
	}
	if taskID == "" {
		taskID = "task"
	}
	return appName + "/benchmark-task/" + taskID
}

func (r *Runner) makeModel(input ModelInput) (model.Model, error) {
	if r.config.ModelFactory == nil {
		return nil, fmt.Errorf("model factory is required; benchmark runs must use a real provider model")
	}
	candidate := r.config.ModelFactory(input)
	if candidate == nil {
		return nil, fmt.Errorf("model factory returned nil")
	}
	return candidate, nil
}

func snapshotModelRequest(taskID string, mode bench.Mode, index int, request *model.Request) ModelRequestTrace {
	trace := ModelRequestTrace{
		TaskID:       taskID,
		Mode:         string(mode),
		RequestIndex: index,
		Generation:   request.GenerationConfig,
	}
	// Copy the outer slice so a later framework append cannot change the
	// diagnostic snapshot before an asynchronous observer serializes it.
	trace.Messages = append([]model.Message(nil), request.Messages...)
	if len(request.Tools) == 0 {
		return trace
	}
	names := make([]string, 0, len(request.Tools))
	for name := range request.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	trace.Tools = make([]ModelToolDeclaration, 0, len(names))
	for _, name := range names {
		instance := request.Tools[name]
		if instance == nil {
			trace.Tools = append(trace.Tools, ModelToolDeclaration{Name: name})
			continue
		}
		declaration := instance.Declaration()
		if declaration == nil {
			trace.Tools = append(trace.Tools, ModelToolDeclaration{Name: name})
			continue
		}
		trace.Tools = append(trace.Tools, ModelToolDeclaration{
			Name:        declaration.Name,
			Description: declaration.Description,
			InputSchema: declaration.InputSchema,
		})
	}
	return trace
}

func drainRunEvents(events <-chan *event.Event) error {
	if events == nil {
		return fmt.Errorf("agent returned a nil event channel")
	}
	var firstErr error
	for ev := range events {
		if ev == nil {
			continue
		}
		// The framework Runner has already persisted the event and released any
		// completion barrier before forwarding it. We only observe terminal errors
		// here so benchmark results can distinguish a failed invocation; no event
		// or Session mutation belongs in this consumer.
		if (ev.IsTerminalError() || (ev.IsError() && ev.Done)) && firstErr == nil {
			firstErr = eventError(ev)
		}
	}
	return firstErr
}

func eventError(ev *event.Event) error {
	if ev == nil {
		return nil
	}
	if ev.Response != nil && ev.Response.Error != nil {
		message := strings.TrimSpace(ev.Response.Error.Message)
		if typ := strings.TrimSpace(ev.Response.Error.Type); typ != "" && message != "" {
			return fmt.Errorf("%s: %s", typ, message)
		}
		if message != "" {
			return fmt.Errorf("%s", message)
		}
	}
	if ev.IsError() {
		return fmt.Errorf("agent emitted an error event")
	}
	return nil
}

// callableTool wraps a ToolSpec and records every execution in task state.
type callableTool struct {
	spec     bench.ToolSpec
	fullName string
	state    *bench.TaskState
}

func (t *callableTool) Declaration() *tool.Declaration {
	return &tool.Declaration{
		Name:         t.spec.Name,
		Description:  t.spec.Description,
		InputSchema:  t.spec.InputSchema,
		OutputSchema: t.spec.OutputSchema,
	}
}

func (t *callableTool) Call(ctx context.Context, args []byte) (any, error) {
	if t == nil {
		return nil, fmt.Errorf("activationbench: nil callable tool")
	}
	handler := t.spec.Handler
	if handler == nil {
		err := fmt.Errorf("tool %q has no executable implementation", t.fullName)
		t.state.RecordCall(bench.CallRecord{
			Name: t.fullName, Arguments: string(args), Succeeded: false, Error: err.Error(),
		})
		return nil, err
	}
	value, err := handler(ctx, args, t.state)
	record := bench.CallRecord{
		Name:      t.fullName,
		Arguments: string(args),
		Succeeded: err == nil,
	}
	if err != nil {
		record.Error = err.Error()
	}
	t.state.RecordCall(record)
	return value, err
}

var _ tool.CallableTool = (*callableTool)(nil)

type taskToolSet struct {
	name  string
	tools []tool.Tool
}

func (s *taskToolSet) Name() string { return s.name }

func (s *taskToolSet) Tools(context.Context) []tool.Tool {
	return append([]tool.Tool(nil), s.tools...)
}

func (*taskToolSet) Close() error { return nil }

func buildToolSets(specs []bench.ToolSpec, skills []bench.SkillSpec, state *bench.TaskState) ([]tool.ToolSet, []bench.ToolSpec, map[string]string, error) {
	skillNames := make(map[string]bool, len(skills))
	for _, spec := range skills {
		skillNames[strings.TrimSpace(spec.Name)] = true
	}
	groups := make(map[string][]bench.ToolSpec)
	order := make([]string, 0)
	base := make([]bench.ToolSpec, 0)
	qualifiedSkills := make(map[string]string)
	for _, spec := range specs {
		skillName := strings.TrimSpace(spec.Skill)
		if skillName == "" {
			base = append(base, spec)
			continue
		}
		if !skillNames[skillName] {
			return nil, nil, nil, fmt.Errorf("tool %q references unknown skill %q", spec.Name, spec.Skill)
		}
		setName := strings.TrimSpace(spec.ToolSet)
		if setName == "" {
			setName = bench.ToolSetName(skillName)
		}
		if _, seen := groups[setName]; !seen {
			order = append(order, setName)
		}
		groups[setName] = append(groups[setName], spec)
		qualifiedSkills[bench.QualifiedToolName(spec)] = skillName
	}
	sets := make([]tool.ToolSet, 0, len(order))
	for _, setName := range order {
		setSpecs := groups[setName]
		instances := make([]tool.Tool, 0, len(setSpecs))
		for _, spec := range setSpecs {
			instances = append(instances, &callableTool{
				spec:     spec,
				fullName: bench.QualifiedToolName(spec),
				state:    state,
			})
		}
		sets = append(sets, &taskToolSet{name: setName, tools: instances})
	}
	return sets, base, qualifiedSkills, nil
}

func baseToolInstances(specs []bench.ToolSpec, state *bench.TaskState) []tool.Tool {
	instances := make([]tool.Tool, 0, len(specs))
	for _, spec := range specs {
		instances = append(instances, &callableTool{
			spec:     spec,
			fullName: bench.QualifiedToolName(spec),
			state:    state,
		})
	}
	return instances
}

func skillToolSetNames(skillSpec bench.SkillSpec, sets []tool.ToolSet, specs []bench.ToolSpec) []string {
	if len(skillSpec.ToolSets) > 0 {
		return uniqueStrings(skillSpec.ToolSets)
	}
	defaultName := bench.ToolSetName(skillSpec.Name)
	for _, set := range sets {
		if set.Name() == defaultName {
			return []string{defaultName}
		}
	}
	// A custom ToolSet may be used by all tools in this Skill. Infer it from
	// catalog metadata when exactly one candidate remains. This keeps custom
	// suites usable when SkillSpec.ToolSets is omitted, without guessing when a
	// Skill intentionally spans multiple sets.
	candidates := make(map[string]bool)
	for _, spec := range specs {
		if strings.TrimSpace(spec.Skill) != strings.TrimSpace(skillSpec.Name) {
			continue
		}
		setName := strings.TrimSpace(spec.ToolSet)
		if setName == "" {
			setName = bench.ToolSetName(spec.Skill)
		}
		if setName != "" {
			candidates[setName] = true
		}
	}
	if len(candidates) != 1 {
		return nil
	}
	for setName := range candidates {
		return []string{setName}
	}
	return nil
}

func qualifiedRequiredTools(task bench.Task, specs []bench.ToolSpec) []string {
	byName := make(map[string]string, len(specs))
	for _, spec := range specs {
		byName[spec.Name] = bench.QualifiedToolName(spec)
		byName[bench.QualifiedToolName(spec)] = bench.QualifiedToolName(spec)
	}
	result := make([]string, 0, len(task.RequiredTools))
	for _, name := range task.RequiredTools {
		if qualified, ok := byName[strings.TrimSpace(name)]; ok {
			result = append(result, qualified)
		} else {
			result = append(result, strings.TrimSpace(name))
		}
	}
	return result
}

// inferActivations derives a conservative, auditable activation signal from
// the public request trace. A skill_load call is considered an activation only
// when all of the following hold:
//   - the call is the canonical skill_load tool and has one well-formed,
//     catalog-known skill argument;
//   - the immediately following model request exposes at least one new tool;
//   - every newly exposed tool belongs to that skill according to ToolSpec.
//
// We intentionally inspect only the adjacent request. Looking arbitrarily far
// ahead can attribute a later, unrelated menu change to a failed/empty load.
// A response containing multiple skill_load calls is skipped because the
// public request trace cannot prove which call produced which menu change.
// Visible snapshots are refreshed on every request, including requests that
// contain no load. This keeps the baseline correct when a session lifetime
// reuses an already-active ToolSet or when a provider changes its tool menu.
func inferActivations(
	requests []metrics.RequestRecord,
	required []string,
	specs []bench.ToolSpec,
	lifetime llmagent.ToolActivationLifetime,
) []metrics.ActivationRecord {
	if len(requests) < 2 {
		return nil
	}

	aliases := bench.ToolNameAliases(specs)
	toolsBySkill := activationToolsBySkill(specs, aliases)
	if len(toolsBySkill) == 0 {
		return nil
	}
	requiredSet := make(map[string]bool, len(required))
	for _, name := range required {
		name = strings.TrimSpace(name)
		if canonical, ok := aliases[name]; ok {
			name = canonical
		}
		if name != "" {
			requiredSet[name] = true
		}
	}

	previous := visibleToolSet(requests[0].VisibleTools, aliases)
	result := make([]metrics.ActivationRecord, 0)
	for index := 1; index < len(requests); index++ {
		current := visibleToolSet(requests[index].VisibleTools, aliases)
		added := setDifference(current, previous)

		// The model call in requests[index-1] is the only event whose result
		// could have changed this snapshot. Do not scan further requests.
		loads := knownSkillLoads(requests[index-1], toolsBySkill)
		if len(loads) == 1 && len(added) > 0 {
			skillName := loads[0]
			expected := toolsBySkill[skillName]
			activated := make([]string, 0, len(added))
			allBelong := true
			for _, visibleName := range sortedSetValues(added) {
				if !expected[visibleName] {
					allBelong = false
					break
				}
				activated = append(activated, visibleName)
			}
			if allBelong && len(activated) > 0 {
				necessary := false
				for _, name := range activated {
					if requiredSet[name] {
						necessary = true
						break
					}
				}
				result = append(result, metrics.ActivationRecord{
					Skill:          skillName,
					ToolSet:        toolSetForActivatedTools(activated, specs),
					ActivatedTools: activated,
					NewToolCount:   len(activated),
					RequestIndex:   index,
					Lifetime:       string(lifetime),
					Necessary:      necessary,
				})
			}
		}

		// Always advance the baseline. In particular, a no-op or failed load
		// must not leave its old snapshot around to claim a later change.
		previous = current
	}
	return result
}

// activationToolsBySkill returns canonical model-facing tool names grouped by
// their owning Skill. ToolSet names are deliberately not parsed from strings;
// custom sets may contain underscores.
func activationToolsBySkill(
	specs []bench.ToolSpec,
	aliases map[string]string,
) map[string]map[string]bool {
	result := make(map[string]map[string]bool)
	for _, spec := range specs {
		skillName := strings.TrimSpace(spec.Skill)
		qualified := strings.TrimSpace(bench.QualifiedToolName(spec))
		if skillName == "" || qualified == "" {
			continue
		}
		if result[skillName] == nil {
			result[skillName] = make(map[string]bool)
		}
		canonical := qualified
		if mapped, ok := aliases[qualified]; ok {
			canonical = mapped
		}
		result[skillName][canonical] = true
	}
	return result
}

// knownSkillLoads returns exactly one valid canonical skill_load target. Any
// malformed, unknown, or duplicate skill_load in a response makes that
// response ambiguous and therefore ineligible for activation inference.
func knownSkillLoads(
	request metrics.RequestRecord,
	toolsBySkill map[string]map[string]bool,
) []string {
	// A response-level error means the framework did not establish a
	// successful skill-load result. Do not infer activation from a menu that
	// may have changed for an unrelated reason.
	if strings.TrimSpace(request.Error) != "" {
		return nil
	}
	loads := make([]string, 0)
	for index, name := range request.ToolCallsReturned {
		if !isFrameworkToolNamed(name, "skill_load") {
			continue
		}
		if index >= len(request.ToolCallArguments) {
			return nil
		}
		var args struct {
			Skill string `json:"skill"`
		}
		if err := json.Unmarshal([]byte(request.ToolCallArguments[index]), &args); err != nil {
			return nil
		}
		// The framework resolves skill names exactly. Accepting a whitespace-
		// normalized spelling here would make the diagnostic more permissive
		// than the actual activation path.
		rawSkill := args.Skill
		if rawSkill == "" || strings.TrimSpace(rawSkill) != rawSkill {
			return nil
		}
		if _, known := toolsBySkill[rawSkill]; !known {
			return nil
		}
		loads = append(loads, rawSkill)
	}
	if len(loads) != 1 {
		return nil
	}
	return loads
}

func visibleToolSet(names []string, aliases map[string]string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if canonical, ok := aliases[name]; ok {
			name = canonical
		}
		set[name] = true
	}
	return set
}

func setDifference(current, previous map[string]bool) map[string]bool {
	added := make(map[string]bool)
	for name := range current {
		if !previous[name] {
			added[name] = true
		}
	}
	return added
}

func sortedSetValues(values map[string]bool) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

// toolSetForActivatedTools resolves a ToolSet using catalog metadata rather
// than splitting on the first underscore. ToolSet names may contain
// underscores, while QualifiedToolName only guarantees a separator between
// the set and tool names.
func toolSetForActivatedTools(activated []string, specs []bench.ToolSpec) string {
	if len(activated) == 0 {
		return ""
	}
	byQualified := make(map[string]string, len(specs))
	for _, spec := range specs {
		qualified := strings.TrimSpace(bench.QualifiedToolName(spec))
		if qualified == "" {
			continue
		}
		setName := strings.TrimSpace(spec.ToolSet)
		if setName == "" {
			setName = bench.ToolSetName(spec.Skill)
		}
		byQualified[qualified] = setName
	}
	for _, name := range activated {
		if setName := byQualified[strings.TrimSpace(name)]; setName != "" {
			return setName
		}
	}
	return ""
}

func convertCalls(calls []bench.CallRecord) []metrics.CallRecord {
	result := make([]metrics.CallRecord, 0, len(calls))
	for _, call := range calls {
		result = append(result, metrics.CallRecord{
			Name:         call.Name,
			Arguments:    call.Arguments,
			Succeeded:    call.Succeeded,
			OutcomeKnown: true,
			Error:        call.Error,
		})
	}
	return result
}

// observedCalls merges model-returned calls with calls that reached a local
// benchmark handler. A model can return an invalid tool name (or the flow can
// fail before execution), so relying only on TaskState.Calls would hide those
// quality signals. Framework calls such as skill_load are retained and marked
// separately for activation accounting.
func observedCalls(requests []metrics.RequestRecord, executed []bench.CallRecord, specs []bench.ToolSpec) []metrics.CallRecord {
	knownSkills := make(map[string]bool)
	toolAliases := bench.ToolNameAliases(specs)
	knownTools := make(map[string]bool, len(toolAliases))
	for name := range toolAliases {
		knownTools[name] = true
	}
	for _, spec := range specs {
		if skill := strings.TrimSpace(spec.Skill); skill != "" {
			knownSkills[skill] = true
		}
	}
	result := make([]metrics.CallRecord, 0, len(executed)+len(requests))
	used := make([]bool, len(executed))
	for _, request := range requests {
		for index, name := range request.ToolCallsReturned {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			args := ""
			if index < len(request.ToolCallArguments) {
				args = request.ToolCallArguments[index]
			}
			if isFrameworkTool(name) {
				// The model response proves an attempt, not the framework tool's
				// internal result. Keep Succeeded false unless a public result/event
				// is available; activation accounting uses the attempt and menu diff.
				call := metrics.CallRecord{Name: name, Arguments: args, Framework: true}
				if isFrameworkToolNamed(name, "skill_load") {
					var payload map[string]any
					if err := json.Unmarshal([]byte(args), &payload); err != nil {
						call.Succeeded = false
						call.OutcomeKnown = true
						call.Error = "invalid skill_load arguments"
					} else if skillName, _ := payload["skill"].(string); !knownSkills[strings.TrimSpace(skillName)] {
						call.Succeeded = false
						call.OutcomeKnown = true
						call.Error = fmt.Sprintf("unknown skill %q", skillName)
					}
				}
				result = append(result, call)
				continue
			}
			match := -1
			for executedIndex, candidate := range executed {
				if used[executedIndex] || !sameToolNameWithAliases(name, candidate.Name, toolAliases) {
					continue
				}
				match = executedIndex
				break
			}
			if match >= 0 {
				used[match] = true
				call := convertCalls([]bench.CallRecord{executed[match]})[0]
				if call.Arguments == "" {
					call.Arguments = args
				}
				result = append(result, call)
				continue
			}
			call := metrics.CallRecord{
				Name: name, Arguments: args, Succeeded: false,
				OutcomeKnown: true,
				Error:        "model tool call was not executed",
			}
			if !knownTools[name] {
				call.Error = "unknown or unavailable tool"
			}
			result = append(result, call)
		}
	}
	// Include any handler calls not represented in a model response. This is
	// uncommon in the standard flow but keeps custom runners auditable.
	for index, call := range executed {
		if used[index] {
			continue
		}
		result = append(result, convertCalls([]bench.CallRecord{call})[0])
	}
	return result
}

func sameToolNameWithAliases(a, b string, aliases map[string]string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	canonicalA, knownA := aliases[a]
	canonicalB, knownB := aliases[b]
	return knownA && knownB && canonicalA == canonicalB
}

func sameToolName(a, b string) bool {
	// Callers with a catalog should use sameToolNameWithAliases. The fallback
	// here recognizes only the framework's conventional <skill>-tools_<tool>
	// prefix; a broad suffix check would incorrectly accept names such as
	// "evil_mail_search".
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	return bench.ConventionalToolSetAlias(a, b) || bench.ConventionalToolSetAlias(b, a)
}

func isFrameworkTool(name string) bool {
	return bench.IsFrameworkToolName(name)
}

func isFrameworkToolNamed(name, target string) bool {
	name = strings.TrimSpace(name)
	target = strings.TrimSpace(target)
	// Framework tools are registered under their canonical names. Do not use a
	// suffix match here: a model-returned name such as evil_skill_load must be
	// counted as an invalid domain call rather than an executed framework call.
	return name != "" && name == target
}

func frameworkCallCounts(calls []metrics.CallRecord) (framework, skillLoads int) {
	for _, call := range calls {
		if !call.Framework {
			continue
		}
		framework++
		if isFrameworkToolNamed(call.Name, "skill_load") {
			skillLoads++
		}
	}
	return framework, skillLoads
}

func usageFromRequests(requests []metrics.RequestRecord) (usage metrics.TokenUsage) {
	for _, request := range requests {
		usage.Add(request.Usage)
	}
	return usage
}

func initialToolCount(requests []metrics.RequestRecord) int {
	if len(requests) == 0 {
		return 0
	}
	return requests[0].VisibleToolCount
}

func peakVisibleTools(requests []metrics.RequestRecord) int {
	peak := 0
	for _, request := range requests {
		if request.VisibleToolCount > peak {
			peak = request.VisibleToolCount
		}
	}
	return peak
}

func finalVisibleTools(requests []metrics.RequestRecord) int {
	if len(requests) == 0 {
		return 0
	}
	return requests[len(requests)-1].VisibleToolCount
}

func wrongToolCalls(calls []metrics.CallRecord, required []string, specs ...[]bench.ToolSpec) int {
	aliases := map[string]string(nil)
	if len(specs) > 0 {
		aliases = bench.ToolNameAliases(specs[0])
	}
	allowed := make(map[string]bool, len(required))
	for _, name := range required {
		name = strings.TrimSpace(name)
		if canonical, ok := aliases[name]; ok {
			name = canonical
		}
		allowed[name] = true
	}
	wrong := 0
	for _, call := range calls {
		if call.Framework {
			continue
		}
		name := strings.TrimSpace(call.Name)
		if canonical, ok := aliases[name]; ok {
			name = canonical
		}
		if allowed[name] {
			continue
		}
		// Preserve the conventional <skill>-tools_<tool> spelling for callers
		// that do not pass a catalog, while still rejecting arbitrary suffix
		// lookalikes. The normal runner path uses the exact alias map above.
		matched := false
		for target := range allowed {
			if sameToolName(name, target) {
				matched = true
				break
			}
		}
		if !matched {
			wrong++
		}
	}
	return wrong
}

func invalidToolCalls(calls []metrics.CallRecord) int {
	invalid := 0
	for _, call := range calls {
		if !call.Framework && !call.Succeeded {
			invalid++
		}
	}
	return invalid
}

// successfulRequiredPrecision is call-attempt precision: every non-framework
// attempt, including duplicate retries, remains in the denominator. It is a
// diagnostic for tool selection, not a unique-tool coverage metric.
func successfulRequiredPrecision(calls []metrics.CallRecord, required []string, specs ...[]bench.ToolSpec) float64 {
	if len(calls) == 0 {
		if len(required) == 0 {
			return 1
		}
		return 0
	}
	aliases := map[string]string(nil)
	if len(specs) > 0 {
		aliases = bench.ToolNameAliases(specs[0])
	}
	requiredSet := make(map[string]bool, len(required))
	for _, name := range required {
		name = strings.TrimSpace(name)
		if canonical, ok := aliases[name]; ok {
			name = canonical
		}
		requiredSet[name] = true
	}
	valid := 0
	considered := 0
	for _, call := range calls {
		if call.Framework {
			continue
		}
		considered++
		if !call.Succeeded {
			continue
		}
		name := strings.TrimSpace(call.Name)
		if canonical, ok := aliases[name]; ok {
			name = canonical
		}
		if requiredSet[name] {
			valid++
			continue
		}
		matched := false
		for target := range requiredSet {
			if sameToolName(name, target) {
				matched = true
				break
			}
		}
		if matched {
			valid++
		}
	}
	if considered == 0 {
		return 0
	}
	return float64(valid) / float64(considered)
}

func ratio(n, d int) float64 {
	if d <= 0 {
		if n <= 0 {
			return 1
		}
		return 0
	}
	return float64(n) / float64(d)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		result = append(result, value)
	}
	return result
}

func (r *Runner) openSkillRepository() (*catalog.SkillRepository, error) {
	if r == nil {
		return nil, fmt.Errorf("runner is nil")
	}
	values := frameworkSkills(r.config.Suite.Skills)
	if root := strings.TrimSpace(r.config.SkillRoot); root != "" {
		repository, err := catalog.OpenSkillRepository(root)
		if err != nil {
			return nil, err
		}
		if err := validateSkillRepository(repository, r.config.Suite.Skills); err != nil {
			_ = repository.Close()
			return nil, err
		}
		return repository, nil
	}
	return catalog.NewTempSkillRepositoryFromSkills(values)
}

func frameworkSkills(specs []bench.SkillSpec) []trpcskill.Skill {
	values := make([]trpcskill.Skill, 0, len(specs))
	for _, spec := range specs {
		name := strings.TrimSpace(spec.Name)
		values = append(values, trpcskill.Skill{
			Summary: trpcskill.Summary{Name: name, Description: spec.Description},
			Body:    spec.Body,
		})
	}
	return values
}

// newTempSkillRepository is retained as a small test/helper entry point. The
// runner uses openSkillRepository so a configured fixed root can be reused;
// callers of this helper always get an owned temporary repository.
func newTempSkillRepository(specs []bench.SkillSpec) (*catalog.SkillRepository, error) {
	return catalog.NewTempSkillRepositoryFromSkills(frameworkSkills(specs))
}

func validateSkillRepository(repository trpcskill.Repository, specs []bench.SkillSpec) error {
	if repository == nil {
		return fmt.Errorf("skill repository is nil")
	}
	expected := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		expected[strings.TrimSpace(spec.Name)] = struct{}{}
	}
	actual := make(map[string]struct{}, len(repository.Summaries()))
	for _, summary := range repository.Summaries() {
		actual[strings.TrimSpace(summary.Name)] = struct{}{}
	}
	var missing, unexpected []string
	for name := range expected {
		if _, ok := actual[name]; !ok {
			missing = append(missing, name)
		}
	}
	for name := range actual {
		if _, ok := expected[name]; !ok {
			unexpected = append(unexpected, name)
		}
	}
	if len(missing) == 0 && len(unexpected) == 0 {
		return nil
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return fmt.Errorf("skill repository does not match suite: missing=%v unexpected=%v", missing, unexpected)
}
