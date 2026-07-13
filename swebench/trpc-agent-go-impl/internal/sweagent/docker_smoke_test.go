//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweagent

import (
	"context"
	"os"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// TestDockerMiniAgentSmoke is opt-in because it pulls/starts an official
// SWE-Bench image. It uses a deterministic scripted model and therefore sends
// no external model requests.
func TestDockerMiniAgentSmoke(t *testing.T) {
	instanceID := os.Getenv("SWE_DOCKER_SMOKE_INSTANCE")
	if instanceID == "" {
		t.Skip("set SWE_DOCKER_SMOKE_INSTANCE to run the Docker parity smoke")
	}
	var config environment.Config
	config.Environment.Interpreter = []string{"bash", "-c"}
	config.Environment.Env = map[string]string{"PAGER": "cat", "MANPAGER": "cat"}
	factory := environment.DockerFactory{
		Config:         config,
		DockerHost:     os.Getenv("DOCKER_HOST"),
		CommandTimeout: time.Minute,
		CaseTimeout:    10 * time.Minute,
		Labels:         map[string]string{"trpc-agent-go.smoke": "mini-v2.1-parity"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	env, err := factory.Start(ctx, instanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Minute)
		defer closeCancel()
		if err := env.Close(closeCtx); err != nil {
			t.Errorf("close environment: %v", err)
		}
	}()

	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("inspect", bashCall("inspect", "printf 'docker-observation\\n'")),
		assistantResponse("submit", bashCall("submit", "echo "+SubmissionMarker+" && printf 'diff --git a/a b/a\\n'")),
	}}
	result := (&MiniAgent{Model: modelImpl, Environment: env, StepLimit: 3}).Run(ctx, "deterministic smoke")
	if result.Info.ExitStatus != "Submitted" || result.Submission != "diff --git a/a b/a\n" {
		t.Fatalf("result = %#v, submission = %q", result.Info, result.Submission)
	}
	if result.LLMCalls != 2 || result.ToolCalls != 2 {
		t.Fatalf("calls = llm %d tool %d", result.LLMCalls, result.ToolCalls)
	}
	if len(modelImpl.requests) != 2 || len(modelImpl.requests[1].Messages) != 4 {
		t.Fatalf("second request = %#v", modelImpl.requests)
	}
}
