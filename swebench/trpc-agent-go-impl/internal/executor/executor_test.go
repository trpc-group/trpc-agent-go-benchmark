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
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/minicompat"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

type scriptedModel struct {
	mu        sync.Mutex
	responses []*model.Response
	requests  []*model.Request
}

func (*scriptedModel) Info() model.Info { return model.Info{Name: "scripted"} }

func (m *scriptedModel) GenerateContent(_ context.Context, request *model.Request) (<-chan *model.Response, error) {
	m.mu.Lock()
	copyRequest := *request
	copyRequest.Messages = append([]model.Message(nil), request.Messages...)
	m.requests = append(m.requests, &copyRequest)
	var response *model.Response
	if len(m.responses) > 0 {
		response = m.responses[0]
		m.responses = m.responses[1:]
	}
	m.mu.Unlock()
	responses := make(chan *model.Response, 1)
	if response != nil {
		responses <- response
	}
	close(responses)
	return responses, nil
}

type fakeFactory struct{ environment *fakeEnvironment }

func (f fakeFactory) Start(context.Context, string) (sweenv.Environment, error) {
	return f.environment, nil
}

type fakeEnvironment struct {
	mu       sync.Mutex
	results  []sweenv.CommandResult
	commands []string
	closed   bool
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
	return nil
}

func (e *fakeEnvironment) SnapshotWorkspace(_ context.Context, destination string) error {
	return os.WriteFile(filepath.Join(destination, "users.py"), []byte(`
class UserStore:
    def find_user_by_email(self, email):
        return self.users.get(email)
`), 0o600)
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

func newTestExecutor(modelImpl model.Model, environment *fakeEnvironment, codec minicompat.ObservationCodec) Executor {
	return Executor{
		Factory: fakeFactory{environment: environment}, ObservationCodec: codec,
		ModelFactory: func(modelconfig.EnvConfig) model.Model { return modelImpl },
	}
}

func TestFormatErrorReplacementContinuesWithoutPersistingInvalidAssistant(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("not a tool call"),
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: minicompat.SubmissionMarker + "\ndiff --git a/a b/a\n",
	}}}
	result := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecXML).Execute(
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
		{Output: minicompat.SubmissionMarker + "\npatch\n"},
	}}
	result := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecJSON).Execute(
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

func TestSubmissionSkipSummarizationDoesNotCallModelAgain(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
		assistantResponse("must not be requested"),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: minicompat.SubmissionMarker + "\npatch\n",
	}}}
	result := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecText).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "patch\n" {
		t.Fatalf("result = %+v", result)
	}
	if len(modelImpl.requests) != 1 || result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("calls = requests %d, llm %d, tool %d", len(modelImpl.requests), result.LLMCalls, result.ToolCalls)
	}
	if modelImpl.requests[0].ToolOrder != nil {
		t.Fatalf("native tool order = %#v, want nil", modelImpl.requests[0].ToolOrder)
	}
	assertOfflineRequestPrompts(t, modelImpl.requests[0], false)
	if !environment.closed {
		t.Fatal("environment was not closed")
	}
}

func TestEmptyPatchSubmissionIsSubmitted(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: minicompat.SubmissionMarker + "\n",
	}}}
	result := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecXML).Execute(
		context.Background(), contract.Case{InstanceID: "case-a", ProblemStatement: "fix it"},
	)

	if result.Info.ExitStatus != "Submitted" || result.ModelPatch != "" {
		t.Fatalf("result = %+v", result)
	}
	if result.LLMCalls != 1 || result.ToolCalls != 1 {
		t.Fatalf("calls = llm %d, tool %d", result.LLMCalls, result.ToolCalls)
	}
}

func TestCodeSearchWithoutPreloadSkipsInitialRetrieval(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: minicompat.SubmissionMarker + "\npatch\n",
	}}}
	exec := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecXML)
	exec.EnableCodeSearch = true

	result := exec.Execute(context.Background(), contract.Case{
		InstanceID: "case-a",
		// BuildContext rejects an empty query. A successful run therefore proves
		// that no initial retrieval occurred on the no-preload path.
		ProblemStatement: "",
	})

	if result.Info.ExitStatus != "Submitted" {
		t.Fatalf("result = %+v", result)
	}
	if result.WorkspaceIndex.Documents == 0 {
		t.Fatalf("workspace index was not built: %+v", result.WorkspaceIndex)
	}
	if result.WorkspaceIndex.PreloadedDocuments != 0 ||
		result.WorkspaceIndex.PreloadedChars != 0 ||
		result.WorkspaceIndex.PreloadInjected {
		t.Fatalf("no-preload run retrieved initial context: %+v", result.WorkspaceIndex)
	}
	if len(modelImpl.requests) != 1 || len(modelImpl.requests[0].Messages) < 2 {
		t.Fatalf("model requests = %#v", modelImpl.requests)
	}
	assertOfflineRequestPrompts(t, modelImpl.requests[0], true)
	taskPrompt := modelImpl.requests[0].Messages[1].Content
	if !strings.Contains(taskPrompt, "Use code_search") {
		t.Fatalf("task prompt did not select the code-search protocol: %q", taskPrompt)
	}
	if strings.Contains(taskPrompt, "<workspace_context>") {
		t.Fatalf("no-preload prompt contains workspace context: %q", taskPrompt)
	}
}

func assertOfflineRequestPrompts(t *testing.T, request *model.Request, codeSearch bool) {
	t.Helper()
	if len(request.Messages) < 2 {
		t.Fatalf("request messages = %#v", request.Messages)
	}
	wantSystem := minicompat.OfflineSystemPrompt
	if codeSearch {
		wantSystem = minicompat.OfflineSystemPromptWithCodeSearch
	}
	if request.Messages[0].Role != model.RoleSystem || request.Messages[0].Content != wantSystem {
		t.Fatalf("system message = %#v, want %q", request.Messages[0], wantSystem)
	}
	if strings.Contains(request.Messages[1].Content, "you can also install it") {
		t.Fatalf("user prompt encourages installing unavailable tools: %q", request.Messages[1].Content)
	}
}

func TestCodeSearchWithExplicitPreloadInjectsInitialContext(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: minicompat.SubmissionMarker + "\npatch\n",
	}}}
	exec := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecXML)
	exec.EnableCodeSearch = true
	exec.EnableWorkspacePreload = true

	result := exec.Execute(context.Background(), contract.Case{
		InstanceID: "case-a", ProblemStatement: "find_user_by_email",
	})

	if result.Info.ExitStatus != "Submitted" {
		t.Fatalf("result = %+v", result)
	}
	if !result.WorkspaceIndex.PreloadInjected ||
		result.WorkspaceIndex.PreloadedDocuments == 0 ||
		result.WorkspaceIndex.PreloadedChars == 0 {
		t.Fatalf("preload stats = %+v", result.WorkspaceIndex)
	}
	if len(modelImpl.requests) != 1 || len(modelImpl.requests[0].Messages) < 2 {
		t.Fatalf("model requests = %#v", modelImpl.requests)
	}
	taskPrompt := modelImpl.requests[0].Messages[1].Content
	if !strings.Contains(taskPrompt, "<workspace_context>") ||
		!strings.Contains(taskPrompt, "find_user_by_email") {
		t.Fatalf("preloaded prompt = %q", taskPrompt)
	}
}

func TestCodeSearchOnlyTurnReturnsXMLBeforeBashSubmission(t *testing.T) {
	modelImpl := &scriptedModel{responses: []*model.Response{
		assistantResponse("locate the implementation", codeSearchCall("search-1", "find_user_by_email")),
		assistantResponse("done", bashCall("submit", "submit")),
	}}
	environment := &fakeEnvironment{results: []sweenv.CommandResult{{
		Output: minicompat.SubmissionMarker + "\npatch\n",
	}}}
	exec := newTestExecutor(modelImpl, environment, minicompat.ObservationCodecXML)
	exec.EnableCodeSearch = true

	result := exec.Execute(context.Background(), contract.Case{
		InstanceID: "case-a", ProblemStatement: "Fix user lookup.",
	})
	if result.Info.ExitStatus != "Submitted" || result.CodeSearchCalls != 1 ||
		result.CodeSearchResultBytes == 0 || result.CodeSearchObservationBytes == 0 ||
		len(result.CodeSearchRawResults) != 1 {
		t.Fatalf("result = %+v", result)
	}
	var rawResult map[string]any
	if err := json.Unmarshal(result.CodeSearchRawResults[0], &rawResult); err != nil {
		t.Fatalf("unmarshal raw code-search result: %v", err)
	}
	if _, ok := rawResult["documents"]; !ok {
		t.Fatalf("raw code-search result = %#v", rawResult)
	}
	if len(modelImpl.requests) != 2 {
		t.Fatalf("model requests = %d, want 2", len(modelImpl.requests))
	}
	if got := modelImpl.requests[0].ToolOrder; !reflect.DeepEqual(got, []string{"code_search", "bash"}) {
		t.Fatalf("tool order = %#v, want code_search before bash", got)
	}
	var searchObservation string
	for _, message := range modelImpl.requests[1].Messages {
		if message.Role == model.RoleTool && message.ToolName == "code_search" {
			searchObservation = message.Content
			break
		}
	}
	if !strings.HasPrefix(searchObservation, `<code_search_results snapshot="task_start">`) ||
		!strings.Contains(searchObservation, `<result path="users.py"`) ||
		strings.Contains(searchObservation, `"documents"`) {
		t.Fatalf("code_search observation = %q", searchObservation)
	}
}

func TestValidateRequiresStoreForEnabledEmbeddingCache(t *testing.T) {
	cfg := &embeddingconfig.Config{}
	cfg.Embedding.Provider = "openai"
	cfg.Embedding.APIBase = "http://embedding.example/v1"
	cfg.Embedding.APIKey = "secret"
	cfg.Embedding.Model = "bge-m3"
	cfg.Embedding.Dimensions = 3
	cfg.Embedding.BatchSize = 64
	cfg.Embedding.Concurrency = 1
	cfg.Retrieval.Mode = "hybrid"
	cfg.Retrieval.MaxResults = 4
	cfg.Retrieval.MaxChars = 6000
	cfg.Cache.Enabled = true
	cfg.Cache.Directory = t.TempDir()
	cfg.Cache.ModelFingerprint = "weights-v1"

	exec := newTestExecutor(
		&scriptedModel{},
		&fakeEnvironment{},
		minicompat.ObservationCodecXML,
	)
	exec.EnableCodeSearch = true
	exec.EmbeddingConfig = cfg
	if err := exec.Validate(); err == nil ||
		!strings.Contains(err.Error(), "no cache store") {
		t.Fatalf("Validate() error = %v, want missing cache store", err)
	}
}

func TestValidateRequiresCodeSearchForWorkspacePreload(t *testing.T) {
	exec := newTestExecutor(
		&scriptedModel{},
		&fakeEnvironment{},
		minicompat.ObservationCodecXML,
	)
	exec.EnableWorkspacePreload = true

	if err := exec.Validate(); err == nil ||
		!strings.Contains(err.Error(), "preload requires code search") {
		t.Fatalf("Validate() error = %v, want code-search requirement", err)
	}
}

func TestValidateRequiresCodeSearchForNonDefaultRepresentation(t *testing.T) {
	exec := newTestExecutor(
		&scriptedModel{},
		&fakeEnvironment{},
		minicompat.ObservationCodecXML,
	)
	exec.WorkspaceRepresentation = tagagent.WorkspaceRepresentationASTCode

	if err := exec.Validate(); err == nil ||
		!strings.Contains(err.Error(), "requires code search") {
		t.Fatalf("Validate() error = %v, want code-search requirement", err)
	}
}
