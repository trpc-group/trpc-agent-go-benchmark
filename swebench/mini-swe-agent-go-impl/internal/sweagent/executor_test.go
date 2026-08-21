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
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	environment "trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type fakeFactory struct{ env environment.Environment }

func (f fakeFactory) Start(context.Context, string) (environment.Environment, error) {
	return f.env, nil
}

func TestExecutorOpenAIAdapterSendsUpstreamToolRequest(t *testing.T) {
	requests := make(chan map[string]any, 1)
	requestLabels := make(chan string, 1)
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requestLabels <- request.Header.Get("X-Benchmark-Run")
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			return nil, err
		}
		requests <- body
		response := `{
			"id":"chatcmpl-1","object":"chat.completion","created":1,"model":"mock",
			"choices":[{"index":0,"message":{"role":"assistant","content":"done","tool_calls":[{"id":"submit-1","type":"function","function":{"name":"bash","arguments":"{\"command\":\"submit\"}"}}]},"finish_reason":"tool_calls"}]
		}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(response)),
			Request:    request,
		}, nil
	})

	patch := "diff --git a/a b/a\n"
	env := &fakeEnvironment{results: []environment.CommandResult{{Output: SubmissionMarker + "\n" + patch}}}
	executor := Executor{
		Factory: fakeFactory{env: env},
		ModelConfig: modelconfig.EnvConfig{
			"MODEL_NAME":      "mock",
			"OPENAI_BASE_URL": "http://example.test/v1",
			"OPENAI_API_KEY":  "test-key",
			modelconfig.HTTPHeaderPrefix + "X-Benchmark-Run": "codec-json-e1",
		},
		CaseTimeout: time.Minute,
		ModelFactory: func(config modelconfig.EnvConfig) model.Model {
			return newModelWithTransport(config, transport)
		},
	}
	result := executor.Execute(context.Background(), contract.Case{InstanceID: "repo__repo-1", ProblemStatement: "fix it"})
	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != patch {
		t.Fatalf("result = %#v", result)
	}
	body := <-requests
	if got := <-requestLabels; got != "codec-json-e1" {
		t.Fatalf("X-Benchmark-Run = %q", got)
	}
	if body["parallel_tool_calls"] != true || body["temperature"] != float64(0) {
		t.Fatalf("request settings = %#v", body)
	}
	tools, ok := body["tools"].([]any)
	if !ok || len(tools) != 1 {
		t.Fatalf("tools = %#v", body["tools"])
	}
	function := tools[0].(map[string]any)["function"].(map[string]any)
	if function["name"] != "bash" {
		t.Fatalf("tool function = %#v", function)
	}
	messages := body["messages"].([]any)
	if messages[0].(map[string]any)["content"] != SystemPrompt || messages[1].(map[string]any)["content"] != PromptForTask("fix it") {
		t.Fatalf("messages do not match upstream prompt")
	}
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

func TestExecutorRunsMiniGoAgentWithMockModelAndFakeEnvironment(t *testing.T) {
	patch := "diff --git a/a b/a\n--- a/a\n+++ b/a\n+fixed\n"
	env := &fakeEnvironment{results: []environment.CommandResult{{Output: SubmissionMarker + "\n" + patch}}}
	var progress []ProgressUpdate
	executor := Executor{
		Factory:                 fakeFactory{env: env},
		ModelConfig:             modelconfig.EnvConfig{"MODEL_NAME": "mock-model"},
		RunID:                   "run-1",
		SourceRevision:          "revision-1",
		SourceModified:          true,
		BinarySHA256:            "binary-hash",
		ModelConfigHash:         "model-hash",
		EnvironmentConfigSHA256: "environment-hash",
		CasesHash:               "cases-hash",
		CommandTimeout:          30 * time.Second,
		CaseTimeout:             time.Minute,
		SelectedInstancesSHA256: "selection-hash",
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
	if result.Info.Submission != patch {
		t.Fatalf("submission = %q", result.Info.Submission)
	}
	if result.ModelPatch != patch {
		t.Fatalf("patch = %q", result.ModelPatch)
	}
	if len(result.TRPCResponses) == 0 {
		t.Fatal("expected raw tRPC model responses")
	}
	if result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("llm calls = %d, tool calls = %d", result.LLMCalls, result.ToolCalls)
	}
	if len(progress) == 0 || progress[len(progress)-1].Phase != "finished" || progress[len(progress)-1].ExitStatus != "Submitted" {
		t.Fatalf("progress = %#v", progress)
	}
	info := result.Info
	if info.RunID != "run-1" || info.SourceRevision != "revision-1" || !info.SourceModified ||
		info.BinarySHA256 != "binary-hash" || info.ModelConfigHash != "model-hash" ||
		info.EnvironmentConfigSHA256 != "environment-hash" || info.CasesHash != "cases-hash" ||
		info.CommandTimeout != "30s" || info.CaseTimeout != "1m0s" ||
		info.SelectedInstancesSHA256 != "selection-hash" {
		t.Fatalf("provenance = %#v", info)
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
