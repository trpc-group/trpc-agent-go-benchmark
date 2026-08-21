//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package executor

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type scriptedModel struct {
	mu        sync.Mutex
	responses []*model.Response
	requests  []*model.Request
	err       error
}

func TestScrubCaseResultSensitiveRemovesEmbeddingSecrets(t *testing.T) {
	config := &embeddingconfig.Config{}
	config.Embedding.APIBase = "https://private.example.invalid/v1"
	config.Embedding.APIKey = "secret-key"
	config.Embedding.ExtraHeaders = map[string]string{"X-Tenant": "private-tenant"}
	config.Cache.Directory = "/private/cache"
	message := "POST https://private.example.invalid/v1/embeddings secret-key private-tenant /private/cache"
	raw, err := json.Marshal(map[string]string{"error": message})
	if err != nil {
		t.Fatal(err)
	}
	response := &model.Response{
		Done:    true,
		Error:   &model.ResponseError{Message: message},
		Choices: []model.Choice{{Message: model.Message{Content: message}}},
	}
	result := CaseResult{
		Info:                  CaseInfo{Error: message},
		CodeSearchResultBytes: len(raw),
		CodeSearchRawResults:  []json.RawMessage{raw},
		RetrievalTrace: []tagagent.RetrievalTraceEntry{{
			Status:       "error",
			Error:        message,
			ErrorSHA256:  tagagent.DigestBytes([]byte(message)),
			ResultBytes:  len(raw),
			ResultSHA256: tagagent.DigestBytes(raw),
		}},
		Responses: []*model.Response{response.Clone()},
		Events:    []*event.Event{{Response: response.Clone()}},
	}
	scrubCaseResultSensitive(&result, config)
	serialized, err := json.Marshal(struct {
		Result    CaseResult        `json:"result"`
		Responses []*model.Response `json:"responses"`
	}{Result: result, Responses: result.Responses})
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		config.Embedding.APIBase,
		config.Embedding.APIKey,
		config.Embedding.ExtraHeaders["X-Tenant"],
		config.Cache.Directory,
	} {
		if strings.Contains(string(serialized), forbidden) {
			t.Fatalf("portable case artifact contains %q: %s", forbidden, serialized)
		}
	}
	if result.CodeSearchResultBytes != len(result.CodeSearchRawResults[0]) ||
		result.RetrievalTrace[0].ResultBytes != len(result.CodeSearchRawResults[0]) ||
		result.RetrievalTrace[0].ResultSHA256 != tagagent.DigestBytes(result.CodeSearchRawResults[0]) ||
		result.RetrievalTrace[0].ErrorSHA256 != tagagent.DigestBytes([]byte(result.RetrievalTrace[0].Error)) {
		t.Fatalf("scrubbed retrieval evidence is inconsistent: %#v", result)
	}
}

func (*scriptedModel) Info() model.Info { return model.Info{Name: "scripted"} }

func (m *scriptedModel) GenerateContent(_ context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	copyRequest := *request
	copyRequest.Messages = append([]model.Message(nil), request.Messages...)
	m.requests = append(m.requests, &copyRequest)
	if m.err != nil {
		return nil, m.err
	}
	responses := make(chan *model.Response, 1)
	if len(m.responses) > 0 {
		responses <- m.responses[0]
		m.responses = m.responses[1:]
	}
	close(responses)
	return responses, nil
}

type fakeFactory struct {
	environment sweenv.Environment
	err         error
	start       func(context.Context, sweenv.CaseSpec) (sweenv.Environment, error)
}

func (f fakeFactory) StartCase(ctx context.Context, spec sweenv.CaseSpec) (sweenv.Environment, error) {
	if f.start != nil {
		return f.start(ctx, spec)
	}
	return f.environment, f.err
}

type fakeEnvironment struct {
	mu       sync.Mutex
	results  []sweenv.CommandResult
	commands []string
	closed   bool
	closeErr error
}

type provenanceEnvironment struct {
	*fakeEnvironment
	provenance sweenv.Provenance
}

func (e provenanceEnvironment) Provenance() sweenv.Provenance { return e.provenance }

type snapshotEnvironment struct{ *fakeEnvironment }

func (e snapshotEnvironment) SnapshotWorkspace(_ context.Context, destination string) error {
	return os.WriteFile(
		filepath.Join(destination, "users.py"),
		[]byte("def find_user_by_email(email):\n    return email\n"),
		0o600,
	)
}

func (e *fakeEnvironment) Execute(_ context.Context, command string) sweenv.CommandResult {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.commands = append(e.commands, command)
	if len(e.results) == 0 {
		return sweenv.CommandResult{}
	}
	result := e.results[0]
	e.results = e.results[1:]
	return result
}

func (e *fakeEnvironment) Close(context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	return e.closeErr
}

func assistantResponse(content string, calls ...model.ToolCall) *model.Response {
	usage := &model.Usage{PromptTokens: 10, CompletionTokens: 2, TotalTokens: 12}
	return &model.Response{
		Done: true, Object: model.ObjectTypeChatCompletion, Usage: usage,
		Choices: []model.Choice{{Message: model.Message{
			Role: model.RoleAssistant, Content: content, ToolCalls: calls,
		}}},
	}
}

func bashCall(id, command string) model.ToolCall {
	arguments, _ := json.Marshal(map[string]string{"command": command})
	return model.ToolCall{
		ID: id, Type: "function",
		Function: model.FunctionDefinitionParam{Name: "bash", Arguments: arguments},
	}
}

func codeSearchCall(id, query string) model.ToolCall {
	arguments, _ := json.Marshal(map[string]string{"query": query})
	return model.ToolCall{
		ID: id, Type: "function",
		Function: model.FunctionDefinitionParam{Name: "code_search", Arguments: arguments},
	}
}

func newTestExecutor(modelImpl model.Model, environment *fakeEnvironment, codec observation.ObservationCodec) Executor {
	return Executor{
		Factory: fakeFactory{environment: environment}, ObservationCodec: codec,
		ModelFactory: func(modelconfig.EnvConfig) model.Model { return modelImpl },
		Workers:      1,
	}
}

func TestFormatErrorReplacementContinuesWithoutPersistingInvalidAssistant(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("not a tool call"),
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\ndiff --git a/a b/a\n",
	}}}
	result := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "diff --git a/a b/a\n" {
		t.Fatalf("result = %+v", result)
	}
	if result.LLMCalls != 2 || result.ToolCalls != 1 || len(modelImpl.requests) != 2 {
		t.Fatalf("calls = llm %d, tool %d, requests %d", result.LLMCalls, result.ToolCalls, len(modelImpl.requests))
	}
	messages := modelImpl.requests[1].Messages
	if len(messages) != 3 {
		t.Fatalf("second request messages = %#v", messages)
	}
	if got := []model.Role{messages[0].Role, messages[1].Role, messages[2].Role}; !reflect.DeepEqual(got, []model.Role{model.RoleSystem, model.RoleUser, model.RoleUser}) {
		t.Fatalf("roles = %v", got)
	}
	if messages[2].Content == "" || messages[2].Content == "not a tool call" {
		t.Fatalf("replacement = %#v", messages[2])
	}
	for _, message := range messages {
		if message.Role == model.RoleAssistant && message.Content == "not a tool call" {
			t.Fatal("invalid assistant response entered the next request")
		}
	}
}

func TestToolResultsUseSelectedCodecAndExecuteSequentially(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("inspect", bashCall("one", "pwd"), bashCall("two", "git status --short")),
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{
		{Output: "/testbed\n"},
		{Output: " M a.go\n"},
		{Output: protocol.SubmissionMarker + "\npatch\n"},
	}}
	result := newTestExecutor(modelImpl, environment, observation.ObservationCodecJSON).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.LLMCalls != 2 || result.ToolCalls != 3 {
		t.Fatalf("result = %+v", result)
	}
	wantCommands := []string{"pwd", "git status --short", "submit"}
	if !reflect.DeepEqual(environment.commands, wantCommands) {
		t.Fatalf("commands = %#v, want %#v", environment.commands, wantCommands)
	}
	if len(modelImpl.requests) != 2 {
		t.Fatalf("requests = %d", len(modelImpl.requests))
	}
	request := modelImpl.requests[1]
	if request.ExtraFields["parallel_tool_calls"] != true {
		t.Fatalf("extra fields = %#v", request.ExtraFields)
	}
	var toolMessages []model.Message
	for _, message := range request.Messages {
		if message.Role == model.RoleTool {
			toolMessages = append(toolMessages, message)
		}
	}
	wantObservations := []string{
		`{"returncode":0,"output":"/testbed\n"}`,
		`{"returncode":0,"output":" M a.go\n"}`,
	}
	if len(toolMessages) != 2 || toolMessages[0].Content != wantObservations[0] || toolMessages[1].Content != wantObservations[1] {
		t.Fatalf("tool messages = %#v", toolMessages)
	}
	if result.Usage.PromptTokens != 20 || result.Usage.CompletionTokens != 4 || result.Usage.TotalTokens != 24 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}

func TestOptionalCodeSearchBuildsStaticIndexAndProjectsTelemetry(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("search", codeSearchCall("search-1", "find_user_by_email")),
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := snapshotEnvironment{fakeEnvironment: &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\npatch\n",
	}}}}
	preload := false
	exec := Executor{
		Factory:                 fakeFactory{environment: environment},
		ObservationCodec:        observation.ObservationCodecXML,
		ModelFactory:            func(modelconfig.EnvConfig) model.Model { return modelImpl },
		Workers:                 1,
		EnableCodeSearch:        true,
		EnableWorkspacePreload:  preload,
		WorkspaceRepresentation: tagagent.WorkspaceRepresentationCurrentFixed,
	}
	result := exec.Execute(
		context.Background(),
		contract.Case{InstanceID: "case-a", Repo: "owner/project", ProblemStatement: "fix lookup"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" ||
		!result.Info.CodeSearch || result.Info.WorkspacePreload == nil ||
		*result.Info.WorkspacePreload != preload {
		t.Fatalf("result identity = %+v", result)
	}
	if result.LLMCalls != 2 || result.ToolCalls != 2 || result.CodeSearchCalls != 1 ||
		result.CodeSearchResultBytes <= 0 || result.CodeSearchObservationBytes <= 0 {
		t.Fatalf("retrieval telemetry = %+v", result)
	}
	if result.WorkspaceIndex == nil || result.WorkspaceIndex.Documents == 0 ||
		result.WorkspaceIndex.RetrievalMode != "keyword" || result.WorkspaceIndex.PreloadInjected {
		t.Fatalf("workspace index = %+v", result.WorkspaceIndex)
	}
	if len(result.CodeSearchRawResults) != 1 || len(result.RetrievalTrace) != 1 ||
		result.RetrievalTrace[0].Query != "find_user_by_email" {
		t.Fatalf("retrieval artifacts = raw=%d trace=%+v", len(result.CodeSearchRawResults), result.RetrievalTrace)
	}
	if got := environment.commands; !reflect.DeepEqual(got, []string{"submit"}) {
		t.Fatalf("bash commands = %#v, want submit only", got)
	}
	if len(modelImpl.requests) != 2 || !strings.Contains(
		modelImpl.requests[1].Messages[len(modelImpl.requests[1].Messages)-1].Content,
		"find_user_by_email",
	) {
		t.Fatalf("second model request did not receive code_search result: %#v", modelImpl.requests)
	}
}

func TestSubmissionSkipSummarizationDoesNotCallModelAgain(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
		assistantResponse("must not be requested"),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\npatch\n",
	}}}
	result := newTestExecutor(modelImpl, environment, observation.ObservationCodecText).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" {
		t.Fatalf("result = %+v", result)
	}
	if len(modelImpl.requests) != 1 || result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("calls = requests %d, llm %d, tool %d", len(modelImpl.requests), result.LLMCalls, result.ToolCalls)
	}
	if !environment.closed {
		t.Fatal("environment was not closed")
	}
}

func TestSubmissionStopsRemainingToolCallsInSameResponse(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse(
			"done",
			bashCall("submit", "submit"),
			bashCall("must-not-run", "touch /tmp/must-not-run"),
		),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\npatch\n",
	}}}
	result := newTestExecutor(modelImpl, environment, observation.ObservationCodecText).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" {
		t.Fatalf("result = %+v", result)
	}
	if want := []string{"submit"}; !reflect.DeepEqual(environment.commands, want) {
		t.Fatalf("commands = %#v, want %#v", environment.commands, want)
	}
	if result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("calls = llm %d, tool %d", result.LLMCalls, result.ToolCalls)
	}
}

func TestEmptyPatchSubmissionIsSubmitted(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\n",
	}}}
	result := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "" {
		t.Fatalf("result = %+v", result)
	}
	if result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("calls = llm %d, tool %d", result.LLMCalls, result.ToolCalls)
	}
}

func TestMaxLLMCallsStopsAfterExactly250ModelRequests(t *testing.T) {
	responses := make([]*model.Response, maxLLMCallsForTest)
	for i := range responses {
		responses[i] = assistantResponse("missing tool call")
	}
	modelImpl := &scriptedModel{responses: responses}
	result := newTestExecutor(modelImpl, &fakeEnvironment{}, observation.ObservationCodecXML).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "LimitsExceeded" || result.Info.ErrorCategory != protocol.ErrorCategoryAgentLimit {
		t.Fatalf("result = %+v", result)
	}
	if result.LLMCalls != maxLLMCallsForTest || len(modelImpl.requests) != maxLLMCallsForTest {
		t.Fatalf("calls = llm %d, requests %d, want %d", result.LLMCalls, len(modelImpl.requests), maxLLMCallsForTest)
	}
	if !strings.Contains(result.Info.Error, "max LLM calls (250) exceeded") {
		t.Fatalf("error = %q", result.Info.Error)
	}
}

func TestEmptyAndNilModelResponseChannelsAreImmediateErrors(t *testing.T) {
	for _, test := range []struct {
		name  string
		model model.Model
	}{
		{name: "empty", model: &scriptedModel{}},
		{name: "nil", model: nilChannelModel{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := newTestExecutor(test.model, &fakeEnvironment{}, observation.ObservationCodecXML).Execute(
				context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
			)
			if result.Info.ExitStatus != "Error" || result.Info.ErrorCategory != protocol.ErrorCategoryAgent {
				t.Fatalf("result = %+v", result)
			}
			if !strings.Contains(result.Info.Error, "response channel") {
				t.Fatalf("error = %q", result.Info.Error)
			}
		})
	}
}

func TestNilResponseBeforeValidResponseIsIgnored(t *testing.T) {
	modelImpl := nilThenValidModel{response: assistantResponse("done", bashCall("submit", "submit"))}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\npatch\n",
	}}}
	result := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)
	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" {
		t.Fatalf("result = %+v", result)
	}
}

func TestDoneResponseDoesNotWaitForSourceChannelClose(t *testing.T) {
	modelImpl := doneOpenModel{response: assistantResponse("done", bashCall("submit", "submit"))}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: protocol.SubmissionMarker + "\npatch\n",
	}}}
	exec := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML)
	exec.CaseTimeout = time.Second
	result := exec.Execute(context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"})
	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" {
		t.Fatalf("result = %+v", result)
	}
}

func TestEnvironmentAndEndpointFailuresAreClassified(t *testing.T) {
	t.Run("environment", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			err       error
			cleanRoom bool
			retryable bool
		}{
			{
				name:      "online preserves historical retry",
				err:       errors.New("docker unavailable"),
				retryable: true,
			},
			{
				name:      "clean-room unmarked deterministic",
				err:       errors.New("invalid clean-room policy"),
				cleanRoom: true,
			},
			{
				name:      "clean-room explicit transient",
				err:       sweenv.MarkStartErrorRetryable(errors.New("docker unavailable")),
				cleanRoom: true,
				retryable: true,
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				result := Executor{
					Factory:      fakeFactory{err: tc.err},
					ModelFactory: func(modelconfig.EnvConfig) model.Model { return &scriptedModel{} },
					CleanRoom:    tc.cleanRoom,
				}.Execute(context.Background(), contract.Case{InstanceID: "case-a"})
				if result.Info.ErrorCategory != protocol.ErrorCategoryEnvironment ||
					result.Info.Retryable != tc.retryable {
					t.Fatalf("result = %+v", result)
				}
			})
		}
	})
	t.Run("endpoint", func(t *testing.T) {
		modelImpl := &scriptedModel{err: errors.New("POST /chat/completions: context deadline exceeded")}
		environment := &fakeEnvironment{}
		result := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML).Execute(
			context.Background(), contract.Case{InstanceID: "case-a"},
		)
		if result.Info.ErrorCategory != protocol.ErrorCategoryEndpointTimeout || !result.Info.Retryable {
			t.Fatalf("result = %+v", result)
		}
		if result.LLMCalls != 1 {
			t.Fatalf("llm calls = %d, want 1", result.LLMCalls)
		}
	})
	t.Run("cleanup", func(t *testing.T) {
		modelImpl := &scriptedModel{responses: []*model.Response{
			assistantResponse("done", bashCall("submit", "submit")),
		}}
		environment := &fakeEnvironment{
			results:  []sweenv.CommandResult{{Output: protocol.SubmissionMarker + "\npatch\n"}},
			closeErr: errors.New("remove container failed"),
		}
		result := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML).Execute(
			context.Background(), contract.Case{InstanceID: "case-a"},
		)
		if result.Info.ExitStatus != "Error" || result.Info.ErrorCategory != protocol.ErrorCategoryEnvironment || !result.Info.Retryable {
			t.Fatalf("result = %+v", result)
		}
	})
}

func TestCaseIdentityAndCleanRoomProvenanceReachCaseArtifact(t *testing.T) {
	selectedCase := contract.Case{
		InstanceID:       "org__repo-123",
		Repo:             "org/repo",
		BaseCommit:       strings.Repeat("1", 40),
		ProblemStatement: "fix it",
	}
	wantSpec := sweenv.CaseSpec{
		InstanceID: selectedCase.InstanceID,
		Repo:       selectedCase.Repo,
		BaseCommit: selectedCase.BaseCommit,
	}
	wantProvenance := sweenv.Provenance{Testbed: sweenv.ImageIdentity{
		Reference: sweenv.ImageForInstance(selectedCase.InstanceID),
		ID:        "sha256:" + strings.Repeat("2", 64),
	}}
	dockerImages := map[string]sweenv.ImageIdentity{
		wantProvenance.Testbed.Reference: wantProvenance.Testbed,
	}
	environment := provenanceEnvironment{
		fakeEnvironment: &fakeEnvironment{results: []sweenv.CommandResult{{
			Output: protocol.SubmissionMarker + "\npatch\n",
		}}},
		provenance: wantProvenance,
	}
	var gotSpec sweenv.CaseSpec
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	exec := Executor{
		Factory: fakeFactory{start: func(_ context.Context, spec sweenv.CaseSpec) (sweenv.Environment, error) {
			gotSpec = spec
			return environment, nil
		}},
		ObservationCodec:      observation.ObservationCodecXML,
		ModelFactory:          func(modelconfig.EnvConfig) model.Model { return modelImpl },
		CleanRoom:             true,
		CleanRoomPolicySHA256: strings.Repeat("3", 64),
		OfflineAssetsSHA256:   strings.Repeat("4", 64),
		ImageSetSHA256:        strings.Repeat("5", 64),
		DockerImages:          dockerImages,
		Workers:               1,
	}
	result := exec.Execute(context.Background(), selectedCase)

	if !reflect.DeepEqual(gotSpec, wantSpec) {
		t.Fatalf("case spec = %#v, want %#v", gotSpec, wantSpec)
	}
	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" {
		t.Fatalf("result = %+v", result)
	}
	if !result.Info.CleanRoom || result.Info.CleanRoomPolicySHA256 != strings.Repeat("3", 64) ||
		result.Info.OfflineAssetsSHA256 != strings.Repeat("4", 64) ||
		result.Info.ImageSetSHA256 != strings.Repeat("5", 64) {
		t.Fatalf("clean-room identity = %+v", result.Info)
	}
	if result.Info.Repo != selectedCase.Repo || result.Info.BaseCommit != selectedCase.BaseCommit ||
		result.Info.VerifiedBaseCommit != selectedCase.BaseCommit {
		t.Fatalf("case identity = %+v", result.Info)
	}
	if result.Info.EnvironmentProvenance == nil ||
		!reflect.DeepEqual(*result.Info.EnvironmentProvenance, wantProvenance) {
		t.Fatalf("provenance = %#v, want %#v", result.Info.EnvironmentProvenance, wantProvenance)
	}
}

func TestEnvironmentFailureDoesNotConstructModel(t *testing.T) {
	modelFactoryCalls := 0
	result := Executor{
		Factory: fakeFactory{start: func(_ context.Context, spec sweenv.CaseSpec) (sweenv.Environment, error) {
			if spec.InstanceID != "case-a" {
				t.Fatalf("case spec = %#v", spec)
			}
			return nil, sweenv.MarkStartErrorRetryable(errors.New("clean-room setup failed"))
		}},
		ModelFactory: func(modelconfig.EnvConfig) model.Model {
			modelFactoryCalls++
			return &scriptedModel{}
		},
		CleanRoom:       true,
		ToolLoopWarning: true,
	}.Execute(context.Background(), contract.Case{InstanceID: "case-a"})

	if modelFactoryCalls != 0 {
		t.Fatalf("model factory calls = %d, want 0", modelFactoryCalls)
	}
	if result.Info.ExitStatus != "Error" || result.Info.ErrorCategory != protocol.ErrorCategoryEnvironment ||
		!result.Info.Retryable {
		t.Fatalf("result = %+v", result)
	}
	if !result.IsRetryableCleanRoomPreStartFailure() {
		t.Fatalf("result was not recognized as a retryable clean-room pre-start failure: %+v", result)
	}
	if result.Info.VerifiedBaseCommit != "" || result.Info.EnvironmentProvenance != nil {
		t.Fatalf("pre-start failure contains success-only attestations: %+v", result.Info)
	}
	if result.ToolLoopWarningLLMCalls == nil || len(result.ToolLoopWarningLLMCalls) != 0 {
		t.Fatalf("pre-start warning calls = %#v, want explicit empty array", result.ToolLoopWarningLLMCalls)
	}
}

func TestRetryableCleanRoomPreStartFailureRequiresExactShape(t *testing.T) {
	base := CaseResult{
		InstanceID: "case-a",
		Info: CaseInfo{
			CleanRoom:     true,
			ExitStatus:    "Error",
			Error:         "clean-room setup failed",
			ErrorCategory: protocol.ErrorCategoryEnvironment,
			Retryable:     true,
		},
	}
	if !base.IsRetryableCleanRoomPreStartFailure() {
		t.Fatal("exact pre-start failure was not recognized")
	}

	tests := []struct {
		name   string
		change func(*CaseResult)
	}{
		{name: "non-retryable", change: func(result *CaseResult) { result.Info.Retryable = false }},
		{name: "wrong category", change: func(result *CaseResult) { result.Info.ErrorCategory = protocol.ErrorCategoryAgent }},
		{name: "verified base", change: func(result *CaseResult) { result.Info.VerifiedBaseCommit = "base" }},
		{name: "provenance", change: func(result *CaseResult) { result.Info.EnvironmentProvenance = &sweenv.Provenance{} }},
		{name: "model call", change: func(result *CaseResult) { result.LLMCalls = 1 }},
		{name: "tool call", change: func(result *CaseResult) { result.ToolCalls = 1 }},
		{name: "warning count", change: func(result *CaseResult) { result.ToolLoopWarningCount = 1 }},
		{name: "warning first call", change: func(result *CaseResult) {
			first := 1
			result.FirstToolLoopWarningLLMCall = &first
		}},
		{name: "warning calls", change: func(result *CaseResult) { result.ToolLoopWarningLLMCalls = []int{1} }},
		{name: "response", change: func(result *CaseResult) { result.Responses = []*model.Response{{Done: true}} }},
		{name: "event", change: func(result *CaseResult) { result.Events = []*event.Event{{}} }},
		{name: "patch", change: func(result *CaseResult) { result.ModelPatch = "patch" }},
		{name: "prompt usage", change: func(result *CaseResult) { result.Usage.PromptTokens = 1 }},
		{name: "cached usage", change: func(result *CaseResult) { result.Usage.PromptTokensDetails.CachedTokens = 1 }},
		{name: "completion usage", change: func(result *CaseResult) { result.Usage.CompletionTokens = 1 }},
		{name: "timing usage", change: func(result *CaseResult) {
			result.Usage.TimingInfo = &model.TimingInfo{FirstTokenDuration: time.Nanosecond}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			result := base
			tc.change(&result)
			if result.IsRetryableCleanRoomPreStartFailure() {
				t.Fatalf("non-pre-start result was accepted: %+v", result)
			}
		})
	}
}

func TestApplySnapshotProjectsToolLoopWarningTelemetry(t *testing.T) {
	result := CaseResult{}
	applySnapshot(&result, tagagent.Snapshot{
		LLMCalls:                    7,
		ToolLoopWarningCount:        2,
		FirstToolLoopWarningLLMCall: 3,
		ToolLoopWarningLLMCalls:     []int{3, 7},
	})
	if result.ToolLoopWarningCount != 2 || result.FirstToolLoopWarningLLMCall == nil ||
		*result.FirstToolLoopWarningLLMCall != 3 ||
		!reflect.DeepEqual(result.ToolLoopWarningLLMCalls, []int{3, 7}) {
		t.Fatalf("warning telemetry = count %d first %v calls %#v",
			result.ToolLoopWarningCount,
			result.FirstToolLoopWarningLLMCall,
			result.ToolLoopWarningLLMCalls,
		)
	}

	result = CaseResult{FirstToolLoopWarningLLMCall: new(int), ToolLoopWarningLLMCalls: []int{1}}
	applySnapshot(&result, tagagent.Snapshot{})
	if result.ToolLoopWarningCount != 0 || result.FirstToolLoopWarningLLMCall != nil ||
		result.ToolLoopWarningLLMCalls == nil || len(result.ToolLoopWarningLLMCalls) != 0 {
		t.Fatalf("zero warning telemetry = %+v", result)
	}
}

func TestDefaultModePreservesStartupBoundaryAndOmitsCleanRoomProvenance(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := provenanceEnvironment{
		fakeEnvironment: &fakeEnvironment{results: []sweenv.CommandResult{{
			Output: protocol.SubmissionMarker + "\npatch\n",
		}}},
		provenance: sweenv.Provenance{Testbed: sweenv.ImageIdentity{
			Reference: "must-not-be-recorded",
			ID:        "sha256:" + strings.Repeat("1", 64),
		}},
	}
	result := Executor{
		Factory: fakeFactory{start: func(ctx context.Context, _ sweenv.CaseSpec) (sweenv.Environment, error) {
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("default-mode environment startup unexpectedly consumed the case timeout")
			}
			return environment, nil
		}},
		ObservationCodec: observation.ObservationCodecXML,
		ModelFactory:     func(modelconfig.EnvConfig) model.Model { return modelImpl },
		Workers:          1,
		CaseTimeout:      time.Second,
	}.Execute(context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"})

	if result.Info.ExitStatus != "Submitted" {
		t.Fatalf("result = %+v", result)
	}
	if result.Info.EnvironmentProvenance != nil || result.Info.VerifiedBaseCommit != "" {
		t.Fatalf("default-mode clean-room provenance = %+v", result.Info)
	}
}

func TestCleanRoomRequiresImageProvenanceBeforeConstructingModel(t *testing.T) {
	tests := []struct {
		name        string
		environment sweenv.Environment
	}{
		{name: "missing provider", environment: &fakeEnvironment{}},
		{name: "empty identity", environment: provenanceEnvironment{fakeEnvironment: &fakeEnvironment{}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			modelFactoryCalls := 0
			result := Executor{
				Factory:   fakeFactory{environment: tc.environment},
				CleanRoom: true,
				ModelFactory: func(modelconfig.EnvConfig) model.Model {
					modelFactoryCalls++
					return &scriptedModel{}
				},
			}.Execute(context.Background(), contract.Case{
				InstanceID: "case-a",
				Repo:       "org/repo",
				BaseCommit: strings.Repeat("1", 40),
			})

			if modelFactoryCalls != 0 {
				t.Fatalf("model factory calls = %d, want 0", modelFactoryCalls)
			}
			if result.Info.ExitStatus != "Error" || result.Info.ErrorCategory != protocol.ErrorCategoryEnvironment ||
				result.Info.Retryable || !strings.Contains(result.Info.Error, "attest") {
				t.Fatalf("result = %+v", result)
			}
			switch environment := tc.environment.(type) {
			case *fakeEnvironment:
				if !environment.closed {
					t.Fatal("environment was not closed")
				}
			case provenanceEnvironment:
				if !environment.closed {
					t.Fatal("environment was not closed")
				}
			}
		})
	}
}

func TestCleanRoomRequiresExactAuxiliaryImagesBeforeConstructingModel(t *testing.T) {
	instanceID := "psf__requests-2317"
	testbed := sweenv.ImageIdentity{
		Reference: sweenv.ImageForInstance(instanceID),
		ID:        "sha256:" + strings.Repeat("1", 64),
	}
	httpbin := sweenv.ImageIdentity{
		Reference: offlineHTTPBinImageReference,
		ID:        "sha256:" + strings.Repeat("2", 64),
	}
	images := map[string]sweenv.ImageIdentity{
		testbed.Reference: testbed,
		httpbin.Reference: httpbin,
	}
	tests := []struct {
		name      string
		auxiliary map[string]sweenv.ImageIdentity
		wantError string
	}{
		{
			name: "missing httpbin",
			auxiliary: map[string]sweenv.ImageIdentity{
				"network-helper": testbed,
			},
			wantError: "auxiliary image roles",
		},
		{
			name: "wrong network helper",
			auxiliary: map[string]sweenv.ImageIdentity{
				"httpbin":        httpbin,
				"network-helper": httpbin,
			},
			wantError: "network-helper image provenance",
		},
		{
			name: "extra role",
			auxiliary: map[string]sweenv.ImageIdentity{
				"httpbin":        httpbin,
				"network-helper": testbed,
				"unexpected":     testbed,
			},
			wantError: "auxiliary image roles",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			environment := provenanceEnvironment{
				fakeEnvironment: &fakeEnvironment{},
				provenance: sweenv.Provenance{
					Testbed:         testbed,
					AuxiliaryImages: tc.auxiliary,
				},
			}
			modelFactoryCalls := 0
			result := Executor{
				Factory:      fakeFactory{environment: environment},
				CleanRoom:    true,
				DockerImages: images,
				ModelFactory: func(modelconfig.EnvConfig) model.Model {
					modelFactoryCalls++
					return &scriptedModel{}
				},
			}.Execute(context.Background(), contract.Case{
				InstanceID: instanceID,
				Repo:       "psf/requests",
				BaseCommit: strings.Repeat("3", 40),
			})

			if modelFactoryCalls != 0 {
				t.Fatalf("model factory calls = %d, want 0", modelFactoryCalls)
			}
			if result.Info.ErrorCategory != protocol.ErrorCategoryEnvironment || result.Info.Retryable ||
				!strings.Contains(result.Info.Error, tc.wantError) {
				t.Fatalf("result = %+v", result)
			}
			if !environment.closed {
				t.Fatal("environment was not closed")
			}
		})
	}
}

func TestCaseTimeout(t *testing.T) {
	modelImpl := blockingModel{}
	environment := &fakeEnvironment{}
	exec := newTestExecutor(modelImpl, environment, observation.ObservationCodecXML)
	exec.CaseTimeout = time.Millisecond
	result := exec.Execute(context.Background(), contract.Case{InstanceID: "case-a"})
	if result.Info.ExitStatus != "Timeout" || result.Info.ErrorCategory != protocol.ErrorCategoryCaseTimeout {
		t.Fatalf("result = %+v", result)
	}
}

func TestSubmissionDetectedAfterCaseDeadlineIsTimeout(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	exec := Executor{
		Factory:          fakeFactory{environment: deadlineSubmissionEnvironment{}},
		ObservationCodec: observation.ObservationCodecXML,
		ModelFactory:     func(modelconfig.EnvConfig) model.Model { return modelImpl },
		Workers:          1,
		CaseTimeout:      time.Millisecond,
	}
	result := exec.Execute(context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"})
	if result.Info.ExitStatus != "Timeout" || result.Info.ErrorCategory != protocol.ErrorCategoryCaseTimeout {
		t.Fatalf("result = %+v", result)
	}
}

type blockingModel struct{}

type nilChannelModel struct{}

type nilThenValidModel struct{ response *model.Response }

type doneOpenModel struct{ response *model.Response }

type deadlineSubmissionEnvironment struct{}

const maxLLMCallsForTest = 250

func (blockingModel) Info() model.Info { return model.Info{Name: "blocking"} }

func (nilChannelModel) Info() model.Info { return model.Info{Name: "nil-channel"} }

func (nilThenValidModel) Info() model.Info { return model.Info{Name: "nil-then-valid"} }

func (doneOpenModel) Info() model.Info { return model.Info{Name: "done-open"} }

func (nilChannelModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	return nil, nil
}

func (m nilThenValidModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response, 2)
	responses <- nil
	responses <- m.response
	close(responses)
	return responses, nil
}

func (m doneOpenModel) GenerateContent(context.Context, *model.Request) (<-chan *model.Response, error) {
	responses := make(chan *model.Response, 1)
	responses <- m.response
	return responses, nil
}

func (deadlineSubmissionEnvironment) Execute(ctx context.Context, _ string) sweenv.CommandResult {
	<-ctx.Done()
	return sweenv.CommandResult{Output: protocol.SubmissionMarker + "\npatch\n"}
}

func (deadlineSubmissionEnvironment) Close(context.Context) error { return nil }

func (blockingModel) GenerateContent(ctx context.Context, _ *model.Request) (<-chan *model.Response, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
