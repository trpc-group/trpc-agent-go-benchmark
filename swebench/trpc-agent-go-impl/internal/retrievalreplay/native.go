//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
)

type nativeManifest struct {
	RunID                   string                 `json:"run_id"`
	RunnerType              string                 `json:"runner_type"`
	FrameworkModule         string                 `json:"framework_module"`
	FrameworkVersion        string                 `json:"framework_version"`
	AgentProtocol           string                 `json:"agent_protocol"`
	UpstreamCommit          string                 `json:"upstream_commit"`
	ObservationCodec        string                 `json:"observation_codec"`
	SourceRevision          string                 `json:"source_revision"`
	SourceModified          bool                   `json:"source_modified"`
	BinarySHA256            string                 `json:"binary_sha256"`
	CasesSHA256             string                 `json:"cases_sha256"`
	ModelConfigSHA256       string                 `json:"model_config_sha256"`
	EnvironmentConfigSHA256 string                 `json:"environment_config_sha256"`
	SelectedInstancesSHA256 string                 `json:"selected_instances_sha256"`
	CleanRoom               bool                   `json:"clean_room"`
	ToolLoopWarning         bool                   `json:"tool_loop_warning"`
	CleanRoomPolicySHA256   string                 `json:"clean_room_policy_sha256"`
	ImageSetSHA256          string                 `json:"image_set_sha256"`
	CommandTimeout          string                 `json:"command_timeout"`
	CaseTimeout             string                 `json:"case_timeout"`
	CaseCount               int                    `json:"case_count"`
	CompletedCount          int                    `json:"completed_count"`
	PredictionCount         int                    `json:"prediction_count"`
	Workers                 int                    `json:"workers"`
	Status                  string                 `json:"status"`
	CodeSearch              *bool                  `json:"code_search,omitempty"`
	WorkspacePreload        *bool                  `json:"workspace_preload,omitempty"`
	WorkspaceRepresentation string                 `json:"workspace_representation,omitempty"`
	RepresentationSchema    string                 `json:"workspace_representation_schema,omitempty"`
	RepresentationSHA256    string                 `json:"workspace_representation_sha256,omitempty"`
	EmbeddingConfigSHA256   string                 `json:"embedding_config_sha256,omitempty"`
	EmbeddingConfig         *nativeEmbeddingConfig `json:"embedding_config,omitempty"`
}

type nativeCaseInfo struct {
	RunID                   string `json:"run_id"`
	ObservationCodec        string `json:"observation_codec"`
	SourceRevision          string `json:"source_revision"`
	SourceModified          bool   `json:"source_modified"`
	BinarySHA256            string `json:"binary_sha256"`
	ModelConfigSHA256       string `json:"model_config_sha256"`
	EnvironmentConfigSHA256 string `json:"environment_config_sha256"`
	CasesSHA256             string `json:"cases_sha256"`
	CommandTimeout          string `json:"command_timeout"`
	CaseTimeout             string `json:"case_timeout"`
	SelectedInstancesSHA256 string `json:"selected_instances_sha256"`
	CleanRoom               bool   `json:"clean_room"`
	ToolLoopWarning         bool   `json:"tool_loop_warning"`
	CodeSearch              bool   `json:"code_search"`
	WorkspacePreload        bool   `json:"workspace_preload"`
	WorkspaceRepresentation string `json:"workspace_representation"`
	RepresentationSHA256    string `json:"workspace_representation_sha256"`
	EmbeddingConfigSHA256   string `json:"embedding_config_sha256"`
	CleanRoomPolicySHA256   string `json:"clean_room_policy_sha256"`
	ImageSetSHA256          string `json:"image_set_sha256"`
	Repo                    string `json:"repo"`
	BaseCommit              string `json:"base_commit"`
	VerifiedBaseCommit      string `json:"verified_base_commit"`
	Workers                 int    `json:"workers"`
	ExitStatus              string `json:"exit_status"`
}

type nativeEmbeddingConfig struct {
	Provider      string `json:"provider"`
	Model         string `json:"model"`
	Dimensions    int    `json:"dimensions"`
	RetrievalMode string `json:"retrieval_mode"`
	MaxResults    int    `json:"max_results"`
	Cache         struct {
		Enabled          bool   `json:"enabled"`
		ModelFingerprint string `json:"model_fingerprint"`
	} `json:"cache"`
}

type nativeCaseDocument struct {
	InstanceID                 string                 `json:"instance_id"`
	Info                       nativeCaseInfo         `json:"info"`
	LLMCalls                   int                    `json:"llm_calls"`
	ToolCalls                  int                    `json:"tool_calls"`
	Events                     json.RawMessage        `json:"events"`
	ResponseCount              int                    `json:"response_count"`
	ResponsesSHA256            string                 `json:"responses_sha256"`
	CodeSearchCalls            int                    `json:"code_search_calls"`
	CodeSearchErrors           int                    `json:"code_search_errors"`
	CodeSearchResultBytes      int                    `json:"code_search_result_bytes"`
	CodeSearchObservationBytes int                    `json:"code_search_observation_bytes"`
	CodeSearchRawResults       []json.RawMessage      `json:"code_search_raw_results"`
	RetrievalTrace             []nativeRetrievalTrace `json:"retrieval_trace"`
	WorkspaceIndex             *nativeWorkspaceIndex  `json:"workspace_index"`
}

type nativeWorkspaceIndex struct {
	Representation        string `json:"representation"`
	RepresentationSchema  string `json:"representation_schema"`
	RepresentationSHA256  string `json:"representation_sha256"`
	ParserDependency      string `json:"parser_dependency"`
	ParserRuntimeSHA256   string `json:"parser_runtime_sha256"`
	EligibleFileSetSHA256 string `json:"eligible_file_set_sha256"`
	EligibleContentSHA256 string `json:"eligible_content_sha256"`
	DocumentSetSHA256     string `json:"document_set_sha256"`
	RetrievalMode         string `json:"retrieval_mode"`
}

type nativeRetrievalTrace struct {
	Call              int                       `json:"call"`
	ToolCallID        string                    `json:"tool_call_id"`
	Query             string                    `json:"query"`
	Status            string                    `json:"status"`
	Error             string                    `json:"error"`
	ErrorSHA256       string                    `json:"error_sha256"`
	ArgumentsSHA256   string                    `json:"arguments_sha256"`
	ResultSHA256      string                    `json:"result_sha256"`
	ObservationSHA256 string                    `json:"observation_sha256"`
	ResultBytes       int                       `json:"result_bytes"`
	ObservationBytes  int                       `json:"observation_bytes"`
	Documents         []nativeRetrievalDocument `json:"documents"`
}

type nativeRetrievalDocument struct {
	ID            string  `json:"id"`
	Path          string  `json:"path"`
	Lines         string  `json:"lines"`
	Symbol        string  `json:"symbol"`
	Score         float64 `json:"score"`
	ContentSHA256 string  `json:"content_sha256"`
}

type traceResponse struct {
	Choices   []traceChoice `json:"choices"`
	Done      bool          `json:"done"`
	IsPartial bool          `json:"is_partial"`
	Error     any           `json:"error"`
}

type traceChoice struct {
	Message traceMessage `json:"message"`
}

type traceMessage struct {
	Role      string          `json:"role"`
	ToolCalls []traceToolCall `json:"tool_calls"`
}

type traceToolCall struct {
	ID       string            `json:"id"`
	Function traceToolFunction `json:"function"`
}

type traceToolFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

func loadNativeManifest(runRoot string, binding NativeRunBinding) (nativeManifest, string, error) {
	manifest, actualSHA, err := inspectNativeManifest(runRoot)
	if err != nil {
		return nativeManifest{}, "", err
	}
	if manifest.RunID != binding.RunID {
		return nativeManifest{}, "", fmt.Errorf(
			"Native manifest run_id=%q, want %q",
			manifest.RunID,
			binding.RunID,
		)
	}
	if actualSHA != binding.ManifestSHA256 {
		return nativeManifest{}, "", fmt.Errorf(
			"Native manifest sha256=%s, want %s",
			actualSHA,
			binding.ManifestSHA256,
		)
	}
	return manifest, actualSHA, nil
}

func inspectNativeManifest(runRoot string) (nativeManifest, string, error) {
	manifestPath, err := boundedArtifactPath(runRoot, NativeManifestFilename)
	if err != nil {
		return nativeManifest{}, "", fmt.Errorf("resolve Native manifest: %w", err)
	}
	data, err := readRegularFile(manifestPath, "Native manifest", maxManifestBytes)
	if err != nil {
		return nativeManifest{}, "", err
	}
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nativeManifest{}, "", fmt.Errorf("parse Native manifest: %w", err)
	}
	actualSHA := digestBytes(data)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nativeManifest{}, "", fmt.Errorf("parse Native manifest fields: %w", err)
	}
	for _, name := range []string{
		"run_id", "runner_type", "framework_module", "framework_version",
		"agent_protocol", "upstream_commit", "observation_codec",
		"source_revision", "source_modified", "binary_sha256", "cases_sha256",
		"model_config_sha256", "environment_config_sha256",
		"selected_instances_sha256", "clean_room", "tool_loop_warning",
		"command_timeout", "case_timeout", "case_count", "completed_count",
		"prediction_count", "workers", "status",
	} {
		if value, ok := raw[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nativeManifest{}, "", fmt.Errorf("Native manifest is missing required field %q", name)
		}
	}
	var manifest nativeManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nativeManifest{}, "", fmt.Errorf("decode Native manifest: %w", err)
	}
	if err := validateNativeManifest(manifest, NativeRunBinding{RunID: manifest.RunID}); err != nil {
		return nativeManifest{}, "", err
	}
	return manifest, actualSHA, nil
}

func validateNativeManifest(manifest nativeManifest, binding NativeRunBinding) error {
	if manifest.RunID != binding.RunID {
		return fmt.Errorf("Native manifest run_id=%q, want %q", manifest.RunID, binding.RunID)
	}
	if manifest.RunnerType != "trpc-agent-go-native" {
		return fmt.Errorf("Native manifest runner_type=%q is unsupported", manifest.RunnerType)
	}
	if strings.TrimSpace(manifest.FrameworkModule) == "" ||
		strings.TrimSpace(manifest.FrameworkVersion) == "" ||
		strings.TrimSpace(manifest.UpstreamCommit) == "" ||
		!strings.HasPrefix(manifest.AgentProtocol, "mini-swe-agent-v2.1-on-trpc-agent-go") {
		return errors.New("Native manifest has incomplete framework/protocol provenance")
	}
	if strings.TrimSpace(manifest.ObservationCodec) == "" ||
		strings.TrimSpace(manifest.SourceRevision) == "" || manifest.SourceModified {
		return errors.New("Native manifest has invalid source or observation provenance")
	}
	for name, value := range map[string]string{
		"binary_sha256":             manifest.BinarySHA256,
		"cases_sha256":              manifest.CasesSHA256,
		"model_config_sha256":       manifest.ModelConfigSHA256,
		"environment_config_sha256": manifest.EnvironmentConfigSHA256,
		"selected_instances_sha256": manifest.SelectedInstancesSHA256,
	} {
		if !isHexSHA256(value) {
			return fmt.Errorf("Native manifest has invalid %s", name)
		}
	}
	if manifest.CleanRoom && (!isHexSHA256(manifest.CleanRoomPolicySHA256) ||
		!isHexSHA256(manifest.ImageSetSHA256)) {
		return errors.New("clean-room Native manifest has incomplete policy/image provenance")
	}
	if strings.TrimSpace(manifest.CommandTimeout) == "" ||
		strings.TrimSpace(manifest.CaseTimeout) == "" || manifest.Workers <= 0 ||
		manifest.CaseCount <= 0 || manifest.CompletedCount < 0 || manifest.PredictionCount < 0 {
		return errors.New("Native manifest has invalid timeout, worker, or coverage fields")
	}
	if manifest.CompletedCount > manifest.CaseCount || manifest.PredictionCount > manifest.CaseCount {
		return errors.New("Native manifest coverage exceeds case_count")
	}
	if manifest.Status != "completed" && manifest.Status != "completed_with_errors" {
		return fmt.Errorf("Native manifest status=%q is not terminal", manifest.Status)
	}
	if manifest.CodeSearch == nil || !*manifest.CodeSearch || manifest.WorkspacePreload == nil {
		return errors.New("Native manifest is not a provenance-complete code_search run")
	}
	if strings.TrimSpace(manifest.WorkspaceRepresentation) == "" ||
		strings.TrimSpace(manifest.RepresentationSchema) == "" ||
		!isHexSHA256(manifest.RepresentationSHA256) {
		return errors.New("Native manifest has incomplete workspace representation provenance")
	}
	if manifest.EmbeddingConfigSHA256 != "" && !isHexSHA256(manifest.EmbeddingConfigSHA256) {
		return errors.New("Native manifest has invalid embedding_config_sha256")
	}
	return nil
}

type nativeCaseEvidence struct {
	Identity        NativeCaseIdentity
	Queries         []RecordedQuery
	Results         []ExpectedQueryResult
	EmittedQueries  int
	SkippedQueries  int
	WorkspaceIndex  nativeWorkspaceIndex
	NativeSHA256    string
	ResponsesSHA256 string
}

func inspectNativeCase(
	runRoot string,
	manifest nativeManifest,
	instanceID string,
) (nativeCaseEvidence, error) {
	prefix := instanceID + "/" + instanceID
	nativePath, err := boundedArtifactPath(runRoot, prefix+".native.json")
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("resolve Native case %s: %w", instanceID, err)
	}
	nativeData, err := readRegularFile(nativePath, "Native case "+instanceID, maxNativeBytes)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("load Native case %s: %w", instanceID, err)
	}
	if err := rejectDuplicateJSONKeys(nativeData); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("parse Native case %s: %w", instanceID, err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(nativeData, &raw); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("inspect Native case %s: %w", instanceID, err)
	}
	for _, name := range []string{
		"instance_id", "info", "llm_calls", "tool_calls", "events",
		"response_count", "responses_sha256", "code_search_calls",
		"code_search_result_bytes", "code_search_raw_results",
		"retrieval_trace", "workspace_index",
	} {
		if value, ok := raw[name]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nativeCaseEvidence{}, fmt.Errorf(
				"Native case %s is missing required field %q",
				instanceID,
				name,
			)
		}
	}
	var result nativeCaseDocument
	if err := json.Unmarshal(nativeData, &result); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("decode Native case %s: %w", instanceID, err)
	}
	if err := validateNativeCase(result, manifest, instanceID); err != nil {
		return nativeCaseEvidence{}, err
	}

	responsesPath, err := boundedArtifactPath(runRoot, prefix+".responses.json")
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("resolve Native responses %s: %w", instanceID, err)
	}
	responsesData, err := readRegularFile(responsesPath, "Native responses "+instanceID, maxResponsesBytes)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("load Native responses %s: %w", instanceID, err)
	}
	responsesSHA := digestBytes(responsesData)
	if result.ResponsesSHA256 != responsesSHA {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s responses_sha256=%s, want %s",
			instanceID,
			result.ResponsesSHA256,
			responsesSHA,
		)
	}
	if err := rejectDuplicateJSONKeys(responsesData); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("parse Native responses %s: %w", instanceID, err)
	}
	var responses []traceResponse
	if err := json.Unmarshal(responsesData, &responses); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("decode Native responses %s: %w", instanceID, err)
	}
	if len(responses) != result.ResponseCount {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s response_count=%d, artifact has %d",
			instanceID,
			result.ResponseCount,
			len(responses),
		)
	}
	responseQueries, err := extractRecordedQueries(responses)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("extract Native response queries %s: %w", instanceID, err)
	}
	var events []traceResponse
	if err := json.Unmarshal(result.Events, &events); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("decode Native events %s: %w", instanceID, err)
	}
	eventQueries, err := extractRecordedQueries(events)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf("extract Native event queries %s: %w", instanceID, err)
	}
	executedQueries, err := selectExecutedQueries(result.RetrievalTrace, responseQueries)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s executed query provenance: %w",
			instanceID,
			err,
		)
	}
	executedEventQueries, err := selectExecutedQueries(result.RetrievalTrace, eventQueries)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s executed event query provenance: %w",
			instanceID,
			err,
		)
	}
	if err := equalRecordedQueries(executedQueries, executedEventQueries); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s executed response/event query provenance mismatch: %w",
			instanceID,
			err,
		)
	}
	if len(executedQueries) == 0 {
		return nativeCaseEvidence{}, fmt.Errorf("Native case %s contains no executed code_search query", instanceID)
	}
	results, err := validateNativeRetrieval(result, executedQueries)
	if err != nil {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s retrieval provenance: %w",
			instanceID,
			err,
		)
	}
	identity := NativeCaseIdentity{
		InstanceID: instanceID, RunID: result.Info.RunID, Repo: result.Info.Repo,
		BaseCommit: result.Info.BaseCommit, ExitStatus: result.Info.ExitStatus,
		LLMCalls: result.LLMCalls, ToolCalls: result.ToolCalls,
		ResponseCount: result.ResponseCount, ResponsesSHA256: result.ResponsesSHA256,
	}
	return nativeCaseEvidence{
		Identity: identity, Queries: executedQueries, Results: results,
		EmittedQueries: len(responseQueries), SkippedQueries: len(responseQueries) - len(executedQueries),
		WorkspaceIndex: *result.WorkspaceIndex, NativeSHA256: digestBytes(nativeData),
		ResponsesSHA256: responsesSHA,
	}, nil
}

// selectExecutedQueries uses retrieval_trace as the authoritative execution
// sequence. A model response may emit code_search after a successful submit in
// the same parallel tool-call batch; the runner's StopError prevents that later
// call from executing. Every executed trace must still map to exactly one
// emitted response call with the same ID, query, and raw argument digest.
func selectExecutedQueries(
	traces []nativeRetrievalTrace,
	emitted []RecordedQuery,
) ([]RecordedQuery, error) {
	byID := make(map[string][]RecordedQuery, len(emitted))
	for _, query := range emitted {
		byID[query.ToolCallID] = append(byID[query.ToolCallID], query)
	}
	executed := make([]RecordedQuery, len(traces))
	seen := make(map[string]struct{}, len(traces))
	for index, trace := range traces {
		if _, duplicate := seen[trace.ToolCallID]; duplicate {
			return nil, fmt.Errorf("retrieval_trace repeats tool_call_id %q", trace.ToolCallID)
		}
		seen[trace.ToolCallID] = struct{}{}
		candidates := byID[trace.ToolCallID]
		var matches []RecordedQuery
		for _, candidate := range candidates {
			if trace.Query == candidate.Query &&
				trace.ArgumentsSHA256 == candidate.NativeArgumentsSHA256 {
				matches = append(matches, candidate)
			}
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf(
				"retrieval_trace entry %d did not match an emitted model call with tool_call_id=%q",
				index+1,
				trace.ToolCallID,
			)
		}
		if len(matches) != 1 {
			return nil, fmt.Errorf(
				"retrieval_trace entry %d ambiguously matches %d emitted calls with tool_call_id=%q",
				index+1,
				len(matches),
				trace.ToolCallID,
			)
		}
		query := matches[0]
		query.Ordinal = index + 1
		executed[index] = query
	}
	return executed, nil
}

func loadNativeCase(
	runRoot string,
	manifest nativeManifest,
	spec CaseSpec,
	queries QuerySet,
	expected ExpectedResultSet,
) (nativeCaseEvidence, error) {
	evidence, err := inspectNativeCase(runRoot, manifest, spec.InstanceID)
	if err != nil {
		return nativeCaseEvidence{}, err
	}
	if evidence.NativeSHA256 != spec.NativeArtifactSHA256 {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s sha256=%s, want %s",
			spec.InstanceID,
			evidence.NativeSHA256,
			spec.NativeArtifactSHA256,
		)
	}
	if evidence.ResponsesSHA256 != spec.ResponsesArtifactSHA256 {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native responses %s sha256=%s, want %s",
			spec.InstanceID,
			evidence.ResponsesSHA256,
			spec.ResponsesArtifactSHA256,
		)
	}
	if err := equalRecordedQueries(queries.Queries, evidence.Queries); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf(
			"case %s query artifact does not match Native trajectory: %w",
			spec.InstanceID,
			err,
		)
	}
	if err := validateNativeEvidenceAgainstBundle(evidence, expected, spec); err != nil {
		return nativeCaseEvidence{}, fmt.Errorf(
			"Native case %s retrieval provenance: %w",
			spec.InstanceID,
			err,
		)
	}
	return evidence, nil
}

func validateNativeCase(result nativeCaseDocument, manifest nativeManifest, instanceID string) error {
	if result.InstanceID != instanceID {
		return fmt.Errorf("Native case %s has instance_id=%q", instanceID, result.InstanceID)
	}
	if result.Info.RunID != manifest.RunID ||
		result.Info.ObservationCodec != manifest.ObservationCodec ||
		result.Info.SourceRevision != manifest.SourceRevision ||
		result.Info.SourceModified != manifest.SourceModified ||
		result.Info.BinarySHA256 != manifest.BinarySHA256 ||
		result.Info.ModelConfigSHA256 != manifest.ModelConfigSHA256 ||
		result.Info.EnvironmentConfigSHA256 != manifest.EnvironmentConfigSHA256 ||
		result.Info.CasesSHA256 != manifest.CasesSHA256 ||
		result.Info.CommandTimeout != manifest.CommandTimeout ||
		result.Info.CaseTimeout != manifest.CaseTimeout ||
		result.Info.SelectedInstancesSHA256 != manifest.SelectedInstancesSHA256 ||
		result.Info.CleanRoom != manifest.CleanRoom ||
		result.Info.ToolLoopWarning != manifest.ToolLoopWarning ||
		!result.Info.CodeSearch ||
		result.Info.WorkspacePreload != *manifest.WorkspacePreload ||
		result.Info.WorkspaceRepresentation != manifest.WorkspaceRepresentation ||
		result.Info.RepresentationSHA256 != manifest.RepresentationSHA256 ||
		result.Info.EmbeddingConfigSHA256 != manifest.EmbeddingConfigSHA256 ||
		result.Info.CleanRoomPolicySHA256 != manifest.CleanRoomPolicySHA256 ||
		result.Info.ImageSetSHA256 != manifest.ImageSetSHA256 ||
		result.Info.Workers != manifest.Workers {
		return fmt.Errorf("Native case %s identity does not match its run manifest", instanceID)
	}
	if strings.TrimSpace(result.Info.Repo) == "" || strings.TrimSpace(result.Info.BaseCommit) == "" ||
		result.Info.VerifiedBaseCommit != result.Info.BaseCommit {
		return fmt.Errorf("Native case %s has incomplete verified workspace provenance", instanceID)
	}
	if strings.TrimSpace(result.Info.ExitStatus) == "" || result.LLMCalls < 0 || result.ToolCalls < 0 ||
		result.ResponseCount < 0 || !isHexSHA256(result.ResponsesSHA256) {
		return fmt.Errorf("Native case %s has invalid terminal/call/response fields", instanceID)
	}
	return nil
}

func extractRecordedQueries(responses []traceResponse) ([]RecordedQuery, error) {
	var queries []RecordedQuery
	for responseIndex, response := range responses {
		if response.Error != nil || response.IsPartial || !response.Done {
			continue
		}
		for choiceIndex, choice := range response.Choices {
			for callIndex, call := range choice.Message.ToolCalls {
				if call.Function.Name != "code_search" {
					continue
				}
				if strings.TrimSpace(call.ID) == "" {
					return nil, fmt.Errorf(
						"response %d choice %d tool call %d has empty id",
						responseIndex,
						choiceIndex,
						callIndex,
					)
				}
				canonical, err := canonicalJSONObject([]byte(call.Function.Arguments))
				if err != nil {
					return nil, fmt.Errorf("tool call %s arguments: %w", call.ID, err)
				}
				var arguments struct {
					Query string `json:"query"`
				}
				if err := json.Unmarshal(canonical, &arguments); err != nil {
					return nil, fmt.Errorf("tool call %s arguments schema: %w", call.ID, err)
				}
				if strings.TrimSpace(arguments.Query) == "" {
					return nil, fmt.Errorf("tool call %s has empty query", call.ID)
				}
				queries = append(queries, RecordedQuery{
					Ordinal: len(queries) + 1, ToolCallID: call.ID, ToolName: "code_search",
					Query: arguments.Query, QuerySHA256: digestBytes([]byte(arguments.Query)),
					Arguments: canonical, ArgumentsSHA256: digestBytes(canonical),
					NativeArguments:       call.Function.Arguments,
					NativeArgumentsSHA256: digestBytes([]byte(call.Function.Arguments)),
				})
			}
		}
	}
	return queries, nil
}

func equalRecordedQueries(expected, actual []RecordedQuery) error {
	if len(expected) != len(actual) {
		return fmt.Errorf("query counts differ: responses=%d events=%d", len(expected), len(actual))
	}
	for index := range expected {
		left, right := expected[index], actual[index]
		if left.Ordinal != right.Ordinal || left.ToolCallID != right.ToolCallID ||
			left.ToolName != right.ToolName || left.Query != right.Query ||
			left.QuerySHA256 != right.QuerySHA256 || left.ArgumentsSHA256 != right.ArgumentsSHA256 ||
			left.NativeArguments != right.NativeArguments ||
			left.NativeArgumentsSHA256 != right.NativeArgumentsSHA256 ||
			!bytes.Equal(left.Arguments, right.Arguments) {
			return fmt.Errorf("query %d differs", index+1)
		}
	}
	return nil
}

func validateNativeRetrieval(
	result nativeCaseDocument,
	queries []RecordedQuery,
) ([]ExpectedQueryResult, error) {
	if result.WorkspaceIndex == nil {
		return nil, errors.New("workspace_index is missing")
	}
	for name, value := range map[string]string{
		"representation_sha256":    result.WorkspaceIndex.RepresentationSHA256,
		"eligible_file_set_sha256": result.WorkspaceIndex.EligibleFileSetSHA256,
		"eligible_content_sha256":  result.WorkspaceIndex.EligibleContentSHA256,
		"document_set_sha256":      result.WorkspaceIndex.DocumentSetSHA256,
	} {
		if !isHexSHA256(value) {
			return nil, fmt.Errorf("workspace_index has invalid %s", name)
		}
	}
	if strings.TrimSpace(result.WorkspaceIndex.Representation) == "" ||
		strings.TrimSpace(result.WorkspaceIndex.RepresentationSchema) == "" ||
		strings.TrimSpace(result.WorkspaceIndex.RetrievalMode) == "" {
		return nil, errors.New("workspace_index has incomplete representation or retrieval mode")
	}
	if strings.HasPrefix(result.WorkspaceIndex.Representation, "ast-") {
		if strings.TrimSpace(result.WorkspaceIndex.ParserDependency) == "" ||
			!isHexSHA256(result.WorkspaceIndex.ParserRuntimeSHA256) {
			return nil, errors.New("AST workspace_index has incomplete parser provenance")
		}
	} else if result.WorkspaceIndex.ParserDependency != "" ||
		result.WorkspaceIndex.ParserRuntimeSHA256 != "" {
		return nil, errors.New("non-AST workspace_index unexpectedly declares parser provenance")
	}
	if result.CodeSearchCalls != len(queries) || len(result.RetrievalTrace) != len(queries) ||
		len(result.CodeSearchRawResults) != len(queries) {
		return nil, fmt.Errorf(
			"code_search counts calls=%d trace=%d raw=%d queries=%d",
			result.CodeSearchCalls,
			len(result.RetrievalTrace),
			len(result.CodeSearchRawResults),
			len(queries),
		)
	}
	if result.CodeSearchErrors < 0 || result.CodeSearchErrors > result.CodeSearchCalls {
		return nil, fmt.Errorf(
			"code_search_errors=%d is outside [0,%d]",
			result.CodeSearchErrors,
			result.CodeSearchCalls,
		)
	}
	results := make([]ExpectedQueryResult, len(queries))
	errorCount := 0
	resultBytes := 0
	observationBytes := 0
	for index, trace := range result.RetrievalTrace {
		query := queries[index]
		if trace.Call != index+1 || trace.ToolCallID != query.ToolCallID ||
			trace.Query != query.Query || trace.ArgumentsSHA256 != query.NativeArgumentsSHA256 ||
			!isHexSHA256(trace.ResultSHA256) {
			return nil, fmt.Errorf("retrieval_trace entry %d does not match recorded query", index+1)
		}
		rawResult, err := compactJSONValue(result.CodeSearchRawResults[index])
		if err != nil {
			return nil, fmt.Errorf("raw result %d: %w", index+1, err)
		}
		if digestBytes(rawResult) != trace.ResultSHA256 {
			return nil, fmt.Errorf("raw result %d does not match retrieval_trace result_sha256", index+1)
		}
		if trace.ResultBytes != len(rawResult) {
			return nil, fmt.Errorf(
				"raw result %d has %d compact bytes, retrieval_trace records %d",
				index+1,
				len(rawResult),
				trace.ResultBytes,
			)
		}
		resultBytes += trace.ResultBytes
		if trace.ObservationBytes < 0 ||
			(trace.ObservationSHA256 == "" && trace.ObservationBytes != 0) ||
			(trace.ObservationSHA256 != "" &&
				(!isHexSHA256(trace.ObservationSHA256) || trace.ObservationBytes == 0)) {
			return nil, fmt.Errorf("retrieval_trace entry %d has invalid observation provenance", index+1)
		}
		observationBytes += trace.ObservationBytes
		switch trace.Status {
		case "success":
			if trace.Error != "" || trace.ErrorSHA256 != "" {
				return nil, fmt.Errorf("retrieval_trace entry %d success carries an error", index+1)
			}
			if len(trace.Documents) == 0 {
				return nil, fmt.Errorf(
					"retrieval_trace entry %d records zero-document success; the knowledge tool reports this as an error",
					index+1,
				)
			}
		case "error":
			errorCount++
			if trace.Error == "" || trace.ErrorSHA256 != digestBytes([]byte(trace.Error)) {
				return nil, fmt.Errorf("retrieval_trace entry %d has invalid error provenance", index+1)
			}
			if len(trace.Documents) != 0 {
				return nil, fmt.Errorf("retrieval_trace entry %d error carries documents", index+1)
			}
			var envelope struct {
				Error string `json:"error"`
			}
			if err := decodeStrictJSON(rawResult, &envelope); err != nil || envelope.Error != trace.Error {
				return nil, fmt.Errorf("raw error result %d does not match retrieval_trace error", index+1)
			}
		default:
			return nil, fmt.Errorf("retrieval_trace entry %d has unsupported status %q", index+1, trace.Status)
		}
		ranked := make([]RankedDocument, len(trace.Documents))
		for documentIndex, document := range trace.Documents {
			if math.IsNaN(document.Score) || math.IsInf(document.Score, 0) {
				return nil, fmt.Errorf("retrieval_trace entry %d document %d has non-finite score", index+1, documentIndex+1)
			}
			ranked[documentIndex] = RankedDocument{
				Rank: documentIndex + 1, DocumentID: document.ID, SourcePath: document.Path,
				ContentSHA256:  document.ContentSHA256,
				MetadataSHA256: RetrievalMetadataSHA256(document.Path, document.Lines, document.Symbol),
				ScoreBits:      fmt.Sprintf("%016x", math.Float64bits(document.Score)),
			}
		}
		if err := validateRankedDocuments(ranked); err != nil {
			return nil, fmt.Errorf("retrieval_trace entry %d: %w", index+1, err)
		}
		portableStatus := trace.Status
		if trace.Status == "error" {
			portableStatus = classifySearchError(trace.Error)
		}
		fingerprint, err := OutcomeSHA256(
			query.QuerySHA256,
			portableStatus,
			trace.ErrorSHA256,
			ranked,
		)
		if err != nil {
			return nil, err
		}
		results[index] = ExpectedQueryResult{
			Ordinal: query.Ordinal, QuerySHA256: query.QuerySHA256,
			Status: portableStatus, ErrorSHA256: trace.ErrorSHA256,
			ResultSHA256: fingerprint, Documents: ranked,
		}
	}
	if errorCount != result.CodeSearchErrors {
		return nil, fmt.Errorf(
			"code_search_errors=%d, retrieval_trace errors=%d",
			result.CodeSearchErrors,
			errorCount,
		)
	}
	if resultBytes != result.CodeSearchResultBytes ||
		observationBytes != result.CodeSearchObservationBytes {
		return nil, fmt.Errorf(
			"code_search byte totals result=%d/%d observation=%d/%d",
			result.CodeSearchResultBytes,
			resultBytes,
			result.CodeSearchObservationBytes,
			observationBytes,
		)
	}
	return results, nil
}

func validateNativeEvidenceAgainstBundle(
	evidence nativeCaseEvidence,
	expected ExpectedResultSet,
	spec CaseSpec,
) error {
	index := evidence.WorkspaceIndex
	if index.Representation != spec.Retrieval.WorkspaceRepresentation ||
		index.RepresentationSchema != spec.Retrieval.WorkspaceRepresentationSchema ||
		index.RepresentationSHA256 != spec.Retrieval.WorkspaceRepresentationSHA256 ||
		index.EligibleFileSetSHA256 != spec.Retrieval.EligibleFileSetSHA256 ||
		index.EligibleContentSHA256 != spec.Retrieval.EligibleContentSHA256 ||
		index.DocumentSetSHA256 != spec.Retrieval.DocumentSetSHA256 ||
		index.ParserDependency != spec.Retrieval.ParserDependency ||
		index.ParserRuntimeSHA256 != spec.Retrieval.ParserRuntimeSHA256 ||
		index.RetrievalMode != spec.Retrieval.RetrievalMode {
		return errors.New("workspace_index does not match bundle retrieval provenance")
	}
	if len(expected.Results) != len(evidence.Results) {
		return fmt.Errorf(
			"expected result count=%d, Native retrieval count=%d",
			len(expected.Results),
			len(evidence.Results),
		)
	}
	for index, result := range evidence.Results {
		if result.Ordinal != expected.Results[index].Ordinal ||
			result.QuerySHA256 != expected.Results[index].QuerySHA256 ||
			result.Status != expected.Results[index].Status ||
			result.ErrorSHA256 != expected.Results[index].ErrorSHA256 ||
			result.ResultSHA256 != expected.Results[index].ResultSHA256 ||
			!slices.Equal(result.Documents, expected.Results[index].Documents) {
			return fmt.Errorf("expected result %d does not match Native retrieval_trace", index+1)
		}
	}
	return nil
}
