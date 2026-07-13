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
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/environment"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type fakeFactory struct{ env environment.Environment }

func (f fakeFactory) Start(context.Context, string) (environment.Environment, error) {
	return f.env, nil
}

type submissionModel struct{}

func (submissionModel) Info() model.Info { return model.Info{Name: "mock-model"} }

func (submissionModel) GenerateContent(_ context.Context, _ *model.Request) (<-chan *model.Response, error) {
	finish := "tool_calls"
	responses := make(chan *model.Response, 1)
	responses <- &model.Response{
		Done: true,
		Choices: []model.Choice{{
			FinishReason: &finish,
			Message: model.Message{
				Role: model.RoleAssistant,
				ToolCalls: []model.ToolCall{{
					Type: "function",
					ID:   "submit-1",
					Function: model.FunctionDefinitionParam{
						Name:      "bash",
						Arguments: []byte(`{"command":"COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT\ndone"}`),
					},
				}},
			},
		}},
	}
	close(responses)
	return responses, nil
}

func TestExecutorRunsTRPCAgentWithMockModelAndFakeEnvironment(t *testing.T) {
	env := &fakeEnvironment{patch: "diff --git a/a b/a\n--- a/a\n+++ b/a\n+fixed\n"}
	executor := Executor{
		Factory:     fakeFactory{env: env},
		ModelConfig: modelconfig.EnvConfig{"MODEL_NAME": "mock-model"},
		CaseTimeout: time.Minute,
		ModelFactory: func(modelconfig.EnvConfig) model.Model {
			return submissionModel{}
		},
	}
	result := executor.Execute(context.Background(), contract.Case{InstanceID: "repo__repo-1", ProblemStatement: "fix it"})
	if result.Info.ExitStatus != "Submitted" {
		t.Fatalf("exit status = %q, error = %q", result.Info.ExitStatus, result.Info.Error)
	}
	if result.Info.Submission != "done" {
		t.Fatalf("submission = %q", result.Info.Submission)
	}
	if result.ModelPatch != env.patch {
		t.Fatalf("patch = %q", result.ModelPatch)
	}
	if len(result.Events) == 0 {
		t.Fatal("expected tRPC runner events")
	}
}
