//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

const importSchemaVersion = 1

type importedCase struct {
	SchemaVersion             int                `json:"schema_version"`
	InstanceID                string             `json:"instance_id"`
	Repo                      string             `json:"repo,omitempty"`
	BaseCommit                string             `json:"base_commit,omitempty"`
	VerifiedBaseCommit        string             `json:"verified_base_commit,omitempty"`
	CleanRoom                 bool               `json:"clean_room,omitempty"`
	CleanRoomPolicySHA256     string             `json:"clean_room_policy_sha256,omitempty"`
	OfflineAssetsSHA256       string             `json:"offline_assets_sha256,omitempty"`
	ImageSetSHA256            string             `json:"image_set_sha256,omitempty"`
	EnvironmentProvenance     *sweenv.Provenance `json:"environment_provenance,omitempty"`
	ToolLoopWarning           bool               `json:"tool_loop_warning"`
	CodeSearch                bool               `json:"code_search,omitempty"`
	CodeSearchToolOrder       string             `json:"code_search_tool_order,omitempty"`
	CodeSearchInvocationDedup string             `json:"code_search_invocation_dedup,omitempty"`
	WorkspacePreload          *bool              `json:"workspace_preload,omitempty"`
	WorkspaceRepresentation   string             `json:"workspace_representation,omitempty"`
	RepresentationSchema      string             `json:"workspace_representation_schema,omitempty"`
	RepresentationSHA256      string             `json:"workspace_representation_sha256,omitempty"`
	EmbeddingConfigSHA256     string             `json:"embedding_config_sha256,omitempty"`
	Target                    string             `json:"target"`
	Result                    targetResult       `json:"result"`
}

type targetResult struct {
	MainStatus                  string                       `json:"main_status"`
	FailureReason               string                       `json:"failure_reason,omitempty"`
	ModelNameOrPath             string                       `json:"model_name_or_path,omitempty"`
	PatchPath                   string                       `json:"patch_path,omitempty"`
	TracePath                   string                       `json:"trace_path,omitempty"`
	VerifierResultRef           string                       `json:"verifier_result_ref,omitempty"`
	PatchStats                  artifact.PatchStats          `json:"patch_stats"`
	Usage                       usageStats                   `json:"usage"`
	ToolLoopWarningCount        int                          `json:"tool_loop_warning_count"`
	FirstToolLoopWarningLLMCall *int                         `json:"first_tool_loop_warning_llm_call"`
	ToolLoopWarningLLMCalls     []int                        `json:"tool_loop_warning_llm_calls"`
	CodeSearchCalls             int                          `json:"code_search_calls,omitempty"`
	CodeSearchErrors            int                          `json:"code_search_errors,omitempty"`
	CodeSearchResultBytes       int                          `json:"code_search_result_bytes,omitempty"`
	CodeSearchObservationBytes  int                          `json:"code_search_observation_bytes,omitempty"`
	RetrievalTrace              []nativeRetrievalTraceEntry  `json:"retrieval_trace,omitempty"`
	WorkspaceIndex              *nativeWorkspaceIndexStats   `json:"workspace_index,omitempty"`
	Embedding                   *nativeEmbeddingMetrics      `json:"embedding,omitempty"`
	EmbeddingCache              *nativeEmbeddingCacheMetrics `json:"embedding_cache,omitempty"`
	toolLoopWarningCountSet     bool                         `json:"-"`
	firstToolLoopWarningCallSet bool                         `json:"-"`
	toolLoopWarningCallsSet     bool                         `json:"-"`
}

func (r *targetResult) UnmarshalJSON(data []byte) error {
	type plain targetResult
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*r = targetResult(decoded)
	r.toolLoopWarningCountSet = nonNullJSONField(fields, "tool_loop_warning_count")
	_, r.firstToolLoopWarningCallSet = fields["first_tool_loop_warning_llm_call"]
	r.toolLoopWarningCallsSet = nonNullJSONField(fields, "tool_loop_warning_llm_calls")
	return nil
}

type usageStats struct {
	PromptTokens     int `json:"prompt_tokens"`
	CachedTokens     int `json:"cached_tokens"`
	UncachedTokens   int `json:"uncached_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	ReasoningTokens  int `json:"reasoning_tokens"`
	TotalTokens      int `json:"total_tokens"`
	APICalls         int `json:"api_calls"`
}

type importSummary struct {
	SchemaVersion int            `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Target        string         `json:"target"`
	Total         int            `json:"total"`
	Counts        map[string]int `json:"counts"`
}

func runImport(args []string) error {
	fs := newFlagSet("import")
	target := fs.String("target", "baseline", "path-safe target label")
	casesPath := fs.String("cases", "", "optional canonical cases.jsonl")
	predsPath := fs.String("predictions", "", "runner predictions JSON/JSONL path")
	rawDir := fs.String("raw-dir", "", "runner raw output directory containing per-case trace artifacts")
	shardsManifestPath := fs.String("shards-manifest", "", "optional summarize-shards output path for sharded trajectories")
	harnessReport := fs.String("harness-report", "", "SWE-Bench harness report JSON path")
	output := fs.String("output", "", "normalized output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "predictions", *predsPath); err != nil {
		return err
	}
	if err := required(fs, "output", *output); err != nil {
		return err
	}
	if err := validateTargetLabel(*target); err != nil {
		return err
	}
	if err := ensureDir(*output); err != nil {
		return err
	}

	preds, err := readPredictions(*predsPath)
	if err != nil {
		return err
	}
	selectionFromPredictions := strings.TrimSpace(*casesPath) == ""
	cases, err := readCases(*casesPath, preds)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*casesPath) != "" {
		if err := validateImportInputs(cases, preds); err != nil {
			return err
		}
	}
	traceRawDirs, err := traceRawDirsByCase(*shardsManifestPath)
	if err != nil {
		return err
	}
	harness, err := readHarnessReport(*harnessReport)
	if err != nil {
		return err
	}
	if strings.TrimSpace(*harnessReport) != "" {
		if err := validateHarnessIndex(harness, cases); err != nil {
			return fmt.Errorf("validate harness report: %w", err)
		}
	}

	patchDir := filepath.Join(*output, "patches", *target)
	traceDir := filepath.Join(*output, "traces", *target)
	if err := ensureDir(patchDir); err != nil {
		return err
	}
	if err := ensureDir(traceDir); err != nil {
		return err
	}

	outPath := filepath.Join(*output, "cases.jsonl")
	var rows bytes.Buffer

	summary := importSummary{
		SchemaVersion: importSchemaVersion,
		GeneratedAt:   time.Now().UTC(),
		Target:        *target,
		Total:         len(cases),
		Counts:        map[string]int{},
	}

	for _, c := range cases {
		if err := validateArtifactName("instance id", c.InstanceID); err != nil {
			return err
		}
		pred, hasPred := preds[c.InstanceID]
		result := targetResult{
			PatchStats:              artifact.PatchStats{ChangedFiles: []string{}},
			ToolLoopWarningLLMCalls: []int{},
		}
		var nativeTrace *nativeTraceEnvelope
		if hasPred {
			result.ModelNameOrPath = pred.ModelNameOrPath
			if strings.TrimSpace(pred.ModelPatch) != "" {
				patchPath := filepath.Join(patchDir, c.InstanceID+".patch")
				if err := artifact.WriteFileAtomic(patchPath, []byte(pred.ModelPatch), 0o644); err != nil {
					return err
				}
				result.PatchPath = relPath(*output, patchPath)
				result.PatchStats = artifact.ComputePatchStats(pred.ModelPatch)
			}
			caseRawDir := *rawDir
			if strings.TrimSpace(caseRawDir) == "" {
				caseRawDir = traceRawDirs[c.InstanceID]
			}
			if caseRawDir != "" {
				tracePath, usage, err := copyScrubbedTrace(caseRawDir, traceDir, c.InstanceID)
				if err != nil {
					return fmt.Errorf("copy trace for %s: %w", c.InstanceID, err)
				}
				result.TracePath = relPath(*output, tracePath)
				result.Usage = usage
				nativeTrace, err = importedNativeTrace(caseRawDir, c, selectionFromPredictions)
				if err != nil {
					return fmt.Errorf("import native provenance for %s: %w", c.InstanceID, err)
				}
				if nativeTrace != nil {
					result.ToolLoopWarningCount = nativeTrace.ToolLoopWarningCount
					result.FirstToolLoopWarningLLMCall = cloneInt(nativeTrace.FirstToolLoopWarningLLMCall)
					result.ToolLoopWarningLLMCalls = append([]int{}, nativeTrace.ToolLoopWarningLLMCalls...)
					result.CodeSearchCalls = nativeTrace.CodeSearchCalls
					result.CodeSearchErrors = nativeTrace.CodeSearchErrors
					result.CodeSearchResultBytes = nativeTrace.CodeSearchResultBytes
					result.CodeSearchObservationBytes = nativeTrace.CodeSearchObservationBytes
					result.RetrievalTrace = cloneNativeRetrievalTrace(nativeTrace.RetrievalTrace)
					result.WorkspaceIndex = cloneNativeWorkspaceIndex(nativeTrace.WorkspaceIndex)
					result.Embedding = cloneNativeEmbeddingMetrics(nativeTrace.Embedding)
					result.EmbeddingCache = cloneNativeEmbeddingCacheMetrics(nativeTrace.EmbeddingCache)
				}
			}
		}
		status, reason := classify(c.InstanceID, hasPred, pred.ModelPatch, harness)
		result.MainStatus = status
		result.FailureReason = reason
		if *harnessReport != "" {
			result.VerifierResultRef = absPath(*harnessReport)
		}
		summary.Counts[status]++

		row := importedCase{
			SchemaVersion: importSchemaVersion,
			InstanceID:    c.InstanceID,
			Repo:          c.Repo,
			BaseCommit:    c.BaseCommit,
			Target:        *target,
			Result:        result,
		}
		if nativeTrace != nil {
			nativeInfo := &nativeTrace.Info
			row.ToolLoopWarning = nativeInfo.ToolLoopWarning
			row.CodeSearch = nativeInfo.CodeSearch
			if nativeInfo.CodeSearch {
				row.CodeSearchToolOrder = nativeInfo.CodeSearchToolOrder
				row.CodeSearchInvocationDedup = nativeInfo.CodeSearchInvocationDedup
				row.WorkspacePreload = new(bool)
				*row.WorkspacePreload = nativeInfo.WorkspacePreload
				row.WorkspaceRepresentation = nativeInfo.WorkspaceRepresentation
				row.RepresentationSchema = nativeInfo.WorkspaceRepresentationSchema
				row.RepresentationSHA256 = nativeInfo.RepresentationSHA256
				row.EmbeddingConfigSHA256 = nativeInfo.EmbeddingConfigSHA256
			}
			if selectionFromPredictions {
				row.Repo = nativeInfo.Repo
				row.BaseCommit = nativeInfo.BaseCommit
			}
			if nativeInfo.CleanRoom {
				row.VerifiedBaseCommit = nativeInfo.VerifiedBaseCommit
				row.CleanRoom = true
				row.CleanRoomPolicySHA256 = nativeInfo.CleanRoomPolicySHA256
				row.OfflineAssetsSHA256 = nativeInfo.OfflineAssetsSHA256
				row.ImageSetSHA256 = nativeInfo.ImageSetSHA256
				row.EnvironmentProvenance = cloneEnvironmentProvenance(nativeInfo.EnvironmentProvenance)
			}
		}
		data, err := json.Marshal(row)
		if err != nil {
			return err
		}
		rows.Write(data)
		rows.WriteByte('\n')
	}
	if err := artifact.WriteFileAtomic(outPath, rows.Bytes(), 0o644); err != nil {
		return err
	}
	return writeJSON(filepath.Join(*output, "summary", *target+".json"), summary)
}

func readPredictions(path string) (map[string]contract.Prediction, error) {
	return artifact.ReadPredictions(path)
}

func readCases(path string, preds map[string]contract.Prediction) ([]contract.Case, error) {
	if strings.TrimSpace(path) == "" {
		ids := make([]string, 0, len(preds))
		for id := range preds {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		cases := make([]contract.Case, 0, len(ids))
		for _, id := range ids {
			if err := validateArtifactName("instance id", id); err != nil {
				return nil, err
			}
			cases = append(cases, contract.Case{InstanceID: id})
		}
		return cases, nil
	}
	cases, err := artifact.ReadCasesJSONL(path)
	if err != nil {
		return nil, err
	}
	for _, c := range cases {
		if err := validateArtifactName("instance id", c.InstanceID); err != nil {
			return nil, fmt.Errorf("validate %s: %w", path, err)
		}
	}
	return cases, nil
}

func validateImportInputs(cases []contract.Case, preds map[string]contract.Prediction) error {
	seenCases := map[string]bool{}
	for _, c := range cases {
		if strings.TrimSpace(c.InstanceID) == "" {
			return fmt.Errorf("case with empty instance_id")
		}
		if seenCases[c.InstanceID] {
			return fmt.Errorf("duplicate case instance_id %q", c.InstanceID)
		}
		seenCases[c.InstanceID] = true
	}
	for id := range preds {
		if !seenCases[id] {
			return fmt.Errorf("prediction %q is not present in case manifest", id)
		}
	}
	return nil
}

func traceRawDirsByCase(path string) (map[string]string, error) {
	out := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return out, nil
	}
	var manifest shardsManifest
	if err := readJSONFile(path, &manifest); err != nil {
		return nil, fmt.Errorf("read shards manifest: %w", err)
	}
	for _, shard := range manifest.Shards {
		if strings.TrimSpace(shard.RawDir) == "" {
			continue
		}
		for _, c := range shard.Cases {
			if c.Status == "accepted" && c.InstanceID != "" {
				out[c.InstanceID] = shard.RawDir
			}
		}
	}
	return out, nil
}

func readHarnessReport(path string) (contract.HarnessIndex, error) {
	idx := contract.NewHarnessIndex()
	if strings.TrimSpace(path) == "" {
		return idx, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return idx, err
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return idx, err
	}
	addIDs := func(key string, dst map[string]bool) error {
		value, ok := raw[key]
		if !ok {
			return nil
		}
		var ids []string
		if err := json.Unmarshal(value, &ids); err != nil {
			return fmt.Errorf("field %s: %w", key, err)
		}
		for _, id := range ids {
			if err := validateArtifactName("harness instance id", id); err != nil {
				return fmt.Errorf("field %s: %w", key, err)
			}
			if dst[id] {
				return fmt.Errorf("field %s contains duplicate instance id %q", key, id)
			}
			dst[id] = true
		}
		return nil
	}
	for key, dst := range map[string]map[string]bool{
		"resolved_ids":    idx.Resolved,
		"unresolved_ids":  idx.Unresolved,
		"error_ids":       idx.Errors,
		"empty_patch_ids": idx.EmptyPatch,
		"incomplete_ids":  idx.Incomplete,
		"completed_ids":   idx.Completed,
	} {
		if err := addIDs(key, dst); err != nil {
			return idx, err
		}
	}
	for id, v := range raw {
		var row map[string]any
		if err := json.Unmarshal(v, &row); err == nil {
			resolved, hasResolved := row["resolved"].(bool)
			if resolved {
				idx.Resolved[id] = true
			} else if hasResolved {
				idx.Unresolved[id] = true
			}
		}
	}
	if len(idx.Resolved)+len(idx.Unresolved)+len(idx.Errors)+len(idx.EmptyPatch)+len(idx.Incomplete) == 0 {
		return idx, fmt.Errorf("harness report %s contains no outcomes", path)
	}
	return idx, nil
}

func validateHarnessIndex(index contract.HarnessIndex, cases []contract.Case) error {
	known := make(map[string]struct{}, len(cases))
	for _, c := range cases {
		known[c.InstanceID] = struct{}{}
	}
	outcomes := []struct {
		name string
		ids  map[string]bool
	}{
		{name: "resolved", ids: index.Resolved},
		{name: "unresolved", ids: index.Unresolved},
		{name: "error", ids: index.Errors},
		{name: "empty_patch", ids: index.EmptyPatch},
		{name: "incomplete", ids: index.Incomplete},
	}
	seenOutcomes := make(map[string]string)
	for _, outcome := range outcomes {
		for id := range outcome.ids {
			if previous := seenOutcomes[id]; previous != "" {
				return fmt.Errorf(
					"instance %q appears in multiple terminal outcome sets: %s and %s",
					id,
					previous,
					outcome.name,
				)
			}
			seenOutcomes[id] = outcome.name
			if _, ok := known[id]; !ok {
				return fmt.Errorf("%s instance %q is not present in case manifest", outcome.name, id)
			}
		}
	}
	for id := range index.Completed {
		if _, ok := known[id]; !ok {
			return fmt.Errorf("completed instance %q is not present in case manifest", id)
		}
		if !index.Resolved[id] && !index.Unresolved[id] && !index.Errors[id] {
			return fmt.Errorf("completed instance %q has no resolved, unresolved, or error outcome", id)
		}
	}
	return nil
}

func classify(instanceID string, hasPred bool, patch string, harness contract.HarnessIndex) (string, string) {
	if !hasPred {
		return "incomplete", "missing prediction"
	}
	if strings.TrimSpace(patch) == "" {
		return "empty_patch", "empty model_patch"
	}
	if harness.Errors[instanceID] {
		return "error", "harness error"
	}
	if harness.Resolved[instanceID] {
		return "resolved", ""
	}
	if harness.Unresolved[instanceID] {
		return "unresolved", "failed official harness"
	}
	return "incomplete", "missing harness result"
}

func copyScrubbedTrace(rawDir, traceDir, instanceID string) (string, usageStats, error) {
	src, err := traceSourcePath(rawDir, instanceID)
	if err != nil {
		return "", usageStats{}, err
	}
	data, err := os.ReadFile(src)
	if err != nil {
		return "", usageStats{}, err
	}
	usage := extractUsage(data)
	if strings.HasSuffix(src, ".native.json") {
		usage, err = extractNativeUsage(data, instanceID)
		if err != nil {
			return "", usageStats{}, err
		}
	}
	dst := filepath.Join(traceDir, instanceID+".json")
	if err := artifact.WriteFileAtomic(dst, redactJSONBytes(data), 0o644); err != nil {
		return "", usageStats{}, err
	}
	return dst, usage, nil
}

func traceSourcePath(rawDir, instanceID string) (string, error) {
	caseDir := filepath.Join(rawDir, instanceID)
	candidates := []string{
		filepath.Join(caseDir, instanceID+".traj.json"),
		filepath.Join(caseDir, instanceID+".native.json"),
	}
	found := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if _, err := os.Stat(candidate); err == nil {
			found = append(found, candidate)
		} else if !os.IsNotExist(err) {
			return "", fmt.Errorf("inspect trace candidate %s: %w", candidate, err)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf(
			"trace for %s not found: expected %s or %s",
			instanceID,
			candidates[0],
			candidates[1],
		)
	case 1:
		return found[0], nil
	default:
		return "", fmt.Errorf(
			"ambiguous trace for %s: both %s and %s exist",
			instanceID,
			candidates[0],
			candidates[1],
		)
	}
}

// nativeTraceEnvelope is the validated, framework-native CaseResult boundary.
// Keep it package-private so run-config validation can reuse the exact same
// artifact contract without importing another command's internal package.
type nativeTraceEnvelope struct {
	InstanceID                  string
	Info                        nativeInfoEnvelope
	ModelPatch                  string
	DurationMS                  int64
	LLMCalls                    int
	ToolCalls                   int
	ToolLoopWarningCount        int
	FirstToolLoopWarningLLMCall *int
	ToolLoopWarningLLMCalls     []int
	CodeSearchCalls             int
	CodeSearchErrors            int
	CodeSearchResultBytes       int
	CodeSearchObservationBytes  int
	CodeSearchRawResults        []json.RawMessage
	RetrievalTrace              []nativeRetrievalTraceEntry
	WorkspaceIndex              *nativeWorkspaceIndexStats
	Embedding                   *nativeEmbeddingMetrics
	EmbeddingCache              *nativeEmbeddingCacheMetrics
	Usage                       nativeUsageEnvelope
	ResponseCount               int
	ResponsesSHA256             string
}

type nativeInfoEnvelope struct {
	RunID                         string
	ObservationCodec              string
	SourceRevision                string
	SourceModified                bool
	BinarySHA256                  string
	ModelConfigSHA256             string
	EnvironmentConfigSHA256       string
	CasesSHA256                   string
	CommandTimeout                string
	CaseTimeout                   string
	SelectedInstancesSHA256       string
	CleanRoom                     bool
	CleanRoomDeclared             bool
	ToolLoopWarning               bool
	CodeSearch                    bool
	CodeSearchToolOrder           string
	CodeSearchInvocationDedup     string
	WorkspacePreload              bool
	WorkspacePreloadDeclared      bool
	WorkspaceRepresentation       string
	WorkspaceRepresentationSchema string
	RepresentationSHA256          string
	EmbeddingConfigSHA256         string
	CleanRoomPolicySHA256         string
	OfflineAssetsSHA256           string
	ImageSetSHA256                string
	Repo                          string
	BaseCommit                    string
	VerifiedBaseCommit            string
	EnvironmentProvenance         *sweenv.Provenance
	Workers                       int
	ExitStatus                    string
	Error                         string
	ErrorCategory                 string
	Retryable                     bool
}

type nativeUsageEnvelope struct {
	PromptTokens      int
	CompletionTokens  int
	TotalTokens       int
	PromptDetails     nativePromptDetailsEnvelope
	CompletionDetails nativeCompletionDetailsEnvelope
	TimingInfo        *nativeTimingEnvelope
}

type nativePromptDetailsEnvelope struct {
	CachedTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
}

type nativeCompletionDetailsEnvelope struct {
	ReasoningTokens int
}

type nativeTimingEnvelope struct {
	TimeToFirstToken  int64
	ReasoningDuration int64
}

type nativeTraceJSON struct {
	InstanceID                  *string                      `json:"instance_id"`
	Info                        json.RawMessage              `json:"info"`
	ModelPatch                  *string                      `json:"model_patch"`
	DurationMS                  *int64                       `json:"duration_ms"`
	LLMCalls                    *int                         `json:"llm_calls"`
	ToolCalls                   *int                         `json:"tool_calls"`
	ToolLoopWarningCount        *int                         `json:"tool_loop_warning_count"`
	FirstToolLoopWarningLLMCall *int                         `json:"first_tool_loop_warning_llm_call"`
	ToolLoopWarningLLMCalls     []int                        `json:"tool_loop_warning_llm_calls"`
	CodeSearchCalls             *int                         `json:"code_search_calls,omitempty"`
	CodeSearchErrors            *int                         `json:"code_search_errors,omitempty"`
	CodeSearchResultBytes       *int                         `json:"code_search_result_bytes,omitempty"`
	CodeSearchObservationBytes  *int                         `json:"code_search_observation_bytes,omitempty"`
	CodeSearchRawResults        []json.RawMessage            `json:"code_search_raw_results,omitempty"`
	RetrievalTrace              []nativeRetrievalTraceEntry  `json:"retrieval_trace,omitempty"`
	WorkspaceIndex              *nativeWorkspaceIndexStats   `json:"workspace_index,omitempty"`
	Embedding                   *nativeEmbeddingMetrics      `json:"embedding,omitempty"`
	EmbeddingCache              *nativeEmbeddingCacheMetrics `json:"embedding_cache,omitempty"`
	Usage                       json.RawMessage              `json:"usage"`
	ResponseCount               *int                         `json:"response_count"`
	ResponsesSHA256             *string                      `json:"responses_sha256"`
}

type nativeInfoJSON struct {
	RunID                     string             `json:"run_id,omitempty"`
	ObservationCodec          string             `json:"observation_codec,omitempty"`
	SourceRevision            string             `json:"source_revision,omitempty"`
	SourceModified            bool               `json:"source_modified,omitempty"`
	BinarySHA256              string             `json:"binary_sha256,omitempty"`
	ModelConfigSHA256         string             `json:"model_config_sha256,omitempty"`
	EnvironmentConfigSHA256   string             `json:"environment_config_sha256,omitempty"`
	CasesSHA256               string             `json:"cases_sha256,omitempty"`
	CommandTimeout            string             `json:"command_timeout,omitempty"`
	CaseTimeout               string             `json:"case_timeout,omitempty"`
	SelectedInstancesSHA256   string             `json:"selected_instances_sha256,omitempty"`
	CleanRoom                 *bool              `json:"clean_room,omitempty"`
	ToolLoopWarning           bool               `json:"tool_loop_warning"`
	CodeSearch                *bool              `json:"code_search,omitempty"`
	CodeSearchToolOrder       string             `json:"code_search_tool_order,omitempty"`
	CodeSearchInvocationDedup string             `json:"code_search_invocation_dedup,omitempty"`
	WorkspacePreload          *bool              `json:"workspace_preload,omitempty"`
	WorkspaceRepresentation   string             `json:"workspace_representation,omitempty"`
	RepresentationSHA256      string             `json:"workspace_representation_sha256,omitempty"`
	EmbeddingConfigSHA256     string             `json:"embedding_config_sha256,omitempty"`
	CleanRoomPolicySHA256     string             `json:"clean_room_policy_sha256,omitempty"`
	OfflineAssetsSHA256       string             `json:"offline_assets_sha256,omitempty"`
	ImageSetSHA256            string             `json:"image_set_sha256,omitempty"`
	Repo                      string             `json:"repo,omitempty"`
	BaseCommit                string             `json:"base_commit,omitempty"`
	VerifiedBaseCommit        string             `json:"verified_base_commit,omitempty"`
	EnvironmentProvenance     *sweenv.Provenance `json:"environment_provenance,omitempty"`
	Workers                   *int               `json:"workers"`
	ExitStatus                *string            `json:"exit_status"`
	Error                     string             `json:"error,omitempty"`
	ErrorCategory             string             `json:"error_category,omitempty"`
	Retryable                 bool               `json:"retryable,omitempty"`
}

type nativeUsageJSON struct {
	PromptTokens      *int            `json:"prompt_tokens"`
	CompletionTokens  *int            `json:"completion_tokens"`
	TotalTokens       *int            `json:"total_tokens"`
	PromptDetails     json.RawMessage `json:"prompt_tokens_details"`
	CompletionDetails json.RawMessage `json:"completion_tokens_details"`
	TimingInfo        json.RawMessage `json:"timing_info,omitempty"`
}

type nativePromptDetailsJSON struct {
	CachedTokens        *int `json:"cached_tokens"`
	CacheCreationTokens *int `json:"cache_creation_tokens,omitempty"`
	CacheReadTokens     *int `json:"cache_read_tokens,omitempty"`
}

type nativeCompletionDetailsJSON struct {
	ReasoningTokens *int `json:"reasoning_tokens,omitempty"`
}

type nativeTimingJSON struct {
	TimeToFirstToken  *int64 `json:"time_to_first_token,omitempty"`
	ReasoningDuration *int64 `json:"reasoning_duration,omitempty"`
}

func extractNativeUsage(data []byte, instanceID string) (usageStats, error) {
	trace, err := parseNativeTraceEnvelope(data, instanceID)
	if err != nil {
		return usageStats{}, err
	}
	return usageStats{
		PromptTokens:     trace.Usage.PromptTokens,
		CachedTokens:     trace.Usage.PromptDetails.CachedTokens,
		UncachedTokens:   trace.Usage.PromptTokens - trace.Usage.PromptDetails.CachedTokens,
		CompletionTokens: trace.Usage.CompletionTokens,
		ReasoningTokens:  trace.Usage.CompletionDetails.ReasoningTokens,
		TotalTokens:      trace.Usage.TotalTokens,
		APICalls:         trace.LLMCalls,
	}, nil
}

func parseNativeTraceEnvelope(data []byte, instanceID string) (nativeTraceEnvelope, error) {
	var raw nativeTraceJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nativeTraceEnvelope{}, fmt.Errorf("parse native trace for %s: %w", instanceID, err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nativeTraceEnvelope{}, fmt.Errorf("parse native trace fields for %s: %w", instanceID, err)
	}
	if raw.InstanceID == nil {
		return nativeTraceEnvelope{}, missingNativeFieldError(instanceID, "instance_id")
	}
	if *raw.InstanceID != instanceID {
		return nativeTraceEnvelope{}, fmt.Errorf(
			"native trace instance_id %q does not match %q",
			*raw.InstanceID,
			instanceID,
		)
	}
	for _, field := range []struct {
		name    string
		present bool
	}{
		{"model_patch", raw.ModelPatch != nil},
		{"duration_ms", raw.DurationMS != nil},
		{"llm_calls", raw.LLMCalls != nil},
		{"tool_calls", raw.ToolCalls != nil},
		{"response_count", raw.ResponseCount != nil},
		{"responses_sha256", raw.ResponsesSHA256 != nil},
	} {
		if !field.present {
			return nativeTraceEnvelope{}, missingNativeFieldError(instanceID, field.name)
		}
	}
	if err := requireNativeJSONObject(raw.Info, instanceID, "info"); err != nil {
		return nativeTraceEnvelope{}, err
	}
	if err := requireNativeJSONObject(raw.Usage, instanceID, "usage"); err != nil {
		return nativeTraceEnvelope{}, err
	}

	info, err := parseNativeInfo(raw.Info, instanceID)
	if err != nil {
		return nativeTraceEnvelope{}, err
	}
	if info.ToolLoopWarning {
		for _, field := range []struct {
			name    string
			present bool
		}{
			{"tool_loop_warning_count", raw.ToolLoopWarningCount != nil},
			{"first_tool_loop_warning_llm_call", fields["first_tool_loop_warning_llm_call"] != nil},
			{"tool_loop_warning_llm_calls", raw.ToolLoopWarningLLMCalls != nil},
		} {
			if !field.present {
				return nativeTraceEnvelope{}, missingNativeFieldError(instanceID, field.name)
			}
		}
	}
	usage, err := parseNativeUsage(raw.Usage, instanceID)
	if err != nil {
		return nativeTraceEnvelope{}, err
	}

	intValues := []struct {
		name  string
		value int
	}{
		{"info.workers", info.Workers},
		{"llm_calls", *raw.LLMCalls},
		{"tool_calls", *raw.ToolCalls},
		{"response_count", *raw.ResponseCount},
	}
	for _, value := range intValues {
		if err := rejectNegativeNativeInt(instanceID, value.name, value.value); err != nil {
			return nativeTraceEnvelope{}, err
		}
	}
	if *raw.DurationMS < 0 {
		return nativeTraceEnvelope{}, fmt.Errorf(
			"native trace for %s has negative duration_ms %d",
			instanceID,
			*raw.DurationMS,
		)
	}
	toolLoopWarningCount := 0
	if raw.ToolLoopWarningCount != nil {
		toolLoopWarningCount = *raw.ToolLoopWarningCount
	}
	if err := validateNativeToolLoopWarningTelemetry(
		instanceID,
		info.ToolLoopWarning,
		toolLoopWarningCount,
		raw.FirstToolLoopWarningLLMCall,
		raw.ToolLoopWarningLLMCalls,
		*raw.LLMCalls,
	); err != nil {
		return nativeTraceEnvelope{}, err
	}
	if !isSHA256Hex(*raw.ResponsesSHA256) {
		return nativeTraceEnvelope{}, fmt.Errorf(
			"native trace for %s has invalid responses_sha256 %q: want 64 hexadecimal characters",
			instanceID,
			*raw.ResponsesSHA256,
		)
	}
	codeSearchCalls := optionalIntValue(raw.CodeSearchCalls)
	codeSearchErrors := optionalIntValue(raw.CodeSearchErrors)
	codeSearchResultBytes := optionalIntValue(raw.CodeSearchResultBytes)
	codeSearchObservationBytes := optionalIntValue(raw.CodeSearchObservationBytes)
	trace := nativeTraceEnvelope{
		InstanceID:                  *raw.InstanceID,
		Info:                        info,
		ModelPatch:                  *raw.ModelPatch,
		DurationMS:                  *raw.DurationMS,
		LLMCalls:                    *raw.LLMCalls,
		ToolCalls:                   *raw.ToolCalls,
		ToolLoopWarningCount:        toolLoopWarningCount,
		FirstToolLoopWarningLLMCall: cloneInt(raw.FirstToolLoopWarningLLMCall),
		ToolLoopWarningLLMCalls:     append([]int{}, raw.ToolLoopWarningLLMCalls...),
		CodeSearchCalls:             codeSearchCalls,
		CodeSearchErrors:            codeSearchErrors,
		CodeSearchResultBytes:       codeSearchResultBytes,
		CodeSearchObservationBytes:  codeSearchObservationBytes,
		CodeSearchRawResults:        cloneRawMessages(raw.CodeSearchRawResults),
		RetrievalTrace:              cloneNativeRetrievalTrace(raw.RetrievalTrace),
		WorkspaceIndex:              cloneNativeWorkspaceIndex(raw.WorkspaceIndex),
		Embedding:                   cloneNativeEmbeddingMetrics(raw.Embedding),
		EmbeddingCache:              cloneNativeEmbeddingCacheMetrics(raw.EmbeddingCache),
		Usage:                       usage,
		ResponseCount:               *raw.ResponseCount,
		ResponsesSHA256:             *raw.ResponsesSHA256,
	}
	if err := validateNativeRetrievalTelemetry(instanceID, trace); err != nil {
		return nativeTraceEnvelope{}, err
	}
	return trace, nil
}

func optionalIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func parseNativeInfo(data json.RawMessage, instanceID string) (nativeInfoEnvelope, error) {
	var raw nativeInfoJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nativeInfoEnvelope{}, fmt.Errorf("parse native trace info for %s: %w", instanceID, err)
	}
	if raw.Workers == nil {
		return nativeInfoEnvelope{}, missingNativeFieldError(instanceID, "info.workers")
	}
	if raw.ExitStatus == nil {
		return nativeInfoEnvelope{}, missingNativeFieldError(instanceID, "info.exit_status")
	}
	if *raw.Workers < 1 {
		return nativeInfoEnvelope{}, fmt.Errorf(
			"native trace for %s has non-positive info.workers %d",
			instanceID,
			*raw.Workers,
		)
	}
	if strings.TrimSpace(*raw.ExitStatus) == "" {
		return nativeInfoEnvelope{}, fmt.Errorf(
			"native trace for %s has empty info.exit_status",
			instanceID,
		)
	}
	if err := validateNativeCleanRoomInfo(raw, instanceID); err != nil {
		return nativeInfoEnvelope{}, err
	}
	representationSchema, err := validateNativeTraceRAGInfo(instanceID, raw)
	if err != nil {
		return nativeInfoEnvelope{}, err
	}
	cleanRoom := raw.CleanRoom != nil && *raw.CleanRoom
	codeSearch := raw.CodeSearch != nil && *raw.CodeSearch
	workspacePreload := raw.WorkspacePreload != nil && *raw.WorkspacePreload
	return nativeInfoEnvelope{
		RunID:                         raw.RunID,
		ObservationCodec:              raw.ObservationCodec,
		SourceRevision:                raw.SourceRevision,
		SourceModified:                raw.SourceModified,
		BinarySHA256:                  raw.BinarySHA256,
		ModelConfigSHA256:             raw.ModelConfigSHA256,
		EnvironmentConfigSHA256:       raw.EnvironmentConfigSHA256,
		CasesSHA256:                   raw.CasesSHA256,
		CommandTimeout:                raw.CommandTimeout,
		CaseTimeout:                   raw.CaseTimeout,
		SelectedInstancesSHA256:       raw.SelectedInstancesSHA256,
		CleanRoom:                     cleanRoom,
		CleanRoomDeclared:             raw.CleanRoom != nil,
		ToolLoopWarning:               raw.ToolLoopWarning,
		CodeSearch:                    codeSearch,
		CodeSearchToolOrder:           raw.CodeSearchToolOrder,
		CodeSearchInvocationDedup:     raw.CodeSearchInvocationDedup,
		WorkspacePreload:              workspacePreload,
		WorkspacePreloadDeclared:      raw.WorkspacePreload != nil,
		WorkspaceRepresentation:       raw.WorkspaceRepresentation,
		WorkspaceRepresentationSchema: representationSchema,
		RepresentationSHA256:          raw.RepresentationSHA256,
		EmbeddingConfigSHA256:         raw.EmbeddingConfigSHA256,
		CleanRoomPolicySHA256:         raw.CleanRoomPolicySHA256,
		OfflineAssetsSHA256:           raw.OfflineAssetsSHA256,
		ImageSetSHA256:                raw.ImageSetSHA256,
		Repo:                          raw.Repo,
		BaseCommit:                    raw.BaseCommit,
		VerifiedBaseCommit:            raw.VerifiedBaseCommit,
		EnvironmentProvenance:         cloneEnvironmentProvenance(raw.EnvironmentProvenance),
		Workers:                       *raw.Workers,
		ExitStatus:                    *raw.ExitStatus,
		Error:                         raw.Error,
		ErrorCategory:                 raw.ErrorCategory,
		Retryable:                     raw.Retryable,
	}, nil
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validateNativeToolLoopWarningTelemetry(
	instanceID string,
	enabled bool,
	count int,
	first *int,
	calls []int,
	llmCalls int,
) error {
	if count < 0 {
		return fmt.Errorf("native trace for %s has negative tool_loop_warning_count %d", instanceID, count)
	}
	if len(calls) != count {
		return fmt.Errorf(
			"native trace for %s has %d tool_loop_warning_llm_calls, want count %d",
			instanceID,
			len(calls),
			count,
		)
	}
	if !enabled && count != 0 {
		return fmt.Errorf("native trace for %s has tool-loop warning telemetry with info.tool_loop_warning=false", instanceID)
	}
	if count == 0 {
		if first != nil {
			return fmt.Errorf("native trace for %s has first_tool_loop_warning_llm_call without a warning", instanceID)
		}
		return nil
	}
	if first == nil || *first != calls[0] {
		return fmt.Errorf("native trace for %s has inconsistent first_tool_loop_warning_llm_call", instanceID)
	}
	previous := 0
	for _, call := range calls {
		if call <= previous {
			return fmt.Errorf("native trace for %s tool_loop_warning_llm_calls are not strictly increasing", instanceID)
		}
		if call > llmCalls {
			return fmt.Errorf(
				"native trace for %s has tool-loop warning at LLM call %d beyond llm_calls=%d",
				instanceID,
				call,
				llmCalls,
			)
		}
		previous = call
	}
	return nil
}

func validateNativeCleanRoomInfo(raw nativeInfoJSON, instanceID string) error {
	cleanRoom := raw.CleanRoom != nil && *raw.CleanRoom
	if !cleanRoom {
		for _, field := range []struct {
			name  string
			value string
		}{
			{"info.clean_room_policy_sha256", raw.CleanRoomPolicySHA256},
			{"info.offline_assets_sha256", raw.OfflineAssetsSHA256},
			{"info.image_set_sha256", raw.ImageSetSHA256},
			{"info.verified_base_commit", raw.VerifiedBaseCommit},
		} {
			if field.value != "" {
				return fmt.Errorf("native trace for %s has %s without clean_room=true", instanceID, field.name)
			}
		}
		if raw.EnvironmentProvenance != nil {
			return fmt.Errorf("native trace for %s has info.environment_provenance without clean_room=true", instanceID)
		}
		return nil
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"info.clean_room_policy_sha256", raw.CleanRoomPolicySHA256},
		{"info.image_set_sha256", raw.ImageSetSHA256},
	} {
		if !isSHA256Hex(field.value) {
			return fmt.Errorf("native trace for %s has invalid %s %q", instanceID, field.name, field.value)
		}
	}
	if raw.OfflineAssetsSHA256 != "" && !isSHA256Hex(raw.OfflineAssetsSHA256) {
		return fmt.Errorf(
			"native trace for %s has invalid info.offline_assets_sha256 %q",
			instanceID,
			raw.OfflineAssetsSHA256,
		)
	}
	if strings.TrimSpace(raw.Repo) == "" {
		return missingNativeFieldError(instanceID, "info.repo")
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"info.base_commit", raw.BaseCommit},
		{"info.verified_base_commit", raw.VerifiedBaseCommit},
	} {
		if !isHexIdentifier(field.value, 40) {
			return fmt.Errorf("native trace for %s has invalid %s %q", instanceID, field.name, field.value)
		}
	}
	if raw.BaseCommit != raw.VerifiedBaseCommit {
		return fmt.Errorf(
			"native trace for %s verified base commit %q does not match declared base commit %q",
			instanceID,
			raw.VerifiedBaseCommit,
			raw.BaseCommit,
		)
	}
	if raw.EnvironmentProvenance == nil {
		return missingNativeFieldError(instanceID, "info.environment_provenance")
	}
	return validateCaseEnvironmentProvenance(instanceID, *raw.EnvironmentProvenance, nil)
}

func cloneEnvironmentProvenance(provenance *sweenv.Provenance) *sweenv.Provenance {
	if provenance == nil {
		return nil
	}
	cloned := *provenance
	if provenance.AuxiliaryImages != nil {
		cloned.AuxiliaryImages = make(map[string]sweenv.ImageIdentity, len(provenance.AuxiliaryImages))
		for role, identity := range provenance.AuxiliaryImages {
			cloned.AuxiliaryImages[role] = identity
		}
	}
	return &cloned
}

func importedNativeInfo(
	rawDir string,
	c contract.Case,
	allowTraceCaseMetadata bool,
) (*nativeInfoEnvelope, error) {
	trace, err := importedNativeTrace(rawDir, c, allowTraceCaseMetadata)
	if err != nil || trace == nil {
		return nil, err
	}
	return &trace.Info, nil
}

func importedNativeTrace(
	rawDir string,
	c contract.Case,
	allowTraceCaseMetadata bool,
) (*nativeTraceEnvelope, error) {
	source, err := traceSourcePath(rawDir, c.InstanceID)
	if err != nil {
		return nil, err
	}
	if !strings.HasSuffix(source, ".native.json") {
		return nil, nil
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return nil, err
	}
	trace, err := parseNativeTraceEnvelope(data, c.InstanceID)
	if err != nil {
		return nil, err
	}
	if allowTraceCaseMetadata {
		repoProvided := trace.Info.Repo != ""
		baseCommitProvided := trace.Info.BaseCommit != ""
		if !trace.Info.CleanRoom && !repoProvided && !baseCommitProvided {
			return &trace, nil
		}
		if repoProvided != baseCommitProvided {
			return nil, fmt.Errorf(
				"prediction-defined native import must provide trace info.repo and info.base_commit together",
			)
		}
		if strings.TrimSpace(trace.Info.Repo) == "" {
			return nil, fmt.Errorf("prediction-defined native import requires non-empty trace info.repo")
		}
		if !isHexIdentifier(trace.Info.BaseCommit, 40) {
			return nil, fmt.Errorf(
				"prediction-defined native import requires 40-hex trace info.base_commit, got %q",
				trace.Info.BaseCommit,
			)
		}
		return &trace, nil
	}
	if !trace.Info.CleanRoom {
		return &trace, nil
	}
	if strings.TrimSpace(c.Repo) == "" || !isHexIdentifier(c.BaseCommit, 40) {
		return nil, fmt.Errorf("clean-room native import requires canonical repo/base_commit case metadata")
	}
	if trace.Info.Repo != c.Repo || trace.Info.BaseCommit != c.BaseCommit || trace.Info.VerifiedBaseCommit != c.BaseCommit {
		return nil, fmt.Errorf(
			"trace repo/base/verified %q/%q/%q do not match case metadata %q/%q",
			trace.Info.Repo,
			trace.Info.BaseCommit,
			trace.Info.VerifiedBaseCommit,
			c.Repo,
			c.BaseCommit,
		)
	}
	return &trace, nil
}

func validateCaseEnvironmentProvenance(
	instanceID string,
	provenance sweenv.Provenance,
	images map[string]sweenv.ImageIdentity,
) error {
	expectedReference := sweenv.ImageForInstance(instanceID)
	if provenance.Testbed.Reference != expectedReference {
		return fmt.Errorf(
			"native trace for %s testbed image reference %q does not match %q",
			instanceID,
			provenance.Testbed.Reference,
			expectedReference,
		)
	}
	if _, err := sweenv.ImageSetSHA256(map[string]sweenv.ImageIdentity{
		expectedReference: provenance.Testbed,
	}); err != nil {
		return fmt.Errorf("native trace for %s has invalid testbed image provenance: %w", instanceID, err)
	}
	if images != nil {
		expected, ok := images[expectedReference]
		if !ok || expected != provenance.Testbed {
			return fmt.Errorf(
				"native trace for %s testbed image %+v does not match runner image set %+v",
				instanceID,
				provenance.Testbed,
				expected,
			)
		}
	}

	expectedRoles := expectedAuxiliaryImageRoles(instanceID)
	if len(provenance.AuxiliaryImages) != len(expectedRoles) {
		return fmt.Errorf(
			"native trace for %s has %d auxiliary image roles, want %d",
			instanceID,
			len(provenance.AuxiliaryImages),
			len(expectedRoles),
		)
	}
	for role := range expectedRoles {
		identity, ok := provenance.AuxiliaryImages[role]
		if !ok {
			return fmt.Errorf("native trace for %s is missing auxiliary image role %q", instanceID, role)
		}
		if _, err := sweenv.ImageSetSHA256(map[string]sweenv.ImageIdentity{
			identity.Reference: identity,
		}); err != nil {
			return fmt.Errorf(
				"native trace for %s has invalid auxiliary image role %q: %w",
				instanceID,
				role,
				err,
			)
		}
		switch role {
		case "httpbin":
			if identity.Reference != "docker.io/kennethreitz/httpbin:latest" {
				return fmt.Errorf(
					"native trace for %s httpbin image reference %q does not match the offline fixture",
					instanceID,
					identity.Reference,
				)
			}
		case "network-helper":
			if identity != provenance.Testbed {
				return fmt.Errorf(
					"native trace for %s network-helper image %+v does not match the testbed image %+v",
					instanceID,
					identity,
					provenance.Testbed,
				)
			}
		}
		if images != nil {
			expected, ok := images[identity.Reference]
			if !ok || expected != identity {
				return fmt.Errorf(
					"native trace for %s auxiliary image %q %+v does not match runner image set %+v",
					instanceID,
					role,
					identity,
					expected,
				)
			}
		}
	}
	return nil
}

func expectedAuxiliaryImageRoles(instanceID string) map[string]struct{} {
	roles := map[string]struct{}{}
	if strings.HasPrefix(instanceID, "psf__requests-") {
		roles["httpbin"] = struct{}{}
	}
	switch instanceID {
	case "psf__requests-2317", "psf__requests-2931", "psf__requests-5414", "psf__requests-6028":
		roles["network-helper"] = struct{}{}
	}
	return roles
}

func parseNativeUsage(data json.RawMessage, instanceID string) (nativeUsageEnvelope, error) {
	var raw nativeUsageJSON
	if err := json.Unmarshal(data, &raw); err != nil {
		return nativeUsageEnvelope{}, fmt.Errorf("parse native trace usage for %s: %w", instanceID, err)
	}
	for _, field := range []struct {
		name    string
		present bool
	}{
		{"usage.prompt_tokens", raw.PromptTokens != nil},
		{"usage.completion_tokens", raw.CompletionTokens != nil},
		{"usage.total_tokens", raw.TotalTokens != nil},
	} {
		if !field.present {
			return nativeUsageEnvelope{}, missingNativeFieldError(instanceID, field.name)
		}
	}
	if err := requireNativeJSONObject(
		raw.PromptDetails,
		instanceID,
		"usage.prompt_tokens_details",
	); err != nil {
		return nativeUsageEnvelope{}, err
	}
	if err := requireNativeJSONObject(
		raw.CompletionDetails,
		instanceID,
		"usage.completion_tokens_details",
	); err != nil {
		return nativeUsageEnvelope{}, err
	}

	var promptRaw nativePromptDetailsJSON
	if err := json.Unmarshal(raw.PromptDetails, &promptRaw); err != nil {
		return nativeUsageEnvelope{}, fmt.Errorf(
			"parse native trace prompt token details for %s: %w",
			instanceID,
			err,
		)
	}
	if promptRaw.CachedTokens == nil {
		return nativeUsageEnvelope{}, missingNativeFieldError(
			instanceID,
			"usage.prompt_tokens_details.cached_tokens",
		)
	}
	var completionRaw nativeCompletionDetailsJSON
	if err := json.Unmarshal(raw.CompletionDetails, &completionRaw); err != nil {
		return nativeUsageEnvelope{}, fmt.Errorf(
			"parse native trace completion token details for %s: %w",
			instanceID,
			err,
		)
	}

	prompt := nativePromptDetailsEnvelope{CachedTokens: *promptRaw.CachedTokens}
	if promptRaw.CacheCreationTokens != nil {
		prompt.CacheCreationTokens = *promptRaw.CacheCreationTokens
	}
	if promptRaw.CacheReadTokens != nil {
		prompt.CacheReadTokens = *promptRaw.CacheReadTokens
	}
	completion := nativeCompletionDetailsEnvelope{}
	if completionRaw.ReasoningTokens != nil {
		completion.ReasoningTokens = *completionRaw.ReasoningTokens
	}
	usage := nativeUsageEnvelope{
		PromptTokens:      *raw.PromptTokens,
		CompletionTokens:  *raw.CompletionTokens,
		TotalTokens:       *raw.TotalTokens,
		PromptDetails:     prompt,
		CompletionDetails: completion,
	}
	if len(raw.TimingInfo) > 0 {
		if err := requireNativeJSONObject(raw.TimingInfo, instanceID, "usage.timing_info"); err != nil {
			return nativeUsageEnvelope{}, err
		}
		var timingRaw nativeTimingJSON
		if err := json.Unmarshal(raw.TimingInfo, &timingRaw); err != nil {
			return nativeUsageEnvelope{}, fmt.Errorf(
				"parse native trace timing info for %s: %w",
				instanceID,
				err,
			)
		}
		timing := &nativeTimingEnvelope{}
		if timingRaw.TimeToFirstToken != nil {
			timing.TimeToFirstToken = *timingRaw.TimeToFirstToken
		}
		if timingRaw.ReasoningDuration != nil {
			timing.ReasoningDuration = *timingRaw.ReasoningDuration
		}
		for _, value := range []struct {
			name  string
			value int64
		}{
			{"usage.timing_info.time_to_first_token", timing.TimeToFirstToken},
			{"usage.timing_info.reasoning_duration", timing.ReasoningDuration},
		} {
			if value.value < 0 {
				return nativeUsageEnvelope{}, fmt.Errorf(
					"native trace for %s has negative %s %d",
					instanceID,
					value.name,
					value.value,
				)
			}
		}
		usage.TimingInfo = timing
	}

	for _, value := range []struct {
		name  string
		value int
	}{
		{"usage.prompt_tokens", usage.PromptTokens},
		{"usage.prompt_tokens_details.cached_tokens", usage.PromptDetails.CachedTokens},
		{"usage.prompt_tokens_details.cache_creation_tokens", usage.PromptDetails.CacheCreationTokens},
		{"usage.prompt_tokens_details.cache_read_tokens", usage.PromptDetails.CacheReadTokens},
		{"usage.completion_tokens", usage.CompletionTokens},
		{"usage.completion_tokens_details.reasoning_tokens", usage.CompletionDetails.ReasoningTokens},
		{"usage.total_tokens", usage.TotalTokens},
	} {
		if err := rejectNegativeNativeInt(instanceID, value.name, value.value); err != nil {
			return nativeUsageEnvelope{}, err
		}
	}
	if usage.PromptDetails.CachedTokens > usage.PromptTokens {
		return nativeUsageEnvelope{}, fmt.Errorf(
			"native trace for %s has cached_tokens %d greater than prompt_tokens %d",
			instanceID,
			usage.PromptDetails.CachedTokens,
			usage.PromptTokens,
		)
	}
	return usage, nil
}

func rejectNegativeNativeInt(instanceID, field string, value int) error {
	if value < 0 {
		return fmt.Errorf("native trace for %s has negative %s %d", instanceID, field, value)
	}
	return nil
}

func isSHA256Hex(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func missingNativeFieldError(instanceID, field string) error {
	return fmt.Errorf("native trace for %s is missing required field %s", instanceID, field)
}

func requireNativeJSONObject(data json.RawMessage, instanceID, field string) error {
	if len(data) == 0 {
		return missingNativeFieldError(instanceID, field)
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return fmt.Errorf(
			"native trace for %s required field %s must be a JSON object",
			instanceID,
			field,
		)
	}
	return nil
}

func extractUsage(data []byte) usageStats {
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return usageStats{}
	}
	stats := usageStats{}
	walkUsage(v, &stats)
	stats.UncachedTokens = stats.PromptTokens - stats.CachedTokens
	if stats.UncachedTokens < 0 {
		stats.UncachedTokens = 0
	}
	return stats
}

func walkUsage(v any, stats *usageStats) {
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			switch strings.ToLower(k) {
			case "api_calls", "llm_calls":
				stats.APICalls = maxInt(stats.APICalls, jsonNumberToInt(val))
			case "prompt_tokens":
				stats.PromptTokens = maxInt(stats.PromptTokens, jsonNumberToInt(val))
			case "cached_tokens":
				stats.CachedTokens = maxInt(stats.CachedTokens, jsonNumberToInt(val))
			case "completion_tokens":
				stats.CompletionTokens = maxInt(stats.CompletionTokens, jsonNumberToInt(val))
			case "reasoning_tokens":
				stats.ReasoningTokens = maxInt(stats.ReasoningTokens, jsonNumberToInt(val))
			case "total_tokens":
				stats.TotalTokens = maxInt(stats.TotalTokens, jsonNumberToInt(val))
			default:
				walkUsage(val, stats)
			}
		}
	case []any:
		for _, elem := range x {
			walkUsage(elem, stats)
		}
	}
}

func jsonNumberToInt(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	default:
		return 0
	}
}

func maxInt(a, b int) int {
	if b > a {
		return b
	}
	return a
}

func relPath(base, target string) string {
	rel, err := filepath.Rel(base, target)
	if err != nil {
		return target
	}
	return rel
}
