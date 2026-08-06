//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package loopreplay replays the runtime tool-loop detector over persisted
// warning-off framework-native SWE-Bench trajectories without changing them.
package loopreplay

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/toolloop"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

const (
	reportSchemaVersion       = "tool-loop-shadow-replay-v1"
	inputFingerprintV1        = "tool-loop-shadow-replay-input-v1"
	nativeCaseIdentityFormat  = "native-case-identity-v1"
	legacyTagManifestFormat   = "tag-runner-manifest-v12"
	legacyTagManifestFilename = "tag-runner-manifest.json"
	legacyTagCaseCount        = 500
	toolLoopDetectedMarker    = "<tool_loop_detected>"
)

var (
	instanceIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	maxLLMCallsErrorPattern = regexp.MustCompile(`^max LLM calls \([1-9][0-9]*\) exceeded$`)
)

// Report is one deterministic, auditable offline replay result.
type Report struct {
	SchemaVersion       string           `json:"schema_version"`
	Detector            DetectorMetadata `json:"detector"`
	Input               InputMetadata    `json:"input"`
	CasesScanned        int              `json:"cases_scanned"`
	WouldWarnCaseCount  int              `json:"would_warn_case_count"`
	WouldWarnEventCount int              `json:"would_warn_event_count"`
	Cases               []CaseReport     `json:"cases"`
}

// DetectorMetadata pins the exact shared detector used by both runtime and
// replay. Observed event indexes in this report are one-based; an unobserved
// timeout-side injection has a null event index and an explicit false marker.
type DetectorMetadata struct {
	Algorithm      string `json:"algorithm"`
	Version        string `json:"version"`
	Warning        string `json:"warning"`
	EventIndexBase int    `json:"event_index_base"`
}

// InputMetadata fingerprints every artifact admitted at the replay boundary.
type InputMetadata struct {
	Kind            string                `json:"kind"`
	ArtifactPattern string                `json:"artifact_pattern"`
	RunIdentity     RunIdentity           `json:"run_identity"`
	ManifestPath    string                `json:"manifest_path,omitempty"`
	ManifestSHA256  string                `json:"manifest_sha256,omitempty"`
	SHA256          string                `json:"sha256"`
	Artifacts       []ArtifactFingerprint `json:"artifacts"`
}

// RunIdentity is the runner resume identity shared by every admitted case.
// Per-case repository, base-commit, status, and error fields are deliberately
// excluded because they are not run-wide configuration.
type RunIdentity struct {
	ArtifactFormat          string `json:"artifact_format"`
	RunID                   string `json:"run_id"`
	ObservationCodec        string `json:"observation_codec"`
	FrameworkRevision       string `json:"framework_revision,omitempty"`
	UpstreamCommit          string `json:"upstream_commit,omitempty"`
	SourceRevision          string `json:"source_revision,omitempty"`
	SourceModified          bool   `json:"source_modified"`
	BinarySHA256            string `json:"binary_sha256"`
	ModelConfigSHA256       string `json:"model_config_sha256"`
	EnvironmentConfigSHA256 string `json:"environment_config_sha256"`
	CasesSHA256             string `json:"cases_sha256"`
	CommandTimeout          string `json:"command_timeout"`
	CaseTimeout             string `json:"case_timeout"`
	SelectedInstancesSHA256 string `json:"selected_instances_sha256"`
	// CleanRoom is nil for the frozen V12 tag layout because that legacy
	// manifest did not directly attest clean-room provenance.
	CleanRoom             *bool  `json:"clean_room"`
	ToolLoopWarning       bool   `json:"tool_loop_warning"`
	CleanRoomPolicySHA256 string `json:"clean_room_policy_sha256,omitempty"`
	OfflineAssetsSHA256   string `json:"offline_assets_sha256,omitempty"`
	ImageSetSHA256        string `json:"image_set_sha256,omitempty"`
	Workers               int    `json:"workers"`
}

// ArtifactFingerprint binds one admitted trajectory to its detached model
// response artifact without relying on host-specific absolute paths.
type ArtifactFingerprint struct {
	InstanceID       string `json:"instance_id"`
	TrajectoryKind   string `json:"trajectory_kind"`
	TrajectoryPath   string `json:"trajectory_path"`
	TrajectorySHA256 string `json:"trajectory_sha256"`
	ResponsesPath    string `json:"responses_path"`
	ResponsesSHA256  string `json:"responses_sha256"`
}

// CaseReport contains every warning the enabled runtime would have injected,
// plus terminal and call-count summaries after the first injection point.
type CaseReport struct {
	InstanceID               string            `json:"instance_id"`
	WarningCount             int               `json:"warning_count"`
	FirstWarningLLMCall      int               `json:"first_warning_llm_call,omitempty"`
	WarningLLMCalls          []int             `json:"warning_llm_calls"`
	Warnings                 []WarningEvent    `json:"warnings"`
	FormatRecoveryResetCount int               `json:"format_recovery_reset_count"`
	PostFirstWarningTrace    TrajectorySummary `json:"post_first_warning_trajectory"`
}

// WarningEvent identifies one completed repeat and the next real model call
// before which the warning would have been injected.
type WarningEvent struct {
	RepeatCompletedEventIndex   int    `json:"repeat_completed_event_index"`
	WouldInjectBeforeEventIndex *int   `json:"would_inject_before_event_index"`
	WouldInjectBeforeLLMCall    int    `json:"would_inject_before_llm_call"`
	InjectionEventObserved      bool   `json:"injection_event_observed"`
	BatchSHA256                 string `json:"batch_sha256"`
	ToolCount                   int    `json:"tool_count"`
}

// TrajectorySummary keeps quality and length fields separate from detection.
type TrajectorySummary struct {
	TerminalStatus             string `json:"terminal_status"`
	ErrorCategory              string `json:"error_category,omitempty"`
	Submitted                  bool   `json:"submitted"`
	PatchBytes                 int    `json:"patch_bytes"`
	TotalLLMCalls              int    `json:"total_llm_calls"`
	TotalToolCalls             int    `json:"total_tool_calls"`
	LLMCallsAfterFirstWarning  *int   `json:"llm_calls_after_first_warning,omitempty"`
	ToolCallsAfterFirstWarning *int   `json:"tool_calls_after_first_warning,omitempty"`
}

type nativeInfo struct {
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
	CleanRoomPolicySHA256   string `json:"clean_room_policy_sha256"`
	OfflineAssetsSHA256     string `json:"offline_assets_sha256"`
	ImageSetSHA256          string `json:"image_set_sha256"`
	Workers                 int    `json:"workers"`
	ExitStatus              string `json:"exit_status"`
	Error                   string `json:"error"`
	ErrorCategory           string `json:"error_category"`
	Retryable               bool   `json:"retryable"`
}

type nativeResult struct {
	InstanceID                  string            `json:"instance_id"`
	Info                        nativeInfo        `json:"info"`
	ModelPatch                  string            `json:"model_patch"`
	LLMCalls                    int               `json:"llm_calls"`
	ToolCalls                   int               `json:"tool_calls"`
	ToolLoopWarningCount        int               `json:"tool_loop_warning_count"`
	FirstToolLoopWarningLLMCall *int              `json:"first_tool_loop_warning_llm_call"`
	ToolLoopWarningLLMCalls     []int             `json:"tool_loop_warning_llm_calls"`
	Events                      []*event.Event    `json:"events"`
	ResponseCount               int               `json:"response_count"`
	ResponsesSHA256             string            `json:"responses_sha256"`
	Responses                   []*model.Response `json:"-"`
	responsesLoaded             bool              `json:"-"`
	modernNative                bool              `json:"-"`
}

type loadedCase struct {
	result      nativeResult
	fingerprint ArtifactFingerprint
}

type loadedRun struct {
	cases           []loadedCase
	identity        RunIdentity
	kind            string
	artifactPattern string
	manifestPath    string
	manifestSHA256  string
}

// tagManifest is the frozen V12 run-wide identity. Unlike current native
// artifacts, legacy .tag.json cases do not repeat this identity per case.
type tagManifest struct {
	RunID                         string         `json:"run_id"`
	RunnerType                    string         `json:"runner_type"`
	FrameworkVersion              string         `json:"framework_version"`
	FrameworkRevision             string         `json:"framework_revision"`
	AgentProtocol                 string         `json:"agent_protocol"`
	UpstreamCommit                string         `json:"upstream_commit"`
	ObservationCodec              string         `json:"observation_codec"`
	SourceRevision                string         `json:"source_revision,omitempty"`
	SourceModified                bool           `json:"source_modified"`
	BinarySHA256                  string         `json:"binary_sha256"`
	CasesSHA256                   string         `json:"cases_sha256"`
	CaseList                      string         `json:"case_list,omitempty"`
	CaseListSHA256                string         `json:"case_list_sha256,omitempty"`
	SelectedCaseSetSHA256         string         `json:"selected_case_set_sha256"`
	ModelConfigSHA256             string         `json:"model_config_sha256"`
	EmbeddingConfigSHA256         string         `json:"embedding_config_sha256,omitempty"`
	EnvironmentConfigSHA256       string         `json:"environment_config_sha256"`
	DurationMS                    int64          `json:"duration_ms"`
	Cases                         string         `json:"cases"`
	Predictions                   string         `json:"predictions"`
	Progress                      string         `json:"progress"`
	Filter                        string         `json:"filter,omitempty"`
	CaseCount                     int            `json:"case_count"`
	AttemptedCount                int            `json:"attempted_count"`
	SkippedExisting               int            `json:"skipped_existing"`
	PredictionCount               int            `json:"prediction_count"`
	Workers                       int            `json:"workers"`
	RedoExisting                  bool           `json:"redo_existing"`
	CommandTimeout                string         `json:"command_timeout"`
	CaseTimeout                   string         `json:"case_timeout"`
	CodeSearch                    bool           `json:"code_search"`
	ToolLoopWarning               bool           `json:"tool_loop_warning"`
	ToolLoopWarningCount          int            `json:"tool_loop_warning_count"`
	ToolLoopWarningCaseCount      int            `json:"tool_loop_warning_case_count"`
	WorkspacePreload              bool           `json:"workspace_preload"`
	WorkspaceRepresentation       string         `json:"workspace_representation"`
	WorkspaceRepresentationSchema string         `json:"workspace_representation_schema"`
	WorkspaceRepresentationSHA256 string         `json:"workspace_representation_sha256"`
	ExitStatusCounts              map[string]int `json:"exit_status_counts"`
	LLMCalls                      int            `json:"llm_calls"`
	ToolCalls                     int            `json:"tool_calls"`
	Usage                         map[string]int `json:"usage"`
	Status                        string         `json:"status"`
}

type expectedTool struct {
	id   string
	name string
}

type pendingBatch struct {
	expected []expectedTool
	entries  []toolloop.Entry
	next     int
}

type pendingWarning struct {
	repeatEventIndex int
	batchSHA256      string
	toolCount        int
}

type replayState struct {
	detector              toolloop.Detector
	batch                 *pendingBatch
	pendingWarning        *pendingWarning
	llmCalls              int
	totalLLMCalls         int
	totalToolCalls        int
	inModelCall           bool
	toolResults           int
	submissionToolGap     int
	submissionStopSeen    bool
	modernNative          bool
	terminalStatus        string
	terminalError         string
	terminalErrorCategory string
	terminalRetryable     bool
	firstToolsSeen        int
	warnings              []WarningEvent
	responses             []*model.Response
	responseCursor        int
	responsesLoaded       bool
	formatRecoveryResets  int
	observationCodec      observation.ObservationCodec
}

// Run parses the command line, replays run-dir, and atomically writes output.
func Run(args []string) error {
	if len(args) == 0 {
		return errors.New("tool-loop-shadow-replay argv is empty")
	}
	flags := flag.NewFlagSet(filepath.Base(args[0]), flag.ContinueOnError)
	runDir := flags.String("run-dir", "", "framework-native warning-off run output directory")
	output := flags.String("output", "", "shadow replay JSON output")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	if strings.TrimSpace(*runDir) == "" {
		return errors.New("--run-dir is required")
	}
	if strings.TrimSpace(*output) == "" {
		return errors.New("--output is required")
	}
	if err := rejectOutputInsideRunDir(*runDir, *output); err != nil {
		return err
	}
	report, err := Scan(*runDir)
	if err != nil {
		return err
	}
	if err := artifact.WriteJSON(*output, report); err != nil {
		return fmt.Errorf("write shadow replay output: %w", err)
	}
	return nil
}

func rejectOutputInsideRunDir(runDir, output string) error {
	root, err := physicalPath(runDir)
	if err != nil {
		return fmt.Errorf("resolve physical run directory: %w", err)
	}
	destination, err := physicalPath(output)
	if err != nil {
		return fmt.Errorf("resolve physical output path: %w", err)
	}
	relative, err := filepath.Rel(root, destination)
	if err != nil {
		return fmt.Errorf("compare output and run directory: %w", err)
	}
	if relative == "." || (relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)) {
		return fmt.Errorf("--output must be outside --run-dir (resolved output %s)", destination)
	}
	return nil
}

func physicalPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	current := filepath.Clean(absolute)
	missing := make([]string, 0)
	for {
		if _, err := os.Lstat(current); err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for i := len(missing) - 1; i >= 0; i-- {
				resolved = filepath.Join(resolved, missing[i])
			}
			return filepath.Clean(resolved), nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", fmt.Errorf("no existing ancestor for %s", path)
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
}

// Scan validates and replays every instance bundle in a supported warning-off
// run directory. It accepts either current per-case native identity artifacts
// or the frozen V12 tag layout with one root manifest, never a mixture.
func Scan(runDir string) (Report, error) {
	report := Report{
		SchemaVersion: reportSchemaVersion,
		Detector: DetectorMetadata{
			Algorithm: toolloop.Algorithm, Version: toolloop.Version,
			Warning: toolloop.Warning, EventIndexBase: 1,
		},
		Input: InputMetadata{Artifacts: []ArtifactFingerprint{}},
		Cases: []CaseReport{},
	}
	loaded, err := loadRun(runDir)
	if err != nil {
		return Report{}, err
	}
	report.Input.Kind = loaded.kind
	report.Input.ArtifactPattern = loaded.artifactPattern
	report.Input.RunIdentity = loaded.identity
	report.Input.ManifestPath = loaded.manifestPath
	report.Input.ManifestSHA256 = loaded.manifestSHA256
	inputHash := sha256.New()
	_, _ = io.WriteString(inputHash, inputFingerprintV1+"\n")
	_, _ = io.WriteString(inputHash, "format\n"+loaded.identity.ArtifactFormat+"\n")
	if loaded.manifestSHA256 != "" {
		_, _ = io.WriteString(inputHash, "manifest\n"+loaded.manifestPath+"\n")
		_, _ = io.WriteString(inputHash, loaded.manifestSHA256+"\n")
	}
	for _, one := range loaded.cases {
		fingerprint := one.fingerprint
		report.Input.Artifacts = append(report.Input.Artifacts, fingerprint)
		_, _ = io.WriteString(inputHash, fingerprint.InstanceID+"\n")
		_, _ = io.WriteString(inputHash, fingerprint.TrajectoryKind+"\n")
		_, _ = io.WriteString(inputHash, fingerprint.TrajectoryPath+"\n")
		_, _ = io.WriteString(inputHash, fingerprint.TrajectorySHA256+"\n")
		_, _ = io.WriteString(inputHash, fingerprint.ResponsesPath+"\n")
		_, _ = io.WriteString(inputHash, fingerprint.ResponsesSHA256+"\n")

		caseReport, err := replayCase(one.result)
		if err != nil {
			return Report{}, fmt.Errorf("replay %s: %w", one.result.InstanceID, err)
		}
		report.Cases = append(report.Cases, caseReport)
		if caseReport.WarningCount > 0 {
			report.WouldWarnCaseCount++
			report.WouldWarnEventCount += caseReport.WarningCount
		}
	}
	report.Input.SHA256 = hex.EncodeToString(inputHash.Sum(nil))
	report.CasesScanned = len(report.Cases)
	return report, nil
}

func loadRun(runDir string) (loadedRun, error) {
	rootInfo, err := os.Lstat(runDir)
	if err != nil {
		return loadedRun{}, fmt.Errorf("inspect run directory: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return loadedRun{}, fmt.Errorf("run directory %s must be a real directory, not a symlink", runDir)
	}
	manifestPath, err := boundedJoin(runDir, legacyTagManifestFilename)
	if err != nil {
		return loadedRun{}, err
	}
	if _, err := os.Lstat(manifestPath); err == nil {
		return loadTagRun(runDir, manifestPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return loadedRun{}, fmt.Errorf("inspect tag runner manifest: %w", err)
	}
	cases, err := loadCases(runDir)
	if err != nil {
		return loadedRun{}, err
	}
	if len(cases) == 0 {
		return loadedRun{}, fmt.Errorf("run directory %s contains no native case artifacts", runDir)
	}
	return loadedRun{
		cases:    cases,
		identity: runIdentityFrom(cases[0].result.Info),
		kind:     "framework-native-warning-off-run-directory",
		artifactPattern: "<instance_id>/<instance_id>.native.json + " +
			"<instance_id>/<instance_id>.responses.json",
	}, nil
}

// replayCase applies the shared detector to one already-decoded native case.
// It is intentionally kept deterministic and free of filesystem state.
func replayCase(result nativeResult) (CaseReport, error) {
	if result.modernNative && result.Info.ExitStatus == "Submitted" &&
		(result.Info.Error != "" || result.Info.ErrorCategory != "" || result.Info.Retryable) {
		return CaseReport{}, errors.New("modern Submitted trajectory has inconsistent terminal error metadata")
	}
	state := replayState{
		warnings: []WarningEvent{}, responses: result.Responses,
		responsesLoaded: result.responsesLoaded, totalLLMCalls: result.LLMCalls,
		totalToolCalls: result.ToolCalls, modernNative: result.modernNative,
		terminalStatus: result.Info.ExitStatus, terminalError: result.Info.Error,
		terminalErrorCategory: result.Info.ErrorCategory,
		terminalRetryable:     result.Info.Retryable,
		observationCodec:      observation.ObservationCodec(result.Info.ObservationCodec),
	}
	for index, evt := range result.Events {
		if err := state.observeEvent(index+1, evt); err != nil {
			return CaseReport{}, err
		}
	}
	if err := state.finishAlignment(result.Info.ErrorCategory); err != nil {
		return CaseReport{}, err
	}
	if result.modernNative && result.Info.ExitStatus == "Submitted" && !state.submissionStopSeen {
		return CaseReport{}, errors.New("modern Submitted trajectory has no exact submission StopError boundary")
	}
	if result.responsesLoaded && state.toolResults+state.submissionToolGap != result.ToolCalls {
		return CaseReport{}, fmt.Errorf(
			"reconstructed tool results=%d plus terminal submission gap=%d does not match native tool_calls=%d",
			state.toolResults, state.submissionToolGap, result.ToolCalls,
		)
	}
	report := CaseReport{
		InstanceID:               result.InstanceID,
		WarningCount:             len(state.warnings),
		WarningLLMCalls:          []int{},
		Warnings:                 state.warnings,
		FormatRecoveryResetCount: state.formatRecoveryResets,
		PostFirstWarningTrace: TrajectorySummary{
			TerminalStatus: result.Info.ExitStatus,
			ErrorCategory:  result.Info.ErrorCategory,
			Submitted:      result.Info.ExitStatus == "Submitted",
			PatchBytes:     len(result.ModelPatch),
			TotalLLMCalls:  result.LLMCalls,
			TotalToolCalls: result.ToolCalls,
		},
	}
	for _, warning := range state.warnings {
		report.WarningLLMCalls = append(report.WarningLLMCalls, warning.WouldInjectBeforeLLMCall)
	}
	if len(state.warnings) > 0 {
		first := state.warnings[0].WouldInjectBeforeLLMCall
		report.FirstWarningLLMCall = first
		llmAfter := clampNonnegative(result.LLMCalls - first + 1)
		toolAfter := clampNonnegative(result.ToolCalls - state.firstToolsSeen)
		report.PostFirstWarningTrace.LLMCallsAfterFirstWarning = &llmAfter
		report.PostFirstWarningTrace.ToolCallsAfterFirstWarning = &toolAfter
	}
	return report, nil
}

func (s *replayState) observeEvent(index int, evt *event.Event) error {
	if evt == nil || evt.Response == nil {
		return nil
	}
	response := evt.Response
	if s.submissionStopSeen && isReplaySemanticResponse(response) {
		return fmt.Errorf("event %d follows the terminal submission StopError boundary", index)
	}
	if response.Object == model.ObjectTypeToolResponse {
		return s.observeToolResponse(index, evt)
	}
	if !isModelResponse(response) {
		if response.Error != nil {
			s.reset()
		}
		return nil
	}
	if isMaxLLMCallsStopEvent(response) {
		if s.inModelCall || s.llmCalls != s.totalLLMCalls {
			return fmt.Errorf(
				"event %d max-call StopError is inconsistent with reconstructed llm_calls=%d and native llm_calls=%d",
				index, s.llmCalls, s.totalLLMCalls,
			)
		}
		s.reset()
		return nil
	}
	if s.modernNative && isSubmissionStopEvent(response) {
		return s.observeSubmissionStopBoundary(index)
	}
	if response.Object == model.ObjectTypeError {
		return s.observeErrorBoundary(index, response)
	}
	effective, aligned := s.alignModelResponse(response)
	if !aligned {
		return fmt.Errorf("event %d does not align with detached response %d", index, s.responseCursor+1)
	}
	response = effective
	if !s.inModelCall {
		if err := s.startModelCall(index); err != nil {
			return err
		}
		s.inModelCall = true
	}
	if response.Error != nil {
		// Runtime AfterModel resets the detector before considering partial or
		// Done=false status. A non-terminal error still belongs to the current
		// framework response sequence and therefore cannot create a second LLM
		// call when its terminal response arrives.
		s.reset()
		if response.Done {
			s.inModelCall = false
		}
		return nil
	}
	if response.IsPartial || !response.Done {
		return nil
	}
	s.inModelCall = false

	if len(response.Choices) == 0 {
		s.reset()
		return nil
	}
	calls := response.Choices[0].Message.ToolCalls
	if _, err := protocol.ParseActions(calls); err != nil {
		s.reset()
		return nil
	}
	if s.batch != nil {
		// A new model response before the prior result batch completed is an
		// incomplete association, just like the runtime callback boundary.
		s.reset()
	}
	expected := make([]expectedTool, len(calls))
	for i, call := range calls {
		expected[i] = expectedTool{id: call.ID, name: call.Function.Name}
	}
	s.batch = &pendingBatch{
		expected: expected,
		entries:  make([]toolloop.Entry, 0, len(expected)),
	}
	return nil
}

func (s *replayState) observeSubmissionStopBoundary(index int) error {
	if !s.modernNative {
		return fmt.Errorf("event %d has a current-Native submission StopError outside a current-Native artifact", index)
	}
	if s.terminalStatus != "Submitted" || s.terminalError != "" ||
		s.terminalErrorCategory != "" || s.terminalRetryable {
		return fmt.Errorf("event %d submission StopError is inconsistent with terminal metadata", index)
	}
	if s.inModelCall || s.llmCalls != s.totalLLMCalls {
		return fmt.Errorf(
			"event %d submission StopError is inconsistent with reconstructed llm_calls=%d and native llm_calls=%d",
			index, s.llmCalls, s.totalLLMCalls,
		)
	}
	if s.batch == nil || s.batch.next >= len(s.batch.expected) {
		return fmt.Errorf("event %d submission StopError has no incomplete final tool batch", index)
	}
	gap := s.totalToolCalls - s.toolResults
	remaining := len(s.batch.expected) - s.batch.next
	if gap < 1 || gap > remaining {
		return fmt.Errorf(
			"event %d submission tool gap=%d is outside final pending batch remaining=%d",
			index, gap, remaining,
		)
	}
	s.submissionToolGap = gap
	s.submissionStopSeen = true
	// The runtime resets the detector in AfterTool before returning the
	// submission StopError. The framework then discards the entire merged
	// tool-response event for this incomplete final batch.
	s.reset()
	return nil
}

func (s *replayState) startModelCall(index int) error {
	if s.llmCalls >= s.totalLLMCalls {
		return fmt.Errorf(
			"event %d starts model call %d beyond native llm_calls=%d",
			index, s.llmCalls+1, s.totalLLMCalls,
		)
	}
	s.llmCalls++
	s.consumePendingWarning(index, s.llmCalls)
	return nil
}

func (s *replayState) consumePendingWarning(index, llmCall int) {
	if s.pendingWarning == nil {
		return
	}
	var eventIndex *int
	if index > 0 {
		eventIndex = &index
	}
	warning := WarningEvent{
		RepeatCompletedEventIndex:   s.pendingWarning.repeatEventIndex,
		WouldInjectBeforeEventIndex: eventIndex,
		WouldInjectBeforeLLMCall:    llmCall,
		InjectionEventObserved:      index > 0,
		BatchSHA256:                 s.pendingWarning.batchSHA256,
		ToolCount:                   s.pendingWarning.toolCount,
	}
	if len(s.warnings) == 0 {
		s.firstToolsSeen = s.toolResults
	}
	s.warnings = append(s.warnings, warning)
	s.pendingWarning = nil
}

// observeErrorBoundary distinguishes model failures from framework, tool, and
// pre-BeforeModel terminal errors using the authoritative native call total.
// Multiple response-less callbacks may collapse into one terminal error; a
// pending warning belongs to the first such BeforeModel call.
func (s *replayState) observeErrorBoundary(index int, response *model.Response) error {
	sawDetached := false
	if s.responsesLoaded {
		if s.responseCursor < len(s.responses) {
			original := s.responses[s.responseCursor]
			if responsesEqual(response, original) {
				s.responseCursor++
				sawDetached = true
			}
		}
	}
	missingCalls := s.totalLLMCalls - s.llmCalls
	if s.inModelCall {
		if missingCalls != 0 {
			return fmt.Errorf(
				"event %d terminates an active model call with %d unaccounted native calls",
				index, missingCalls,
			)
		}
		s.inModelCall = false
	} else if missingCalls > 0 {
		s.consumePendingWarning(index, s.llmCalls+1)
		s.llmCalls = s.totalLLMCalls
	} else if missingCalls < 0 {
		return fmt.Errorf("event %d reconstructs more model calls than native llm_calls", index)
	} else if sawDetached {
		return fmt.Errorf("event %d has a detached model error without a native model call", index)
	}
	s.reset()
	return nil
}

func (s *replayState) finishAlignment(errorCategory string) error {
	if s.responsesLoaded {
		if s.responseCursor < len(s.responses) {
			return fmt.Errorf("detached response %d has no matching event", s.responseCursor+1)
		}
	}
	if s.inModelCall {
		return errors.New("final model response sequence has no terminal response or error event")
	}
	missingCalls := s.totalLLMCalls - s.llmCalls
	if missingCalls == 1 && errorCategory == protocol.ErrorCategoryCaseTimeout && !s.inModelCall {
		// A case deadline may cancel the final call after BeforeModel (and warning
		// injection) but before any response/event exists. The native call total
		// is the only persisted boundary; event index zero is explicitly marked
		// unobserved rather than fabricating len(events)+1.
		s.consumePendingWarning(0, s.llmCalls+1)
		s.llmCalls++
		missingCalls = 0
	}
	if missingCalls != 0 {
		return fmt.Errorf(
			"reconstructed llm_calls=%d does not match native llm_calls=%d",
			s.llmCalls, s.totalLLMCalls,
		)
	}
	return nil
}

func isMaxLLMCallsStopEvent(response *model.Response) bool {
	return response != nil && response.Object == model.ObjectTypeError &&
		response.Error != nil && response.Error.Type == agent.ErrorTypeStopAgentError &&
		maxLLMCallsErrorPattern.MatchString(response.Error.Message)
}

func isSubmissionStopEvent(response *model.Response) bool {
	return response != nil && response.Object == model.ObjectTypeError &&
		response.Done && !response.IsPartial && response.Error != nil &&
		response.Error.Type == model.ErrorTypeFlowError &&
		response.Error.Message == protocol.SubmissionStopEventMessage
}

func isReplaySemanticResponse(response *model.Response) bool {
	if response == nil {
		return false
	}
	if response.Error != nil || response.Object == model.ObjectTypeToolResponse || isModelResponse(response) {
		return true
	}
	for _, choice := range response.Choices {
		if choice.Message.Role == model.RoleAssistant || choice.Message.Role == model.RoleTool ||
			choice.Delta.Role == model.RoleAssistant || choice.Delta.Role == model.RoleTool ||
			len(choice.Message.ToolCalls) != 0 || len(choice.Delta.ToolCalls) != 0 {
			return true
		}
	}
	return false
}

func (s *replayState) alignModelResponse(persisted *model.Response) (*model.Response, bool) {
	if !s.responsesLoaded {
		return persisted, true
	}
	if s.responseCursor < len(s.responses) {
		original := s.responses[s.responseCursor]
		if responsesEqual(persisted, original) {
			s.responseCursor++
			return persisted, true
		}
		if isFormatRecoveryBridge(persisted, original) {
			s.responseCursor++
			s.formatRecoveryResets++
			return original, true
		}
		return nil, false
	}
	return nil, false
}

func responsesEqual(first, second *model.Response) bool {
	if first == nil || second == nil {
		return first == second
	}
	firstJSON, err := json.Marshal(responseSemantics(first))
	if err != nil {
		return false
	}
	secondJSON, err := json.Marshal(responseSemantics(second))
	return err == nil && bytes.Equal(firstJSON, secondJSON)
}

type semanticResponse struct {
	Object            string               `json:"object"`
	Created           int64                `json:"created"`
	Model             string               `json:"model"`
	Choices           []model.Choice       `json:"choices"`
	Usage             *model.Usage         `json:"usage,omitempty"`
	SystemFingerprint *string              `json:"system_fingerprint,omitempty"`
	Error             *model.ResponseError `json:"error,omitempty"`
	Done              bool                 `json:"done"`
	IsPartial         bool                 `json:"is_partial"`
}

func responseSemantics(response *model.Response) semanticResponse {
	choices := response.Choices
	if len(choices) == 0 {
		choices = nil
	}
	return semanticResponse{
		Object: response.Object, Created: response.Created, Model: response.Model,
		Choices: choices, Usage: response.Usage,
		SystemFingerprint: response.SystemFingerprint, Error: response.Error,
		Done: response.Done, IsPartial: response.IsPartial,
	}
}

func isFormatRecoveryBridge(persisted, original *model.Response) bool {
	if persisted == nil || original == nil || persisted.Object != model.ObjectTypeChatCompletion ||
		persisted.IsPartial || persisted.Done || len(persisted.Choices) != 1 ||
		original.Object == model.ObjectTypeError || original.IsPartial || !original.Done ||
		original.Error != nil || len(original.Choices) == 0 {
		return false
	}
	message := persisted.Choices[0].Message
	if message.Role != model.RoleUser || len(message.ToolCalls) != 0 ||
		message.ToolID != "" || message.ToolName != "" {
		return false
	}
	_, err := protocol.ParseActions(original.Choices[0].Message.ToolCalls)
	var formatErr protocol.FormatError
	return errors.As(err, &formatErr) && message.Content == formatErr.Error()
}

func (s *replayState) observeToolResponse(index int, evt *event.Event) error {
	response := evt.Response
	if response.Error != nil {
		if s.responsesLoaded {
			return fmt.Errorf("event %d has an errored tool response", index)
		}
		s.reset()
		return nil
	}
	if response.IsPartial {
		return nil
	}
	for _, choice := range response.Choices {
		if choice.Message.Role == model.RoleTool {
			s.toolResults++
		}
	}
	if s.batch == nil || len(response.Choices) == 0 {
		if s.responsesLoaded && len(response.Choices) > 0 {
			return fmt.Errorf("event %d has an unassociated or errored tool response", index)
		}
		s.reset()
		return nil
	}
	argumentsByID, ok, err := event.GetExtension[map[string]string](
		evt, event.ToolCallArgsExtensionKey,
	)
	if err != nil || !ok || len(argumentsByID) != len(response.Choices) {
		if s.responsesLoaded {
			return fmt.Errorf("event %d has incomplete tool-call argument provenance", index)
		}
		s.reset()
		return nil
	}
	for _, choice := range response.Choices {
		if s.batch == nil || s.batch.next >= len(s.batch.expected) {
			if s.responsesLoaded {
				return fmt.Errorf("event %d has excess tool results", index)
			}
			s.reset()
			return nil
		}
		expected := s.batch.expected[s.batch.next]
		message := choice.Message
		arguments, found := argumentsByID[message.ToolID]
		if message.Role != model.RoleTool ||
			message.ToolID != expected.id ||
			message.ToolName != expected.name ||
			!found {
			if s.responsesLoaded {
				return fmt.Errorf("event %d tool result order or identity does not match its model call", index)
			}
			s.reset()
			return nil
		}
		if s.observationCodec != "" && !validObservationEnvelope(message.Content, s.observationCodec) {
			return fmt.Errorf(
				"event %d tool response %q is not a valid %s observation envelope",
				index, message.ToolID, s.observationCodec,
			)
		}
		s.batch.entries = append(s.batch.entries, toolloop.Entry{
			ToolName: message.ToolName, Arguments: []byte(arguments),
			Observation: message.Content,
		})
		s.batch.next++
	}
	if s.batch == nil || s.batch.next != len(s.batch.expected) {
		return nil
	}
	canonical, err := toolloop.CanonicalBatch(s.batch.entries)
	if err != nil {
		s.reset()
		return nil
	}
	hash := sha256.Sum256(canonical)
	warn, err := s.detector.Observe(s.batch.entries)
	toolCount := len(s.batch.entries)
	s.batch = nil
	if err != nil {
		s.reset()
		return nil
	}
	if warn {
		s.pendingWarning = &pendingWarning{
			repeatEventIndex: index,
			batchSHA256:      hex.EncodeToString(hash[:]),
			toolCount:        toolCount,
		}
	}
	return nil
}

func validObservationEnvelope(value string, codec observation.ObservationCodec) bool {
	switch codec {
	case observation.ObservationCodecXML:
		return validXMLObservation(value)
	case observation.ObservationCodecJSON:
		return validJSONObservation(value)
	case observation.ObservationCodecText:
		return validTextObservation(value)
	default:
		return false
	}
}

func validXMLObservation(value string) bool {
	returnCodeStart := strings.Index(value, "<returncode>")
	if returnCodeStart < 0 {
		return false
	}
	prefix := value[:returnCodeStart]
	if prefix != "" && !(strings.HasPrefix(prefix, "<exception>") &&
		strings.HasSuffix(prefix, "</exception>\n")) {
		return false
	}
	remainder := value[returnCodeStart+len("<returncode>"):]
	returnCodeEnd := strings.Index(remainder, "</returncode>\n")
	if returnCodeEnd < 0 {
		return false
	}
	if _, err := strconv.Atoi(remainder[:returnCodeEnd]); err != nil {
		return false
	}
	body := remainder[returnCodeEnd+len("</returncode>\n"):]
	if strings.HasPrefix(body, "<output>\n") && strings.HasSuffix(body, "</output>") {
		return true
	}
	return strings.HasPrefix(body, "<warning>\n") &&
		strings.Contains(body, "\n</warning><output_head>\n") &&
		strings.Contains(body, "\n</output_head>\n<elided_chars>\n") &&
		strings.Contains(body, " characters elided\n</elided_chars>\n<output_tail>\n") &&
		strings.HasSuffix(body, "\n</output_tail>")
}

func validJSONObservation(value string) bool {
	var document map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &document); err != nil {
		return false
	}
	allowed := map[string]bool{
		"exception": true, "returncode": true, "output": true, "warning": true,
		"output_head": true, "elided_chars": true, "output_tail": true,
	}
	for key := range document {
		if !allowed[key] {
			return false
		}
	}
	var returnCode int
	if raw, ok := document["returncode"]; !ok || json.Unmarshal(raw, &returnCode) != nil {
		return false
	}
	if raw, ok := document["exception"]; ok {
		var exception string
		if json.Unmarshal(raw, &exception) != nil {
			return false
		}
	}
	if raw, ok := document["output"]; ok {
		var output string
		wantFields := 2
		if _, hasException := document["exception"]; hasException {
			wantFields++
		}
		return len(document) == wantFields && json.Unmarshal(raw, &output) == nil
	}
	for _, key := range []string{"warning", "output_head", "elided_chars", "output_tail"} {
		if _, ok := document[key]; !ok {
			return false
		}
	}
	var warning, head, tail string
	var elided int
	wantFields := 5
	if _, hasException := document["exception"]; hasException {
		wantFields++
	}
	return len(document) == wantFields &&
		json.Unmarshal(document["warning"], &warning) == nil &&
		json.Unmarshal(document["output_head"], &head) == nil &&
		json.Unmarshal(document["elided_chars"], &elided) == nil &&
		elided >= 0 && json.Unmarshal(document["output_tail"], &tail) == nil
}

func validTextObservation(value string) bool {
	returnCodeStart := strings.Index(value, "returncode: ")
	if returnCodeStart < 0 {
		return false
	}
	prefix := value[:returnCodeStart]
	if prefix != "" && !(strings.HasPrefix(prefix, "exception: ") && strings.HasSuffix(prefix, "\n")) {
		return false
	}
	remainder := value[returnCodeStart+len("returncode: "):]
	lineEnd := strings.IndexByte(remainder, '\n')
	if lineEnd < 0 {
		return false
	}
	if _, err := strconv.Atoi(remainder[:lineEnd]); err != nil {
		return false
	}
	body := remainder[lineEnd+1:]
	return strings.HasPrefix(body, "output:\n") ||
		(strings.HasPrefix(body, "warning:\n") && strings.Contains(body, "\noutput_head:\n") &&
			strings.Contains(body, "\nelided_chars: ") && strings.Contains(body, "\noutput_tail:\n"))
}

func (s *replayState) reset() {
	s.detector.Reset()
	s.batch = nil
	s.pendingWarning = nil
}

func isModelResponse(response *model.Response) bool {
	return response != nil && (response.Object == model.ObjectTypeChatCompletion ||
		response.Object == model.ObjectTypeChatCompletionChunk ||
		response.Object == model.ObjectTypeError)
}

func clampNonnegative(value int) int {
	if value < 0 {
		return 0
	}
	return value
}

func loadCases(runDir string) ([]loadedCase, error) {
	rootInfo, err := os.Lstat(runDir)
	if err != nil {
		return nil, fmt.Errorf("inspect run directory: %w", err)
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("run directory %s must be a real directory, not a symlink", runDir)
	}
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, fmt.Errorf("read run directory: %w", err)
	}
	// os.ReadDir is sorted, but keep the ordering explicit for alternate
	// implementations and future refactors.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	loaded := make([]loadedCase, 0)
	seen := make(map[string]struct{})
	var expectedIdentity *RunIdentity
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("run directory contains symlink %q", entry.Name())
		}
		if !entry.IsDir() {
			if strings.HasSuffix(entry.Name(), ".native.json") ||
				strings.HasSuffix(entry.Name(), ".responses.json") ||
				strings.HasSuffix(entry.Name(), ".tag.json") {
				return nil, fmt.Errorf(
					"run directory contains case artifact %q outside an instance directory",
					entry.Name(),
				)
			}
			continue
		}
		instanceID := entry.Name()
		if err := validateInstanceID(instanceID); err != nil {
			return nil, err
		}
		caseDir, err := boundedJoin(runDir, instanceID)
		if err != nil {
			return nil, err
		}
		caseInfo, err := os.Lstat(caseDir)
		if err != nil {
			return nil, fmt.Errorf("inspect instance directory %s: %w", instanceID, err)
		}
		if caseInfo.Mode()&os.ModeSymlink != 0 || !caseInfo.IsDir() {
			return nil, fmt.Errorf("instance path %s must be a real directory", instanceID)
		}
		if err := validateNativeDirectoryArtifacts(caseDir, instanceID); err != nil {
			return nil, err
		}
		nativePath, err := boundedJoin(caseDir, instanceID+".native.json")
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(nativePath); errors.Is(err, os.ErrNotExist) {
			responsesPath, joinErr := boundedJoin(caseDir, instanceID+".responses.json")
			if joinErr != nil {
				return nil, joinErr
			}
			if _, responsesErr := os.Lstat(responsesPath); responsesErr == nil {
				return nil, fmt.Errorf(
					"directory %q contains an orphan responses artifact without its native artifact",
					instanceID,
				)
			} else if !errors.Is(responsesErr, os.ErrNotExist) {
				return nil, fmt.Errorf("inspect responses artifact %s: %w", instanceID, responsesErr)
			}
			containsNative, inspectErr := containsNativeArtifact(caseDir)
			if inspectErr != nil {
				return nil, inspectErr
			}
			if containsNative {
				return nil, fmt.Errorf(
					"directory %q contains a native artifact outside its exact instance path",
					instanceID,
				)
			}
			continue
		} else if err != nil {
			return nil, fmt.Errorf("inspect native artifact %s: %w", instanceID, err)
		}
		one, err := loadCase(caseDir, instanceID)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[one.result.InstanceID]; duplicate {
			return nil, fmt.Errorf("duplicate native instance id %q", one.result.InstanceID)
		}
		identity := runIdentityFrom(one.result.Info)
		if err := validateRunIdentity(identity); err != nil {
			return nil, fmt.Errorf("native instance %s: %w", instanceID, err)
		}
		if expectedIdentity == nil {
			expectedIdentity = &identity
		} else if !runIdentitiesEqual(identity, *expectedIdentity) {
			return nil, fmt.Errorf(
				"native instance %s has a different run identity from %s",
				instanceID, loaded[0].result.InstanceID,
			)
		}
		seen[one.result.InstanceID] = struct{}{}
		loaded = append(loaded, one)
	}
	sort.Slice(loaded, func(i, j int) bool {
		return loaded[i].result.InstanceID < loaded[j].result.InstanceID
	})
	if expectedIdentity != nil {
		ids := make([]string, len(loaded))
		for i := range loaded {
			ids[i] = loaded[i].result.InstanceID
		}
		selectedHash := stringSHA256(strings.Join(ids, "\n") + "\n")
		if selectedHash != expectedIdentity.SelectedInstancesSHA256 {
			return nil, fmt.Errorf(
				"native case-set SHA-256 %q does not match run identity %q",
				selectedHash, expectedIdentity.SelectedInstancesSHA256,
			)
		}
	}
	return loaded, nil
}

func validateNativeDirectoryArtifacts(caseDir, instanceID string) error {
	entries, err := os.ReadDir(caseDir)
	if err != nil {
		return fmt.Errorf("inspect native instance directory %s: %w", instanceID, err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("native instance %s contains symlink %q", instanceID, name)
		}
		switch {
		case strings.HasSuffix(name, ".tag.json"):
			return fmt.Errorf(
				"native instance %s contains legacy tag artifact %q without an exact root %s",
				instanceID, name, legacyTagManifestFilename,
			)
		case strings.HasSuffix(name, ".native.json") && name != instanceID+".native.json":
			return fmt.Errorf(
				"native instance %s contains a native artifact outside its exact instance path",
				instanceID,
			)
		case strings.HasSuffix(name, ".responses.json") && name != instanceID+".responses.json":
			return fmt.Errorf(
				"native instance %s contains a responses artifact outside its exact instance path",
				instanceID,
			)
		}
	}
	return nil
}

func loadTagRun(runDir, manifestPath string) (loadedRun, error) {
	manifestData, err := readRegularFile(manifestPath)
	if err != nil {
		return loadedRun{}, fmt.Errorf("read tag runner manifest: %w", err)
	}
	var manifest tagManifest
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		return loadedRun{}, fmt.Errorf("parse tag runner manifest: %w", err)
	}
	if err := validateTagManifest(manifestData, manifest); err != nil {
		return loadedRun{}, err
	}
	identity := runIdentityFromTagManifest(manifest)
	if err := validateRunIdentity(identity); err != nil {
		return loadedRun{}, fmt.Errorf("tag runner manifest: %w", err)
	}
	cases, err := loadTagCases(runDir, manifest)
	if err != nil {
		return loadedRun{}, err
	}
	manifestHash := sha256.Sum256(manifestData)
	return loadedRun{
		cases: cases, identity: identity,
		kind: "legacy-tag-manifest-warning-off-run-directory",
		artifactPattern: "tag-runner-manifest.json + " +
			"<instance_id>/<instance_id>.tag.json + " +
			"<instance_id>/<instance_id>.responses.json",
		manifestPath:   legacyTagManifestFilename,
		manifestSHA256: hex.EncodeToString(manifestHash[:]),
	}, nil
}

func validateTagManifest(data []byte, manifest tagManifest) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("inspect tag runner manifest fields: %w", err)
	}
	for _, field := range []string{
		"run_id", "runner_type", "framework_version", "framework_revision", "agent_protocol", "upstream_commit",
		"observation_codec", "source_modified", "binary_sha256", "cases_sha256",
		"selected_case_set_sha256", "model_config_sha256", "environment_config_sha256",
		"duration_ms", "cases", "predictions", "progress", "usage",
		"case_count", "attempted_count", "skipped_existing", "prediction_count",
		"workers", "redo_existing", "command_timeout", "case_timeout", "code_search",
		"tool_loop_warning", "tool_loop_warning_count", "tool_loop_warning_case_count",
		"workspace_preload", "workspace_representation",
		"workspace_representation_schema", "workspace_representation_sha256",
		"exit_status_counts", "llm_calls", "tool_calls", "status",
	} {
		value, ok := raw[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("tag runner manifest is missing or has null required field %q", field)
		}
	}
	if manifest.RunnerType != "tag" {
		return fmt.Errorf("tag runner manifest runner_type=%q, want tag", manifest.RunnerType)
	}
	if strings.TrimSpace(manifest.FrameworkVersion) == "" ||
		manifest.AgentProtocol != "mini-swe-agent-v2.1-on-tag" {
		return fmt.Errorf(
			"tag runner manifest has unsupported framework/protocol version=%q protocol=%q",
			manifest.FrameworkVersion, manifest.AgentProtocol,
		)
	}
	if manifest.DurationMS < 0 || strings.TrimSpace(manifest.Cases) == "" ||
		strings.TrimSpace(manifest.Predictions) == "" || strings.TrimSpace(manifest.Progress) == "" {
		return errors.New("tag runner manifest has invalid duration or artifact paths")
	}
	if manifest.SourceModified {
		return errors.New("tag runner manifest has source_modified=true")
	}
	if manifest.ObservationCodec != string(observation.ObservationCodecXML) {
		return fmt.Errorf(
			"tag runner manifest observation_codec=%q, want frozen V12 xml",
			manifest.ObservationCodec,
		)
	}
	if manifest.CommandTimeout != "1m0s" || manifest.CaseTimeout != "4h0m0s" {
		return fmt.Errorf(
			"tag runner manifest timeouts command=%q case=%q, want frozen V12 1m0s/4h0m0s",
			manifest.CommandTimeout, manifest.CaseTimeout,
		)
	}
	if manifest.CaseCount != legacyTagCaseCount ||
		manifest.AttemptedCount != legacyTagCaseCount ||
		manifest.PredictionCount != legacyTagCaseCount || manifest.SkippedExisting != 0 {
		return fmt.Errorf(
			"tag runner manifest coverage case=%d attempted=%d skipped=%d prediction=%d, want fresh full-%d",
			manifest.CaseCount, manifest.AttemptedCount, manifest.SkippedExisting,
			manifest.PredictionCount, legacyTagCaseCount,
		)
	}
	if manifest.Workers != 15 {
		return fmt.Errorf("tag runner manifest workers=%d, want frozen V12 workers=15", manifest.Workers)
	}
	if manifest.RedoExisting {
		return errors.New("tag runner manifest has redo_existing=true")
	}
	if manifest.CodeSearch || manifest.WorkspacePreload ||
		manifest.WorkspaceRepresentation != "current-fixed" {
		return fmt.Errorf(
			"tag runner manifest is not the frozen native arm: code_search=%t workspace_preload=%t representation=%q",
			manifest.CodeSearch, manifest.WorkspacePreload, manifest.WorkspaceRepresentation,
		)
	}
	if strings.TrimSpace(manifest.WorkspaceRepresentationSchema) == "" ||
		!isHexIdentifier(manifest.WorkspaceRepresentationSHA256, 64) {
		return errors.New("tag runner manifest has invalid workspace representation identity")
	}
	if manifest.EmbeddingConfigSHA256 != "" || manifest.Filter != "" ||
		manifest.CaseList != "" || manifest.CaseListSHA256 != "" {
		return errors.New("tag runner manifest contains non-frozen selection or embedding configuration")
	}
	if manifest.ToolLoopWarning {
		return errors.New("tag runner manifest has tool_loop_warning=true; shadow replay requires warning-off input")
	}
	if manifest.ToolLoopWarningCount != 0 || manifest.ToolLoopWarningCaseCount != 0 {
		return errors.New("tag runner manifest contains tool-loop warning telemetry")
	}
	if manifest.Status != "completed" && manifest.Status != "completed_with_errors" {
		return fmt.Errorf("tag runner manifest status=%q is not terminal", manifest.Status)
	}
	if manifest.LLMCalls < 0 || manifest.ToolCalls < 0 {
		return errors.New("tag runner manifest contains negative aggregate call counts")
	}
	return nil
}

func runIdentityFromTagManifest(manifest tagManifest) RunIdentity {
	return RunIdentity{
		ArtifactFormat: legacyTagManifestFormat,
		RunID:          manifest.RunID, ObservationCodec: manifest.ObservationCodec,
		FrameworkRevision: manifest.FrameworkRevision, UpstreamCommit: manifest.UpstreamCommit,
		SourceRevision: manifest.SourceRevision, SourceModified: manifest.SourceModified,
		BinarySHA256: manifest.BinarySHA256, ModelConfigSHA256: manifest.ModelConfigSHA256,
		EnvironmentConfigSHA256: manifest.EnvironmentConfigSHA256,
		CasesSHA256:             manifest.CasesSHA256, CommandTimeout: manifest.CommandTimeout,
		CaseTimeout:             manifest.CaseTimeout,
		SelectedInstancesSHA256: manifest.SelectedCaseSetSHA256,
		CleanRoom:               nil, ToolLoopWarning: manifest.ToolLoopWarning,
		Workers: manifest.Workers,
	}
}

func loadTagCases(runDir string, manifest tagManifest) ([]loadedCase, error) {
	entries, err := os.ReadDir(runDir)
	if err != nil {
		return nil, fmt.Errorf("read tag run directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	loaded := make([]loadedCase, 0, manifest.CaseCount)
	seen := make(map[string]struct{}, manifest.CaseCount)
	exitCounts := make(map[string]int)
	var llmCalls, toolCalls int
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("tag run directory contains symlink %q", entry.Name())
		}
		if !entry.IsDir() {
			if strings.HasSuffix(entry.Name(), ".tag.json") ||
				strings.HasSuffix(entry.Name(), ".native.json") ||
				strings.HasSuffix(entry.Name(), ".responses.json") {
				return nil, fmt.Errorf("tag run directory contains case artifact %q outside an instance directory", entry.Name())
			}
			continue
		}
		instanceID := entry.Name()
		if err := validateInstanceID(instanceID); err != nil {
			return nil, err
		}
		caseDir, err := boundedJoin(runDir, instanceID)
		if err != nil {
			return nil, err
		}
		caseInfo, err := os.Lstat(caseDir)
		if err != nil {
			return nil, fmt.Errorf("inspect tag instance directory %s: %w", instanceID, err)
		}
		if caseInfo.Mode()&os.ModeSymlink != 0 || !caseInfo.IsDir() {
			return nil, fmt.Errorf("tag instance path %s must be a real directory", instanceID)
		}
		tagPath, err := boundedJoin(caseDir, instanceID+".tag.json")
		if err != nil {
			return nil, err
		}
		tagExists, err := pathExists(tagPath)
		if err != nil {
			return nil, fmt.Errorf("inspect tag artifact %s: %w", instanceID, err)
		}
		if err := validateTagDirectoryArtifacts(caseDir, instanceID, tagExists); err != nil {
			return nil, err
		}
		if !tagExists {
			continue
		}
		one, err := loadTagCase(caseDir, instanceID, manifest.ObservationCodec)
		if err != nil {
			return nil, err
		}
		if _, duplicate := seen[one.result.InstanceID]; duplicate {
			return nil, fmt.Errorf("duplicate tag instance id %q", one.result.InstanceID)
		}
		seen[one.result.InstanceID] = struct{}{}
		loaded = append(loaded, one)
		exitCounts[one.result.Info.ExitStatus]++
		llmCalls += one.result.LLMCalls
		toolCalls += one.result.ToolCalls
	}
	sort.Slice(loaded, func(i, j int) bool {
		return loaded[i].result.InstanceID < loaded[j].result.InstanceID
	})
	if len(loaded) != manifest.CaseCount || len(loaded) != legacyTagCaseCount {
		return nil, fmt.Errorf(
			"tag run has %d exact case artifacts, want manifest/frozen V12 count %d",
			len(loaded), legacyTagCaseCount,
		)
	}
	ids := make([]string, len(loaded))
	for i := range loaded {
		ids[i] = loaded[i].result.InstanceID
	}
	selectedHash := stringSHA256(strings.Join(ids, "\n") + "\n")
	if selectedHash != manifest.SelectedCaseSetSHA256 {
		return nil, fmt.Errorf(
			"tag case-set SHA-256 %q does not match manifest %q",
			selectedHash, manifest.SelectedCaseSetSHA256,
		)
	}
	if !equalStringIntMaps(exitCounts, manifest.ExitStatusCounts) {
		return nil, fmt.Errorf(
			"tag exit-status counts %v do not match manifest %v",
			exitCounts, manifest.ExitStatusCounts,
		)
	}
	if llmCalls != manifest.LLMCalls || toolCalls != manifest.ToolCalls {
		return nil, fmt.Errorf(
			"tag aggregate calls llm=%d tool=%d do not match manifest llm=%d tool=%d",
			llmCalls, toolCalls, manifest.LLMCalls, manifest.ToolCalls,
		)
	}
	return loaded, nil
}

func validateTagDirectoryArtifacts(caseDir, instanceID string, exactTagExists bool) error {
	entries, err := os.ReadDir(caseDir)
	if err != nil {
		return fmt.Errorf("inspect tag instance directory %s: %w", instanceID, err)
	}
	exactResponses := instanceID + ".responses.json"
	responsesFound := false
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("tag instance directory %s contains symlink %q", instanceID, entry.Name())
		}
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		switch {
		case strings.HasSuffix(name, ".native.json"):
			return fmt.Errorf("tag instance %s mixes a native artifact %q", instanceID, name)
		case strings.HasSuffix(name, ".tag.json") && name != instanceID+".tag.json":
			return fmt.Errorf("tag instance %s contains a tag artifact outside its exact instance path", instanceID)
		case strings.HasSuffix(name, ".responses.json") && name != exactResponses:
			return fmt.Errorf("tag instance %s contains a responses artifact outside its exact instance path", instanceID)
		case name == exactResponses:
			responsesFound = true
		}
	}
	if exactTagExists && !responsesFound {
		return fmt.Errorf("tag instance %s has no detached responses artifact", instanceID)
	}
	if !exactTagExists && responsesFound {
		return fmt.Errorf("directory %q contains an orphan responses artifact without its tag artifact", instanceID)
	}
	return nil
}

func loadTagCase(caseDir, instanceID, observationCodec string) (loadedCase, error) {
	tagPath, err := boundedJoin(caseDir, instanceID+".tag.json")
	if err != nil {
		return loadedCase{}, err
	}
	responsesPath, err := boundedJoin(caseDir, instanceID+".responses.json")
	if err != nil {
		return loadedCase{}, err
	}
	tagData, err := readRegularFile(tagPath)
	if err != nil {
		return loadedCase{}, fmt.Errorf("read tag artifact %s: %w", instanceID, err)
	}
	if containsToolLoopMarker(tagData) {
		return loadedCase{}, fmt.Errorf("tag instance %s contains %s marker", instanceID, toolLoopDetectedMarker)
	}
	var result nativeResult
	if err := json.Unmarshal(tagData, &result); err != nil {
		return loadedCase{}, fmt.Errorf("parse tag artifact %s: %w", instanceID, err)
	}
	if result.InstanceID != instanceID {
		return loadedCase{}, fmt.Errorf(
			"tag instance id %q does not match directory %q", result.InstanceID, instanceID,
		)
	}
	if err := validateTagWarningOffArtifact(tagData, result); err != nil {
		return loadedCase{}, err
	}
	responsesData, err := readRegularFile(responsesPath)
	if err != nil {
		return loadedCase{}, fmt.Errorf("read tag responses artifact %s: %w", instanceID, err)
	}
	if containsToolLoopMarker(responsesData) {
		return loadedCase{}, fmt.Errorf(
			"tag responses artifact %s contains %s marker", instanceID, toolLoopDetectedMarker,
		)
	}
	if trimmed := bytes.TrimSpace(responsesData); len(trimmed) == 0 || trimmed[0] != '[' {
		return loadedCase{}, fmt.Errorf("tag responses artifact %s must be a JSON array", instanceID)
	}
	var responses []*model.Response
	if err := json.Unmarshal(responsesData, &responses); err != nil {
		return loadedCase{}, fmt.Errorf("parse tag responses artifact %s: %w", instanceID, err)
	}
	for index, response := range responses {
		if response == nil {
			return loadedCase{}, fmt.Errorf("tag responses artifact %s has null response %d", instanceID, index+1)
		}
	}
	if err := validateTagTerminal(result, len(responses)); err != nil {
		return loadedCase{}, fmt.Errorf("tag instance %s: %w", instanceID, err)
	}
	responsesHash := sha256.Sum256(responsesData)
	responsesSHA256 := hex.EncodeToString(responsesHash[:])
	result.Info.ObservationCodec = observationCodec
	result.ResponseCount = len(responses)
	result.ResponsesSHA256 = responsesSHA256
	result.Responses = responses
	result.responsesLoaded = true
	tagHash := sha256.Sum256(tagData)
	return loadedCase{
		result: result,
		fingerprint: ArtifactFingerprint{
			InstanceID: instanceID, TrajectoryKind: "legacy-tag-case",
			TrajectoryPath:   filepath.ToSlash(filepath.Join(instanceID, instanceID+".tag.json")),
			TrajectorySHA256: hex.EncodeToString(tagHash[:]),
			ResponsesPath:    filepath.ToSlash(filepath.Join(instanceID, instanceID+".responses.json")),
			ResponsesSHA256:  responsesSHA256,
		},
	}, nil
}

func validateTagWarningOffArtifact(data []byte, result nativeResult) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("inspect tag warning-off fields for %s: %w", result.InstanceID, err)
	}
	for _, field := range []string{
		"instance_id", "info", "duration_ms", "llm_calls", "tool_calls", "usage", "events",
		"tool_loop_warning_count", "first_tool_loop_warning_llm_call",
	} {
		value, ok := raw[field]
		if !ok || (field != "first_tool_loop_warning_llm_call" &&
			bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return fmt.Errorf("tag instance %s is missing or has null required field %q", result.InstanceID, field)
		}
	}
	var rawInfo map[string]json.RawMessage
	if err := json.Unmarshal(raw["info"], &rawInfo); err != nil {
		return fmt.Errorf("inspect tag info for %s: %w", result.InstanceID, err)
	}
	if value, ok := rawInfo["exit_status"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fmt.Errorf("tag instance %s is missing or has null required field %q", result.InstanceID, "info.exit_status")
	}
	if result.LLMCalls < 0 || result.ToolCalls < 0 {
		return fmt.Errorf("tag instance %s contains negative call counts", result.InstanceID)
	}
	if result.ToolLoopWarningCount != 0 || result.FirstToolLoopWarningLLMCall != nil ||
		len(result.ToolLoopWarningLLMCalls) != 0 {
		return fmt.Errorf("tag instance %s contains tool-loop warning telemetry", result.InstanceID)
	}
	return nil
}

func validateTagTerminal(result nativeResult, responseCount int) error {
	switch result.Info.ExitStatus {
	case "Submitted":
		if responseCount != result.LLMCalls || result.Info.Error != "" ||
			result.Info.ErrorCategory != "" || result.Info.Retryable {
			return fmt.Errorf(
				"non-canonical Submitted terminal error=%q category=%q retryable=%t responses=%d llm_calls=%d",
				result.Info.Error, result.Info.ErrorCategory, result.Info.Retryable,
				responseCount, result.LLMCalls,
			)
		}
	case "Error":
		if result.Info.ErrorCategory != protocol.ErrorCategoryAgent ||
			result.Info.Error != "max LLM calls (250) exceeded" || result.Info.Retryable ||
			result.LLMCalls != 250 || responseCount != result.LLMCalls || result.ModelPatch != "" {
			return fmt.Errorf(
				"non-canonical Error terminal category=%q error=%q retryable=%t llm_calls=%d responses=%d patch_bytes=%d",
				result.Info.ErrorCategory, result.Info.Error, result.Info.Retryable, result.LLMCalls,
				responseCount, len(result.ModelPatch),
			)
		}
	case "Timeout":
		if result.Info.ErrorCategory != protocol.ErrorCategoryCaseTimeout ||
			result.Info.Error != "context deadline exceeded" || result.Info.Retryable || result.ModelPatch != "" ||
			(responseCount != result.LLMCalls && responseCount != result.LLMCalls-1) {
			return fmt.Errorf(
				"non-canonical Timeout terminal category=%q error=%q retryable=%t llm_calls=%d responses=%d patch_bytes=%d",
				result.Info.ErrorCategory, result.Info.Error, result.Info.Retryable, result.LLMCalls,
				responseCount, len(result.ModelPatch),
			)
		}
	default:
		return fmt.Errorf("unsupported frozen V12 exit_status %q", result.Info.ExitStatus)
	}
	return nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return false, err
}

func stringSHA256(value string) string {
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func equalStringIntMaps(first, second map[string]int) bool {
	if len(first) != len(second) {
		return false
	}
	for key, value := range first {
		if second[key] != value {
			return false
		}
	}
	return true
}

func containsToolLoopMarker(data []byte) bool {
	if bytes.Contains(data, []byte(toolLoopDetectedMarker)) {
		return true
	}
	var document any
	if json.Unmarshal(data, &document) != nil {
		return false
	}
	return jsonValueContainsMarker(document)
}

func jsonValueContainsMarker(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.Contains(typed, toolLoopDetectedMarker)
	case []any:
		for _, element := range typed {
			if jsonValueContainsMarker(element) {
				return true
			}
		}
	case map[string]any:
		for key, element := range typed {
			if strings.Contains(key, toolLoopDetectedMarker) || jsonValueContainsMarker(element) {
				return true
			}
		}
	}
	return false
}

func runIdentityFrom(info nativeInfo) RunIdentity {
	cleanRoom := info.CleanRoom
	return RunIdentity{
		ArtifactFormat: nativeCaseIdentityFormat,
		RunID:          info.RunID, ObservationCodec: info.ObservationCodec,
		SourceRevision: info.SourceRevision, SourceModified: info.SourceModified,
		BinarySHA256: info.BinarySHA256, ModelConfigSHA256: info.ModelConfigSHA256,
		EnvironmentConfigSHA256: info.EnvironmentConfigSHA256,
		CasesSHA256:             info.CasesSHA256, CommandTimeout: info.CommandTimeout,
		CaseTimeout: info.CaseTimeout, SelectedInstancesSHA256: info.SelectedInstancesSHA256,
		CleanRoom:             &cleanRoom,
		ToolLoopWarning:       info.ToolLoopWarning,
		CleanRoomPolicySHA256: info.CleanRoomPolicySHA256,
		OfflineAssetsSHA256:   info.OfflineAssetsSHA256, ImageSetSHA256: info.ImageSetSHA256,
		Workers: info.Workers,
	}
}

func validateRunIdentity(identity RunIdentity) error {
	if identity.ArtifactFormat != nativeCaseIdentityFormat &&
		identity.ArtifactFormat != legacyTagManifestFormat {
		return fmt.Errorf("run identity has unsupported artifact format %q", identity.ArtifactFormat)
	}
	if err := validateInstanceID(identity.RunID); err != nil {
		return fmt.Errorf("invalid run id: %w", err)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"observation codec", identity.ObservationCodec},
		{"command timeout", identity.CommandTimeout},
		{"case timeout", identity.CaseTimeout},
	} {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("run identity has empty %s", field.name)
		}
	}
	codec, err := observation.ParseObservationCodec(identity.ObservationCodec)
	if err != nil || string(codec) != identity.ObservationCodec {
		return fmt.Errorf("run identity observation codec %q is not canonical", identity.ObservationCodec)
	}
	if identity.ArtifactFormat == nativeCaseIdentityFormat {
		if identity.CleanRoom == nil {
			return errors.New("native run identity does not explicitly record clean_room")
		}
		if !isHexIdentifier(identity.SourceRevision, 40, 64) {
			return fmt.Errorf("run identity source revision %q is not a full Git revision", identity.SourceRevision)
		}
		if identity.FrameworkRevision != "" || identity.UpstreamCommit != "" {
			return errors.New("native per-case run identity contains legacy tag revisions")
		}
	} else {
		if identity.CleanRoom != nil {
			return errors.New("legacy tag run identity fabricates unavailable clean_room provenance")
		}
		if identity.SourceRevision != "" && !isHexIdentifier(identity.SourceRevision, 40, 64) {
			return fmt.Errorf("run identity source revision %q is not a full Git revision", identity.SourceRevision)
		}
		for _, revision := range []struct {
			name  string
			value string
		}{
			{"framework revision", identity.FrameworkRevision},
			{"upstream commit", identity.UpstreamCommit},
		} {
			if !isHexIdentifier(revision.value, 40, 64) {
				return fmt.Errorf("run identity %s %q is not a full Git revision", revision.name, revision.value)
			}
		}
	}
	for _, hash := range []struct {
		name  string
		value string
	}{
		{"binary hash", identity.BinarySHA256},
		{"model config hash", identity.ModelConfigSHA256},
		{"environment config hash", identity.EnvironmentConfigSHA256},
		{"cases hash", identity.CasesSHA256},
		{"selected instances hash", identity.SelectedInstancesSHA256},
	} {
		if !isHexIdentifier(hash.value, 64) {
			return fmt.Errorf("run identity %s %q is not a SHA-256 digest", hash.name, hash.value)
		}
	}
	if identity.Workers <= 0 {
		return fmt.Errorf("run identity has non-positive workers %d", identity.Workers)
	}
	if identity.CleanRoom != nil && *identity.CleanRoom {
		for _, hash := range []struct {
			name  string
			value string
		}{
			{"clean-room policy hash", identity.CleanRoomPolicySHA256},
			{"image-set hash", identity.ImageSetSHA256},
		} {
			if !isHexIdentifier(hash.value, 64) {
				return fmt.Errorf("run identity %s %q is not a SHA-256 digest", hash.name, hash.value)
			}
		}
		if identity.OfflineAssetsSHA256 != "" && !isHexIdentifier(identity.OfflineAssetsSHA256, 64) {
			return fmt.Errorf(
				"run identity offline assets hash %q is not a SHA-256 digest",
				identity.OfflineAssetsSHA256,
			)
		}
	} else if identity.CleanRoomPolicySHA256 != "" || identity.OfflineAssetsSHA256 != "" ||
		identity.ImageSetSHA256 != "" {
		return errors.New("non-clean-room run identity contains clean-room provenance")
	}
	return nil
}

func runIdentitiesEqual(first, second RunIdentity) bool {
	if (first.CleanRoom == nil) != (second.CleanRoom == nil) {
		return false
	}
	if first.CleanRoom != nil && *first.CleanRoom != *second.CleanRoom {
		return false
	}
	first.CleanRoom = nil
	second.CleanRoom = nil
	return first == second
}

func isHexIdentifier(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		if len(value) == length {
			validLength = true
			break
		}
	}
	if !validLength {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') &&
			(character < 'A' || character > 'F') {
			return false
		}
	}
	return true
}

func containsNativeArtifact(directory string) (bool, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, fmt.Errorf("inspect non-case directory %s: %w", directory, err)
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return false, fmt.Errorf("directory %s contains symlink %q", directory, entry.Name())
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".native.json") {
			return true, nil
		}
	}
	return false, nil
}

func loadCase(caseDir, instanceID string) (loadedCase, error) {
	nativePath, err := boundedJoin(caseDir, instanceID+".native.json")
	if err != nil {
		return loadedCase{}, err
	}
	responsesPath, err := boundedJoin(caseDir, instanceID+".responses.json")
	if err != nil {
		return loadedCase{}, err
	}
	nativeData, err := readRegularFile(nativePath)
	if err != nil {
		return loadedCase{}, fmt.Errorf("read native artifact %s: %w", instanceID, err)
	}
	if containsToolLoopMarker(nativeData) {
		return loadedCase{}, fmt.Errorf("native instance %s contains %s marker", instanceID, toolLoopDetectedMarker)
	}
	var result nativeResult
	if err := json.Unmarshal(nativeData, &result); err != nil {
		return loadedCase{}, fmt.Errorf("parse native artifact %s: %w", instanceID, err)
	}
	if result.InstanceID != instanceID {
		return loadedCase{}, fmt.Errorf(
			"native instance id %q does not match directory %q", result.InstanceID, instanceID,
		)
	}
	if err := validateWarningOffArtifact(nativeData, result); err != nil {
		return loadedCase{}, err
	}
	responsesData, err := readRegularFile(responsesPath)
	if err != nil {
		return loadedCase{}, fmt.Errorf("read responses artifact %s: %w", instanceID, err)
	}
	if containsToolLoopMarker(responsesData) {
		return loadedCase{}, fmt.Errorf(
			"responses artifact %s contains %s marker", instanceID, toolLoopDetectedMarker,
		)
	}
	var responses []*model.Response
	if err := json.Unmarshal(responsesData, &responses); err != nil {
		return loadedCase{}, fmt.Errorf("parse responses artifact %s: %w", instanceID, err)
	}
	if len(responses) != result.ResponseCount {
		return loadedCase{}, fmt.Errorf(
			"responses artifact %s count %d does not match native count %d",
			instanceID, len(responses), result.ResponseCount,
		)
	}
	responsesHash := sha256.Sum256(responsesData)
	responsesSHA256 := hex.EncodeToString(responsesHash[:])
	if responsesSHA256 != result.ResponsesSHA256 {
		return loadedCase{}, fmt.Errorf(
			"responses artifact %s SHA-256 %q does not match native SHA-256 %q",
			instanceID, responsesSHA256, result.ResponsesSHA256,
		)
	}
	result.Responses = responses
	result.responsesLoaded = true
	result.modernNative = true
	nativeHash := sha256.Sum256(nativeData)
	return loadedCase{
		result: result,
		fingerprint: ArtifactFingerprint{
			InstanceID:       instanceID,
			TrajectoryKind:   "native-case",
			TrajectoryPath:   filepath.ToSlash(filepath.Join(instanceID, instanceID+".native.json")),
			TrajectorySHA256: hex.EncodeToString(nativeHash[:]),
			ResponsesPath:    filepath.ToSlash(filepath.Join(instanceID, instanceID+".responses.json")),
			ResponsesSHA256:  responsesSHA256,
		},
	}, nil
}

func validateWarningOffArtifact(nativeData []byte, result nativeResult) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(nativeData, &raw); err != nil {
		return fmt.Errorf("inspect native warning-off fields for %s: %w", result.InstanceID, err)
	}
	for _, field := range []string{
		"instance_id",
		"info",
		"model_patch",
		"llm_calls",
		"tool_calls",
		"tool_loop_warning_count",
		"first_tool_loop_warning_llm_call",
		"tool_loop_warning_llm_calls",
		"response_count",
		"responses_sha256",
	} {
		value, ok := raw[field]
		if !ok {
			return fmt.Errorf("native instance %s is missing required field %q", result.InstanceID, field)
		}
		if field != "first_tool_loop_warning_llm_call" &&
			field != "tool_loop_warning_llm_calls" &&
			bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("native instance %s has null required field %q", result.InstanceID, field)
		}
	}
	var rawInfo map[string]json.RawMessage
	if infoRaw, ok := raw["info"]; !ok {
		return fmt.Errorf("native instance %s is missing required field %q", result.InstanceID, "info")
	} else if err := json.Unmarshal(infoRaw, &rawInfo); err != nil {
		return fmt.Errorf("inspect native info for %s: %w", result.InstanceID, err)
	}
	for _, field := range []string{"clean_room", "tool_loop_warning", "workers", "exit_status"} {
		value, ok := rawInfo[field]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf(
				"native instance %s is missing or has null required field %q",
				result.InstanceID, "info."+field,
			)
		}
	}
	if result.Info.ToolLoopWarning {
		return fmt.Errorf(
			"native instance %s has tool_loop_warning=true; shadow replay requires warning-off input",
			result.InstanceID,
		)
	}
	if result.ToolLoopWarningCount != 0 || result.FirstToolLoopWarningLLMCall != nil ||
		len(result.ToolLoopWarningLLMCalls) != 0 {
		return fmt.Errorf(
			"native instance %s contains tool-loop warning telemetry; shadow replay requires pristine warning-off input",
			result.InstanceID,
		)
	}
	return nil
}

func validateInstanceID(value string) error {
	if len(value) == 0 || len(value) > 200 || !instanceIDPattern.MatchString(value) {
		return fmt.Errorf(
			"unsafe instance directory %q: expected only letters, digits, dot, underscore, or hyphen",
			value,
		)
	}
	return nil
}

func boundedJoin(root, name string) (string, error) {
	joined := filepath.Join(root, name)
	relative, err := filepath.Rel(root, joined)
	if err != nil {
		return "", fmt.Errorf("validate artifact path %q: %w", name, err)
	}
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", fmt.Errorf("artifact path %q escapes its root", name)
	}
	return joined, nil
}

func readRegularFile(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file, not a symlink", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !sameFileSnapshot(before, opened) {
		return nil, fmt.Errorf("%s changed while it was opened", path)
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if after.Mode()&os.ModeSymlink != 0 || !sameFileSnapshot(opened, openedAfter) ||
		!sameFileSnapshot(openedAfter, after) {
		return nil, fmt.Errorf("%s changed while it was read", path)
	}
	return data, nil
}

func sameFileSnapshot(first, second os.FileInfo) bool {
	return os.SameFile(first, second) && first.Mode() == second.Mode() &&
		first.Size() == second.Size() && first.ModTime().Equal(second.ModTime())
}
