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
	"errors"
	"io"
	"net/http"
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
		Done:   true,
		Object: model.ObjectTypeChatCompletion,
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
	var progress []ProgressUpdate
	executor := Executor{
		Factory:     fakeFactory{env: env},
		ModelConfig: modelconfig.EnvConfig{"MODEL_NAME": "mock-model"},
		CaseTimeout: time.Minute,
		ModelFactory: func(modelconfig.EnvConfig) model.Model {
			return submissionModel{}
		},
		Progress: func(update ProgressUpdate) {
			progress = append(progress, update)
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
	if result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("llm calls = %d, tool calls = %d", result.LLMCalls, result.ToolCalls)
	}
	if len(progress) == 0 || progress[len(progress)-1].Phase != "finished" || progress[len(progress)-1].ExitStatus != "Submitted" {
		t.Fatalf("progress = %#v", progress)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type contextAwareBody struct {
	ctx  context.Context
	done bool
}

func (b *contextAwareBody) Read(p []byte) (int, error) {
	if err := b.ctx.Err(); err != nil {
		return 0, err
	}
	if b.done {
		return 0, io.EOF
	}
	b.done = true
	return copy(p, "ok"), nil
}

func (*contextAwareBody) Close() error { return nil }

func TestTimeoutTransportKeepsContextUntilResponseBodyCloses(t *testing.T) {
	var body *contextAwareBody
	transport := timeoutTransport{
		timeout: time.Minute,
		base: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
			body = &contextAwareBody{ctx: request.Context()}
			return &http.Response{StatusCode: http.StatusOK, Body: body, Header: make(http.Header)}, nil
		}),
	}
	request, err := http.NewRequest(http.MethodGet, "http://example.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read response body: %v", err)
	}
	if string(data) != "ok" {
		t.Fatalf("body = %q", data)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(body.ctx.Err(), context.Canceled) {
		t.Fatalf("body context error = %v, want canceled after Close", body.ctx.Err())
	}
}
