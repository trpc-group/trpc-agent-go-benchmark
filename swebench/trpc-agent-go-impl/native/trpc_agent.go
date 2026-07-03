//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
	aagent "trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
	aeven "trpc.group/trpc-go/trpc-agent-go/event"
	amodel "trpc.group/trpc-go/trpc-agent-go/model"
	arunner "trpc.group/trpc-go/trpc-agent-go/runner"
	atool "trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	trpcAppName   = "swebench-native"
	trpcAgentName = "swe-agent"
)

func runInstanceWithTRPCAgent(ctx context.Context, inst dataset.Instance, ws workspace, req RunRequest) loopResult {
	start := time.Now()
	trace := instanceTrace{
		InstanceID:  inst.InstanceID,
		Repo:        inst.Repo,
		BaseCommit:  inst.BaseCommit,
		Model:       req.Model,
		Environment: nativeEnvironment(req.Environment),
		Image:       ws.Image,
		Status:      "incomplete",
	}
	model, err := newGLMAgentModel(req.GLM, req.Timeout)
	if err != nil {
		trace.Status = "error"
		trace.Error = err.Error()
		return loopResult{Status: "error", DurationMs: time.Since(start).Milliseconds(), Trace: trace, Err: err}
	}
	collector := newTRPCTraceCollector(inst, req, ws)
	flusher := newPartialTraceFlusher(req.PartialTracePath)
	bash := newBashTool(ws, req)
	bash.setAfterExecution(func() {
		flusher.flush(collector.snapshot(bash.executionSnapshot()))
	})
	agentRunner := buildSWEAgentRunner(model, bash, req)
	defer agentRunner.Close()
	flusher.flush(collector.snapshot(nil))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	events, err := agentRunner.Run(
		runCtx,
		"swebench",
		inst.InstanceID,
		amodel.NewUserMessage(sweUserPrompt(inst)),
		aagent.WithRequestID(uuid.NewString()),
	)
	if err != nil {
		trace.Status = "error"
		trace.Error = err.Error()
		return loopResult{Status: "error", DurationMs: time.Since(start).Milliseconds(), Trace: trace, Err: err}
	}

	var loopErr error
	for ev := range events {
		if ev == nil {
			continue
		}
		collector.observe(ev)
		flusher.flush(collector.snapshot(bash.executionSnapshot()))
		if ev.Error != nil && loopErr == nil {
			loopErr = errors.New(ev.Error.Message)
		}
		if totalTokens := collector.totalTokens(); req.TokenLimit > 0 && totalTokens > req.TokenLimit {
			collector.setStopReason(fmt.Sprintf("token_limit_reached:%d>%d", totalTokens, req.TokenLimit))
			cancel()
		}
	}
	trace = collector.finish(bash.executionSnapshot())

	files, filesErr := ws.changedFiles(ctx)
	if filesErr != nil && loopErr == nil {
		loopErr = filesErr
	}
	forbidden := forbiddenPatchFiles(files)
	if len(forbidden) > 0 {
		if err := ws.revertFiles(ctx, forbidden); err != nil && loopErr == nil {
			loopErr = err
		} else {
			trace.RevertedFiles = forbidden
			files, filesErr = ws.changedFiles(ctx)
			if filesErr != nil && loopErr == nil {
				loopErr = filesErr
			}
		}
	}
	submittedPatch, submitted := bash.submission()
	if submitted && trace.StopReason == "" {
		trace.StopReason = "submitted"
	}
	patch := submittedPatch
	if strings.TrimSpace(patch) == "" {
		var diffErr error
		patch, diffErr = ws.diff(ctx)
		if diffErr != nil && loopErr == nil {
			loopErr = diffErr
		}
		if diffErr != nil {
			patch = ""
		}
	} else {
		files = patchChangedFiles(patch)
	}
	added, deleted := patchStats(patch)
	status := "pending_verifier"
	if strings.TrimSpace(patch) == "" {
		status = "incomplete"
	}
	if loopErr != nil {
		status = "error"
		trace.Error = loopErr.Error()
	}
	trace.Status = status
	flusher.flush(trace)
	return loopResult{
		Patch:        patch,
		ChangedFiles: files,
		PatchAdded:   added,
		PatchDeleted: deleted,
		Status:       status,
		Usage:        collector.usage(),
		DurationMs:   time.Since(start).Milliseconds(),
		Trace:        trace,
		Err:          loopErr,
	}
}

func buildSWEAgentRunner(model amodel.Model, bash *bashTool, req RunRequest) arunner.Runner {
	temperature := 0.0
	agt := llmagent.New(
		trpcAgentName,
		llmagent.WithModel(model),
		llmagent.WithDescription("A SWE-Bench agent that edits code through a bash tool."),
		llmagent.WithGlobalInstruction(sweSystemPrompt()),
		llmagent.WithGenerationConfig(amodel.GenerationConfig{
			Temperature: &temperature,
			Stream:      false,
		}),
		llmagent.WithTools([]atool.Tool{bash}),
		llmagent.WithToolCallbacks(miniToolCallbacks(bash)),
		llmagent.WithMaxLLMCalls(req.StepLimit),
		llmagent.WithMaxToolIterations(req.StepLimit),
		llmagent.WithEnableParallelTools(true),
		llmagent.WithReasoningContentMode(llmagent.ReasoningContentModeKeepAll),
		llmagent.WithMessageFilterMode(llmagent.FullContext),
	)
	return arunner.NewRunner(trpcAppName, agt)
}

type trpcTraceCollector struct {
	mu          sync.Mutex
	trace       instanceTrace
	totalUsage  result.Usage
	stopReason  string
	pendingStep []stepTrace
	errText     string
}

func newTRPCTraceCollector(inst dataset.Instance, req RunRequest, ws workspace) *trpcTraceCollector {
	return &trpcTraceCollector{
		trace: instanceTrace{
			InstanceID:  inst.InstanceID,
			Repo:        inst.Repo,
			BaseCommit:  inst.BaseCommit,
			Model:       req.Model,
			Environment: nativeEnvironment(req.Environment),
			Image:       ws.Image,
			Status:      "incomplete",
		},
	}
}

func (c *trpcTraceCollector) observe(ev *aeven.Event) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.observeLocked(ev)
}

func (c *trpcTraceCollector) observeLocked(ev *aeven.Event) {
	if ev == nil || ev.Response == nil {
		return
	}
	if ev.Error != nil {
		c.errText = ev.Error.Message
		return
	}
	if ev.Usage != nil {
		c.totalUsage = addUsage(c.totalUsage, result.Usage{
			PromptTokens:     ev.Usage.PromptTokens,
			CompletionTokens: ev.Usage.CompletionTokens,
			TotalTokens:      ev.Usage.TotalTokens,
		})
	}
	for _, choice := range ev.Choices {
		msg := choice.Message
		if len(msg.ToolCalls) == 0 && len(choice.Delta.ToolCalls) > 0 {
			msg = choice.Delta
		}
		if len(msg.ToolCalls) > 0 {
			c.observeToolCallsLocked(msg)
			continue
		}
		if msg.ToolID != "" || choice.Delta.ToolID != "" {
			if c.stopReason == "" && strings.Contains(msg.Content+choice.Delta.Content, submissionSentinel) {
				c.stopReason = "submitted"
			}
			continue
		}
		if ev.IsFinalResponse() && strings.TrimSpace(msg.Content) != "" {
			c.trace.Steps = append(c.trace.Steps, stepTrace{
				Step:      len(c.trace.Steps) + 1,
				Assistant: msg.Content,
				Action:    agentAction{Final: msg.Content},
			})
		}
	}
}

func (c *trpcTraceCollector) observeToolCallsLocked(msg amodel.Message) {
	for i, call := range msg.ToolCalls {
		action, err := trpcToolCallAction(call)
		st := stepTrace{
			Step:      len(c.pendingStep) + len(c.trace.Steps) + 1,
			Assistant: msg.Content,
			Action:    action,
		}
		if i == 0 {
			st.Usage = c.lastUsage()
		}
		if err != nil {
			st.Error = err.Error()
		}
		c.pendingStep = append(c.pendingStep, st)
	}
}

func (c *trpcTraceCollector) lastUsage() result.Usage {
	return result.Usage{}
}

func (c *trpcTraceCollector) finish(commands []commandResult) instanceTrace {
	return c.snapshot(commands)
}

func (c *trpcTraceCollector) snapshot(commands []commandResult) instanceTrace {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked(commands)
}

func (c *trpcTraceCollector) snapshotLocked(commands []commandResult) instanceTrace {
	trace := c.trace
	trace.Steps = append([]stepTrace(nil), c.trace.Steps...)
	for i, st := range c.pendingStep {
		if i < len(commands) {
			st.Command = commands[i]
		}
		trace.Steps = append(trace.Steps, st)
	}
	trace.StopReason = c.stopReason
	if c.errText != "" {
		trace.Error = c.errText
	}
	return trace
}

func (c *trpcTraceCollector) totalTokens() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalUsage.TotalTokens
}

func (c *trpcTraceCollector) usage() result.Usage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.totalUsage
}

func (c *trpcTraceCollector) setStopReason(reason string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stopReason = reason
}

type partialTraceFlusher struct {
	mu   sync.Mutex
	path string
	err  error
}

func newPartialTraceFlusher(path string) *partialTraceFlusher {
	return &partialTraceFlusher{path: strings.TrimSpace(path)}
}

func (f *partialTraceFlusher) flush(trace instanceTrace) {
	if f == nil || f.path == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := result.WriteJSON(f.path, trace); err != nil && f.err == nil {
		f.err = err
	}
}

func trpcToolCallAction(call amodel.ToolCall) (agentAction, error) {
	if call.Function.Name != bashToolName {
		return agentAction{}, fmt.Errorf("unsupported tool %q", call.Function.Name)
	}
	var args bashToolArgs
	if err := jsonUnmarshal(call.Function.Arguments, &args); err != nil {
		return agentAction{}, fmt.Errorf("parse bash tool arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return agentAction{}, fmt.Errorf("bash tool call missing command")
	}
	return agentAction{Command: args.Command}, nil
}

func jsonUnmarshal(data []byte, v any) error {
	return json.Unmarshal(data, v)
}
