//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	"trpc.group/trpc-go/trpc-agent-go/memory/extractor"
	"trpc.group/trpc-go/trpc-agent-go/memory/inmemory"
	memorymem0 "trpc.group/trpc-go/trpc-agent-go/memory/mem0"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestGetLMEScenariosMem0OSS(t *testing.T) {
	got := getLMEScenarios("mem0_oss")
	if len(got) != 1 || got[0] != scenarios.ScenarioMem0OSS {
		t.Fatalf("getLMEScenarios(mem0_oss) = %v, want [%s]", got, scenarios.ScenarioMem0OSS)
	}
}

func TestCompleteLMERunConfigIgnoresMem0PreflightForAuto(t *testing.T) {
	cfg := lmeRunConfig{
		DatasetPath:       "dataset.json",
		Mem0PreflightPath: filepath.Join(t.TempDir(), "missing-preflight.json"),
		TraceContentMode:  lmeTraceContentHash,
	}
	if err := completeLMERunConfig(&cfg, Config{Scenario: "auto"}); err != nil {
		t.Fatalf("completeLMERunConfig() error = %v", err)
	}
}

func TestNewLMEExtractorUsesDefaultBehavior(t *testing.T) {
	ext := newLMEExtractor(memoryServiceOptions{})
	if _, ok := ext.Metadata()["update_policy"]; ok {
		t.Fatal("default extractor unexpectedly reports update_policy")
	}
	if _, ok := ext.Metadata()["conversation_extraction"]; ok {
		t.Fatal("default extractor unexpectedly reports conversation_extraction")
	}
}

func TestNewLMEExtractorUsesConfiguredUpdatePolicy(t *testing.T) {
	tests := []lmeAutoUpdatePolicy{
		lmeAutoUpdatePolicyPreserveHistory,
		lmeAutoUpdatePolicyAppendOnly,
	}
	for _, policy := range tests {
		t.Run(string(policy), func(t *testing.T) {
			ext := newLMEExtractor(memoryServiceOptions{
				autoUpdatePolicy: policy,
			})
			if _, ok := ext.Metadata()["update_policy"]; ok {
				t.Fatal("configured extractor exposes behavioral metadata")
			}
			provider, ok := ext.(interface {
				UpdatePolicy() extractor.UpdatePolicy
			})
			if !ok {
				t.Fatal("tracing extractor does not expose its update policy")
			}
			if got := provider.UpdatePolicy(); got != extractor.UpdatePolicy(policy) {
				t.Fatalf("UpdatePolicy() = %q, want %q", got, policy)
			}
			wrapper := ext.(*lmeTracingExtractor)
			if wrapper.UnwrapMemoryExtractor() == nil {
				t.Fatal("tracing extractor did not expose its wrapped extractor")
			}
		})
	}
}

func TestNewLMEExtractorUsesAssistantEpisodeExtraction(t *testing.T) {
	ext := newLMEExtractor(memoryServiceOptions{
		conversationExtraction: lmeConversationExtractionAssistantEpisode,
	})
	if _, ok := ext.Metadata()["conversation_extraction"]; ok {
		t.Fatal("configured extractor exposes behavioral metadata")
	}
	wrapper := ext.(*lmeTracingExtractor)
	if wrapper.UnwrapMemoryExtractor() == nil {
		t.Fatal("tracing extractor did not expose its wrapped extractor")
	}
}

func TestParseLMEAutoUpdatePolicy(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    lmeAutoUpdatePolicy
		wantErr bool
	}{
		{name: "empty defaults to merge similar", want: lmeAutoUpdatePolicyMergeSimilar},
		{name: "merge similar", raw: "merge_similar", want: lmeAutoUpdatePolicyMergeSimilar},
		{name: "preserve history", raw: " PRESERVE_HISTORY ", want: lmeAutoUpdatePolicyPreserveHistory},
		{name: "append only", raw: "append_only", want: lmeAutoUpdatePolicyAppendOnly},
		{name: "invalid", raw: "conservative", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLMEAutoUpdatePolicy(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatal("parseLMEAutoUpdatePolicy() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLMEAutoUpdatePolicy() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("parseLMEAutoUpdatePolicy() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestParseLMEConversationExtraction(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    lmeConversationExtraction
		wantErr bool
	}{
		{name: "empty defaults to disabled", want: lmeConversationExtractionDisabled},
		{name: "disabled", raw: "disabled", want: lmeConversationExtractionDisabled},
		{
			name: "assistant episode",
			raw:  " ASSISTANT-EPISODE ",
			want: lmeConversationExtractionAssistantEpisode,
		},
		{name: "invalid", raw: "assistant", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := parseLMEConversationExtraction(test.raw)
			if test.wantErr {
				if err == nil {
					t.Fatal("parseLMEConversationExtraction() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseLMEConversationExtraction() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("parseLMEConversationExtraction() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestValidateLMEProxyRunID(t *testing.T) {
	for _, runID := range []string{"", "lme-20260717.123_test"} {
		if err := validateLMEProxyRunID(runID); err != nil {
			t.Fatalf("validateLMEProxyRunID(%q) error = %v", runID, err)
		}
	}
	for _, runID := range []string{"contains space", "contains/slash", strings.Repeat("x", 129)} {
		if err := validateLMEProxyRunID(runID); err == nil {
			t.Fatalf("validateLMEProxyRunID(%q) error = nil", runID)
		}
	}
}

func TestValidateLMEManifestTaskLimit(t *testing.T) {
	for _, test := range []struct {
		name     string
		maxTasks int
		wantErr  bool
	}{
		{name: "unset", maxTasks: 0},
		{name: "exact", maxTasks: 50},
		{name: "truncated", maxTasks: 2, wantErr: true},
		{name: "oversized", maxTasks: 70, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateLMEManifestTaskLimit(test.maxTasks, 50)
			if test.wantErr && err == nil {
				t.Fatal("validateLMEManifestTaskLimit() error = nil, want error")
			}
			if !test.wantErr && err != nil {
				t.Fatalf("validateLMEManifestTaskLimit() error = %v", err)
			}
		})
	}
}

func TestRequireLMEAutoMemories(t *testing.T) {
	ctx := context.Background()
	service := inmemory.NewMemoryService()
	defer service.Close()
	userKey := memory.UserKey{
		AppName: lmeAppAuto,
		UserID:  "case-1",
	}

	if err := requireLMEAutoMemories(ctx, service, userKey); err == nil {
		t.Fatal("requireLMEAutoMemories() error = nil, want missing memory error")
	}
	if err := service.AddMemory(ctx, userKey, "User likes yoga.", []string{"yoga"}); err != nil {
		t.Fatalf("AddMemory() error = %v", err)
	}
	if err := requireLMEAutoMemories(ctx, service, userKey); err != nil {
		t.Fatalf("requireLMEAutoMemories() error = %v", err)
	}
}

func TestLMEAutoQAToolsUseStandardSearch(t *testing.T) {
	tools := lmeMemoryQATools()
	if len(tools) != 1 {
		t.Fatalf("len(tools) = %d, want 1", len(tools))
	}
	declaration := tools[0].Declaration()
	if declaration.Name != memory.SearchToolName {
		t.Fatalf("tool name = %q, want %q", declaration.Name, memory.SearchToolName)
	}
	if _, ok := declaration.InputSchema.Properties["search_mode"]; ok {
		t.Fatal("standard memory_search exposes search_mode")
	}
}

func TestLMEResultJSONKeepsCurrentSupportedSchema(t *testing.T) {
	result := newLMERunResult("auto", "pgvector", lmeRunConfig{
		RetrievalTopK: lmeRetrievalTopK,
	}, 1)
	result.Cases = append(result.Cases, &lmeCaseResult{QuestionID: "case-1"})
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	metadata := decoded["metadata"].(map[string]any)
	config := metadata["config"].(map[string]any)
	if got := config["retrieval_top_k"]; got != float64(lmeRetrievalTopK) {
		t.Fatalf("retrieval_top_k = %v, want %d", got, lmeRetrievalTopK)
	}
	summary := decoded["summary"].(map[string]any)
	if _, ok := summary["retrieval"]; ok {
		t.Fatal("supported scenario summary contains retrieval field")
	}
	cases := decoded["cases"].([]any)
	if _, ok := cases[0].(map[string]any)["retrieval"]; ok {
		t.Fatal("supported scenario case contains retrieval field")
	}
}

func TestClearResidualLMEMem0MemoriesDeletesUntilEmpty(t *testing.T) {
	userKey := memory.UserKey{AppName: lmeAppMem0, UserID: "case-mem0-clear"}
	mem := &recordingResidualClearMemoryService{
		pages: [][]*memory.Entry{
			{{ID: "m1"}, {ID: "m2"}},
			{{ID: "m3"}},
			{},
		},
	}

	if err := clearResidualLMEMem0Memories(context.Background(), mem, userKey); err != nil {
		t.Fatalf("clearResidualLMEMem0Memories() error = %v", err)
	}
	wantDeleted := []string{"m1", "m2", "m3"}
	if len(mem.deleted) != len(wantDeleted) {
		t.Fatalf("deleted = %v, want %v", mem.deleted, wantDeleted)
	}
	for i := range wantDeleted {
		if mem.deleted[i] != wantDeleted[i] {
			t.Fatalf("deleted = %v, want %v", mem.deleted, wantDeleted)
		}
	}
	if mem.readCalls != 3 {
		t.Fatalf("readCalls = %d, want 3", mem.readCalls)
	}
	for _, key := range mem.deleteKeys {
		if key.AppName != userKey.AppName || key.UserID != userKey.UserID {
			t.Fatalf("delete key = %+v, want app/user %+v", key, userKey)
		}
	}
}

func TestClearResidualLMEMem0MemoriesRejectsEmptyID(t *testing.T) {
	userKey := memory.UserKey{AppName: lmeAppMem0, UserID: "case-mem0-clear-empty"}
	mem := &recordingResidualClearMemoryService{
		pages: [][]*memory.Entry{{{ID: ""}}},
	}

	if err := clearResidualLMEMem0Memories(context.Background(), mem, userKey); err == nil {
		t.Fatal("clearResidualLMEMem0Memories() error = nil, want empty id error")
	}
	if len(mem.deleted) != 0 {
		t.Fatalf("deleted = %v, want none", mem.deleted)
	}
}

type recordingResidualClearMemoryService struct {
	memory.Service

	pages      [][]*memory.Entry
	readCalls  int
	deleted    []string
	deleteKeys []memory.Key
}

func (s *recordingResidualClearMemoryService) ReadMemories(
	_ context.Context,
	_ memory.UserKey,
	_ int,
) ([]*memory.Entry, error) {
	idx := s.readCalls
	s.readCalls++
	if idx >= len(s.pages) {
		return nil, nil
	}
	return s.pages[idx], nil
}

func (s *recordingResidualClearMemoryService) DeleteMemory(
	_ context.Context,
	key memory.Key,
) error {
	s.deleted = append(s.deleted, key.MemoryID)
	s.deleteKeys = append(s.deleteKeys, key)
	return nil
}

func TestSetLMERunMetadataReportsAutoUpdatePolicy(t *testing.T) {
	result := newLMERunResult("auto", "pgvector", lmeRunConfig{
		AutoUpdatePolicy:       lmeAutoUpdatePolicyPreserveHistory,
		ConversationExtraction: string(lmeConversationExtractionAssistantEpisode),
	}, 1)
	result.Summary.CompletedCases = 1
	setLMERunMetadata(result, &lmeAutoEvaluator{})

	if got := result.Metadata.MemoryBuild["update_policy"]; got != lmeAutoUpdatePolicyPreserveHistory {
		t.Fatalf("update_policy = %v, want %s", got, lmeAutoUpdatePolicyPreserveHistory)
	}
	if got := result.Metadata.MemoryBuild["conversation_extraction"]; got !=
		string(lmeConversationExtractionAssistantEpisode) {
		t.Fatalf(
			"conversation_extraction = %v, want %s",
			got,
			lmeConversationExtractionAssistantEpisode,
		)
	}
	if !result.Metadata.FairlyComparable {
		t.Fatal("Auto result is not marked comparable after temporal input alignment")
	}
	if result.Metadata.ComparisonStatus != "comparable" {
		t.Fatalf("comparison status = %q", result.Metadata.ComparisonStatus)
	}
	if len(result.Metadata.ComparisonLimitations) != 0 {
		t.Fatalf("comparison limitations = %v, want none", result.Metadata.ComparisonLimitations)
	}
	payload, err := json.Marshal(result.Metadata)
	if err != nil {
		t.Fatalf("marshal metadata: %v", err)
	}
	if !strings.Contains(string(payload), `"fairly_comparable":true`) {
		t.Fatalf("metadata does not persist an explicit true comparison status: %s", payload)
	}
}

func TestSetLMERunMetadataOmitsAutoPolicyForMem0(t *testing.T) {
	result := newLMERunResult("mem0_oss", "mem0_oss", lmeRunConfig{
		AutoUpdatePolicy: lmeAutoUpdatePolicyMergeSimilar,
	}, 1)
	result.Summary.CompletedCases = 1
	setLMERunMetadata(result, &lmeMem0OSSEvaluator{})

	if _, ok := result.Metadata.MemoryBuild["update_policy"]; ok {
		t.Fatal("Mem0 memory-build metadata contains an Auto update policy")
	}
	if got := result.Metadata.MemoryBuild["temporal_context"]; got != lmeMem0TemporalContext {
		t.Fatalf("temporal_context = %v, want %s", got, lmeMem0TemporalContext)
	}
	if got := result.Metadata.MemoryBuild["custom_extraction_prompt"]; got != true {
		t.Fatalf("custom_extraction_prompt = %v, want true", got)
	}
}

func TestLMEToolTraceHelpersPreserveUsageAndResult(t *testing.T) {
	usage := &model.Usage{
		PromptTokens:     10,
		CompletionTokens: 3,
		TotalTokens:      13,
		PromptTokensDetails: model.PromptTokensDetails{
			CachedTokens: 4,
		},
	}
	var total scenarios.TokenUsage
	addLMEModelUsage(&total, usage)
	if total.PromptTokens != 10 || total.CompletionTokens != 3 ||
		total.TotalTokens != 13 || total.CachedTokens != 4 || total.LLMCalls != 1 {
		t.Fatalf("usage = %+v", total)
	}
	result := &lmeCollectResult{}
	calls := recordLMEToolCalls(result, 1, model.Message{
		Role: model.RoleAssistant,
		ToolCalls: []model.ToolCall{{
			Function: model.FunctionDefinitionParam{
				Name:      "memory_search",
				Arguments: []byte(`{"query":"hello"}`),
			},
		}},
	}, usage)
	recordLMEToolResponse(result, calls, 1, model.Message{
		Role:     model.RoleTool,
		ToolName: "memory_search",
		Content:  `{"results":[]}`,
	})
	if len(result.Steps) != 1 || len(result.Steps[0].ToolCalls) != 1 {
		t.Fatalf("steps = %+v", result.Steps)
	}
	call := result.Steps[0].ToolCalls[0]
	if call.Name != "memory_search" || call.Result != `{"results":[]}` ||
		result.Steps[0].CachedTokens != 4 {
		t.Fatalf("tool call = %+v; step = %+v", call, result.Steps[0])
	}
}

func TestBuildLMECaseResultRecordsInvalidJudgeResponse(t *testing.T) {
	judge := &scriptedLMEJudgeModel{
		responses: []string{
			"The response contains the phrase, but I need to think more.",
			"Still not a valid label.",
		},
	}
	inst := &dataset.LongMemEvalInstance{
		QuestionID:   "judge-invalid",
		QuestionType: "single-session-user",
		Question:     "What type of cocktail recipe did I try last weekend?",
		Answer:       "lavender gin fizz",
	}

	result, err := buildLMECaseResult(
		context.Background(),
		judge,
		lmeRunConfig{JudgeMaxTokens: lmeDefaultJudgeMaxTokens},
		inst,
		"I tried a lavender gin fizz, but not last weekend.",
		0,
		nil,
		0,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("buildLMECaseResult() error = %v", err)
	}
	if result.Correct {
		t.Fatal("result.Correct = true, want false")
	}
	if result.Metrics.Accuracy != 0 {
		t.Fatalf("accuracy = %v, want 0", result.Metrics.Accuracy)
	}
	if result.JudgeError == "" {
		t.Fatal("JudgeError is empty")
	}
	if judge.calls != 2 {
		t.Fatalf("judge calls = %d, want 2", judge.calls)
	}
	for i, maxTokens := range judge.maxTokens {
		if maxTokens != lmeDefaultJudgeMaxTokens {
			t.Fatalf("call %d max tokens = %d, want %d", i, maxTokens, lmeDefaultJudgeMaxTokens)
		}
	}
}

type scriptedLMEJudgeModel struct {
	responses []string
	calls     int
	maxTokens []int
}

func (m *scriptedLMEJudgeModel) GenerateContent(
	_ context.Context,
	request *model.Request,
) (<-chan *model.Response, error) {
	if request != nil && request.GenerationConfig.MaxTokens != nil {
		m.maxTokens = append(m.maxTokens, *request.GenerationConfig.MaxTokens)
	}
	text := "no"
	if m.calls < len(m.responses) {
		text = m.responses[m.calls]
	}
	m.calls++
	ch := make(chan *model.Response, 1)
	ch <- &model.Response{
		Choices: []model.Choice{{
			Message: model.NewAssistantMessage(text),
		}},
	}
	close(ch)
	return ch, nil
}

func (m *scriptedLMEJudgeModel) Info() model.Info {
	return model.Info{Name: "scripted-lme-judge"}
}

func TestLMEQAMemoryServiceEnforcesUniformRetrievalLimit(t *testing.T) {
	entries := make([]*memory.Entry, lmeRetrievalTopK+5)
	for i := range entries {
		entries[i] = &memory.Entry{ID: fmt.Sprintf("memory-%02d", i)}
	}
	for _, appName := range []string{lmeAppAuto, lmeAppMem0} {
		t.Run(appName, func(t *testing.T) {
			inner := &recordingTopKMemoryService{entries: entries}
			service, err := newLMEQAMemoryService(inner)
			if err != nil {
				t.Fatalf("newLMEQAMemoryService() error = %v", err)
			}
			got, err := service.SearchMemories(
				context.Background(),
				memory.UserKey{AppName: appName, UserID: "case-1"},
				"alice hiking",
				memory.WithSearchOptions(memory.SearchOptions{
					Query:      "ignored query",
					Kind:       memory.KindEpisode,
					MaxResults: lmeRetrievalTopK + 10,
				}),
			)
			if err != nil {
				t.Fatalf("SearchMemories() error = %v", err)
			}
			if inner.searchOptions.MaxResults != lmeRetrievalTopK {
				t.Fatalf("inner MaxResults = %d, want %d", inner.searchOptions.MaxResults, lmeRetrievalTopK)
			}
			if inner.userKey.AppName != appName {
				t.Fatalf("inner AppName = %q, want %q", inner.userKey.AppName, appName)
			}
			if inner.searchOptions.Query != "alice hiking" {
				t.Fatalf("inner Query = %q, want alice hiking", inner.searchOptions.Query)
			}
			if inner.searchOptions.Kind != memory.KindEpisode {
				t.Fatalf("inner Kind = %q, want %q", inner.searchOptions.Kind, memory.KindEpisode)
			}
			if len(got) != lmeRetrievalTopK {
				t.Fatalf("len(SearchMemories()) = %d, want %d", len(got), lmeRetrievalTopK)
			}
		})
	}
}

func TestLMEQAMemoryServiceRequiresBackend(t *testing.T) {
	if _, err := newLMEQAMemoryService(nil); err == nil {
		t.Fatal("newLMEQAMemoryService(nil) error = nil")
	}
}

func TestLMEMem0OSSRealSearchPathUsesUniformRetrievalLimit(t *testing.T) {
	var request struct {
		TopK int `json:"top_k"`
	}
	results := make([]map[string]any, lmeRetrievalTopK+5)
	for i := range results {
		results[i] = map[string]any{
			"id":         fmt.Sprintf("memory-%02d", i),
			"memory":     fmt.Sprintf("memory %02d", i),
			"metadata":   map[string]any{"trpc_app_name": lmeAppMem0},
			"score":      float64(i),
			"created_at": "2026-01-01T00:00:00Z",
			"user_id":    "case-1",
		}
	}
	responseBody, err := json.Marshal(map[string]any{"results": results})
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var requestMethod string
	var requestPath string
	client := &http.Client{Transport: lmeRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		requestMethod = r.Method
		requestPath = r.URL.Path
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			return nil, err
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(string(responseBody))),
			Request:    r,
		}, nil
	})}

	inner, err := memorymem0.NewService(
		memorymem0.WithHost("http://mem0.example.test"),
		memorymem0.WithSelfHostedOSS(),
		memorymem0.WithHTTPClient(client),
	)
	if err != nil {
		t.Fatalf("mem0.NewService() error = %v", err)
	}
	defer inner.Close()
	service, err := newLMEQAMemoryService(&lmeMem0OSSService{inner: inner})
	if err != nil {
		t.Fatalf("newLMEQAMemoryService() error = %v", err)
	}
	got, err := service.SearchMemories(
		context.Background(),
		memory.UserKey{AppName: lmeAppMem0, UserID: "case-1"},
		"alice hiking",
		memory.WithSearchOptions(memory.SearchOptions{MaxResults: 1}),
	)
	if err != nil {
		t.Fatalf("SearchMemories() error = %v", err)
	}
	if requestMethod != http.MethodPost || requestPath != "/search" {
		t.Fatalf("Mem0 request = %s %s, want POST /search", requestMethod, requestPath)
	}
	if request.TopK != lmeRetrievalTopK {
		t.Fatalf("Mem0 request top_k = %d, want %d", request.TopK, lmeRetrievalTopK)
	}
	if len(got) != lmeRetrievalTopK {
		t.Fatalf("len(SearchMemories()) = %d, want %d", len(got), lmeRetrievalTopK)
	}
}

func TestLMEMem0OSSRequestErrorsDoNotLeakProviderContent(t *testing.T) {
	const secret = "api_key=provider-secret"
	service := &lmeMem0OSSService{
		host: "http://mem0.example.test",
		client: &http.Client{Transport: lmeRoundTripperFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader(secret)),
			}, nil
		})},
	}
	err := service.doOSS(context.Background(), http.MethodDelete, "/memories", nil)
	if err == nil || !strings.Contains(err.Error(), "status=500") {
		t.Fatalf("doOSS() error = %v, want status", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("doOSS() leaked provider response: %v", err)
	}

	service.client.Transport = lmeRoundTripperFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New(secret)
	})
	err = service.doOSS(context.Background(), http.MethodDelete, "/memories", nil)
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("doOSS() transport error = %v", err)
	}
}

type lmeRoundTripperFunc func(*http.Request) (*http.Response, error)

func (f lmeRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestLMERetrievalMetadataUsesBackendKey(t *testing.T) {
	for _, backend := range []string{"pgvector", "mem0_oss"} {
		t.Run(backend, func(t *testing.T) {
			cfg := lmeRunConfig{RetrievalTopK: lmeRetrievalTopK}
			result := newLMERunResult("scenario", backend, cfg, 1)
			limit, ok := result.Metadata.RetrievalLimits[backend]
			if !ok {
				t.Fatalf("RetrievalLimits[%q] is missing", backend)
			}
			if limit.RequestedTopK != lmeRetrievalTopK ||
				limit.EffectiveTopK != lmeRetrievalTopK {
				t.Fatalf("RetrievalLimits[%q] = %+v", backend, limit)
			}
		})
	}
}

func TestValidateLMEResumeRetrievalLimit(t *testing.T) {
	cfg := lmeRunConfig{RetrievalTopK: lmeRetrievalTopK}
	for _, tt := range []struct {
		name       string
		checkpoint *lmeRunResult
		wantError  bool
	}{
		{name: "matching", checkpoint: newLMERunResult("auto", "pgvector", cfg, 1)},
		{
			name: "config limit changed",
			checkpoint: newLMERunResult("auto", "pgvector", lmeRunConfig{
				RetrievalTopK: lmeRetrievalTopK - 1,
			}, 1),
			wantError: true,
		},
		{
			name: "effective limit changed",
			checkpoint: &lmeRunResult{Metadata: &lmeMetadata{
				Config: cfg,
				RetrievalLimits: map[string]lmeRetrievalLimit{
					"pgvector": {
						RequestedTopK: lmeRetrievalTopK,
						EffectiveTopK: lmeRetrievalTopK - 1,
					},
				},
			}},
			wantError: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLMEResumeRetrievalLimit(tt.checkpoint, "pgvector")
			if (err != nil) != tt.wantError {
				t.Fatalf("validateLMEResumeRetrievalLimit() error = %v, wantError %v", err, tt.wantError)
			}
		})
	}
}

func TestLMEResultJSONIncludesRetrievalLimit(t *testing.T) {
	result := newLMERunResult("auto", "pgvector", lmeRunConfig{
		RetrievalTopK: lmeRetrievalTopK,
	}, 1)
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	metadata := decoded["metadata"].(map[string]any)
	config := metadata["config"].(map[string]any)
	if got := config["retrieval_top_k"]; got != float64(lmeRetrievalTopK) {
		t.Fatalf("retrieval_top_k = %v, want %d", got, lmeRetrievalTopK)
	}
}

type recordingTopKMemoryService struct {
	memory.Service

	entries       []*memory.Entry
	searchOptions memory.SearchOptions
	userKey       memory.UserKey
}

func (s *recordingTopKMemoryService) SearchMemories(
	_ context.Context,
	userKey memory.UserKey,
	query string,
	opts ...memory.SearchOption,
) ([]*memory.Entry, error) {
	s.userKey = userKey
	s.searchOptions = memory.ResolveSearchOptions(query, opts)
	return s.entries, nil
}
