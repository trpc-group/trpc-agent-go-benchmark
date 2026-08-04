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
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
)

var lmeImmutableRevisionPattern = regexp.MustCompile(
	`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64}|sha256:[0-9a-fA-F]{64})$`,
)

const (
	lmeRunManifestSchemaVersion = 5
	lmeRunManifestFileName      = "run_manifest.json"
	lmeOfficialStatusEligible   = "eligible"
	lmeOfficialStatusBlocked    = "blocked"
	lmeDirtyStateClean          = "clean"
	lmeDirtyStateDirty          = "dirty"
	lmeDirtyStateUnknown        = "unknown"
)

type lmeRunManifest struct {
	SchemaVersion       int               `json:"schema_version"`
	CreatedAt           time.Time         `json:"created_at"`
	CompatibilityDigest string            `json:"compatibility_digest"`
	ComparisonDigest    string            `json:"comparison_digest"`
	Reproducible        bool              `json:"reproducible"`
	OfficialStatus      string            `json:"official_status"`
	OfficialBlockers    []string          `json:"official_blockers,omitempty"`
	Code                lmeCodeProvenance `json:"code"`
	Artifacts           lmeArtifactSet    `json:"artifacts"`
	CaseIDs             []string          `json:"case_ids"`
	Run                 lmeRunIdentity    `json:"run"`
	Config              map[string]any    `json:"config"`
	Unavailable         map[string]string `json:"unavailable,omitempty"`
}

type lmeCodeProvenance struct {
	GoVersion    string                 `json:"go_version,omitempty"`
	Benchmark    lmeBenchmarkProvenance `json:"benchmark"`
	RootRevision string                 `json:"trpc_agent_go_root_revision,omitempty"`
	AgentModules []lmeModuleProvenance  `json:"trpc_agent_go_modules,omitempty"`
}

type lmeBenchmarkProvenance struct {
	Revision   string `json:"revision,omitempty"`
	DirtyState string `json:"dirty_state"`
	Source     string `json:"source,omitempty"`
}

type lmeModuleProvenance struct {
	Path             string `json:"path"`
	RequestedVersion string `json:"requested_version,omitempty"`
	EffectivePath    string `json:"effective_path"`
	EffectiveVersion string `json:"effective_version,omitempty"`
	Revision         string `json:"revision,omitempty"`
	Checksum         string `json:"checksum,omitempty"`
	Replaced         bool   `json:"replaced,omitempty"`
	LocalReplacement bool   `json:"local_replacement,omitempty"`
	Resolved         bool   `json:"resolved"`
}

type lmeArtifactSet struct {
	Dataset         lmeArtifactProvenance `json:"dataset"`
	CaseManifest    lmeArtifactProvenance `json:"case_manifest"`
	CanonicalReplay lmeArtifactProvenance `json:"canonical_replay"`
	BuildPlan       lmeArtifactProvenance `json:"build_plan"`
}

type lmeArtifactProvenance struct {
	Configured        bool   `json:"configured"`
	Available         bool   `json:"available"`
	Path              string `json:"path,omitempty"`
	Digest            string `json:"digest,omitempty"`
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

type lmeRunIdentity struct {
	Scenario                     string `json:"scenario"`
	Backend                      string `json:"backend,omitempty"`
	Table                        string `json:"table,omitempty"`
	AutoUpdatePolicy             string `json:"auto_update_policy,omitempty"`
	ConversationExtraction       string `json:"conversation_extraction,omitempty"`
	TemporalContext              string `json:"temporal_context,omitempty"`
	ModelName                    string `json:"model_name"`
	EmbedModelName               string `json:"embed_model_name"`
	LLMEndpointFingerprint       string `json:"llm_endpoint_fingerprint"`
	EmbeddingEndpointFingerprint string `json:"embedding_endpoint_fingerprint"`
	TokenizerName                string `json:"tokenizer_name,omitempty"`
	EffectiveTopK                int    `json:"effective_top_k"`
	BuildProtocol                string `json:"build_protocol"`
	CaseManifestSchemaVersion    int    `json:"case_manifest_schema_version"`
	CaseManifestMethod           string `json:"case_manifest_method,omitempty"`
	CaseManifestSplit            string `json:"case_manifest_split,omitempty"`
	CaseManifestLegacy           bool   `json:"case_manifest_legacy"`
	BackendVersion               string `json:"backend_version,omitempty"`
	BackendRevision              string `json:"backend_revision,omitempty"`
}

type lmeRunManifestRequest struct {
	Scenario string
	Backend  string
	Table    string
	Config   lmeRunConfig
	CaseIDs  []string
}

type lmeRunCompatibility struct {
	Reproducible     bool                   `json:"reproducible"`
	OfficialStatus   string                 `json:"official_status"`
	OfficialBlockers []string               `json:"official_blockers,omitempty"`
	Code             lmeCodeProvenance      `json:"code"`
	Artifacts        lmeCompatibleArtifacts `json:"artifacts"`
	CaseIDs          []string               `json:"case_ids"`
	Run              lmeRunIdentity         `json:"run"`
	Config           map[string]any         `json:"config"`
}

type lmeComparisonCompatibility struct {
	Code      lmeCodeProvenance      `json:"code"`
	Artifacts lmeCompatibleArtifacts `json:"artifacts"`
	CaseIDs   []string               `json:"case_ids"`
	Run       lmeComparisonRun       `json:"run"`
	Config    map[string]any         `json:"config"`
}

type lmeComparisonRun struct {
	ModelName                    string `json:"model_name"`
	EmbedModelName               string `json:"embed_model_name"`
	LLMEndpointFingerprint       string `json:"llm_endpoint_fingerprint"`
	EmbeddingEndpointFingerprint string `json:"embedding_endpoint_fingerprint"`
	TokenizerName                string `json:"tokenizer_name,omitempty"`
	EffectiveTopK                int    `json:"effective_top_k"`
	BuildProtocol                string `json:"build_protocol"`
}

type lmeCompatibleArtifacts struct {
	Dataset         lmeCompatibleArtifact `json:"dataset"`
	CaseManifest    lmeCompatibleArtifact `json:"case_manifest"`
	CanonicalReplay lmeCompatibleArtifact `json:"canonical_replay"`
	BuildPlan       lmeCompatibleArtifact `json:"build_plan"`
}

type lmeCompatibleArtifact struct {
	Configured bool   `json:"configured"`
	Available  bool   `json:"available"`
	Digest     string `json:"digest,omitempty"`
}

func newLMERunManifestRequest(
	scenario string,
	backend string,
	cfg lmeRunConfig,
	instances []*dataset.LongMemEvalInstance,
) lmeRunManifestRequest {
	caseIDs := make([]string, 0, len(instances))
	for _, instance := range instances {
		if instance != nil {
			caseIDs = append(caseIDs, instance.QuestionID)
		}
	}
	table := ""
	if scenario == "auto" {
		table = cfg.AutoMemoryTable
	}
	return lmeRunManifestRequest{
		Scenario: scenario,
		Backend:  backend,
		Table:    table,
		Config:   cfg,
		CaseIDs:  caseIDs,
	}
}

// ensureLMERunManifest creates the run identity once. Resume is allowed only
// when every comparison-relevant field matches the existing manifest.
func ensureLMERunManifest(
	ctx context.Context,
	outputDir string,
	request lmeRunManifestRequest,
	resume bool,
) (*lmeRunManifest, error) {
	return ensureLMERunManifestWithDependencies(
		ctx,
		outputDir,
		request,
		resume,
		defaultLMEProvenanceDependencies(),
	)
}

func ensureLMERunManifestWithDependencies(
	ctx context.Context,
	outputDir string,
	request lmeRunManifestRequest,
	resume bool,
	deps lmeProvenanceDependencies,
) (*lmeRunManifest, error) {
	current, err := buildLMERunManifestAt(ctx, request, outputDir, deps)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(outputDir, lmeRunManifestFileName)
	if resume {
		stored, err := readLMERunManifest(path)
		if err != nil {
			return nil, fmt.Errorf("resume requires a valid immutable run manifest: %w", err)
		}
		if stored.Code.Benchmark.DirtyState != lmeDirtyStateClean ||
			current.Code.Benchmark.DirtyState != lmeDirtyStateClean {
			return nil, errors.New("resume requires a clean benchmark worktree")
		}
		if err := compareLMERunManifests(stored, current); err != nil {
			return nil, fmt.Errorf("resume provenance mismatch: %w", err)
		}
		return stored, nil
	}
	if err := rejectLegacyLMEOutputWithoutManifest(outputDir, path); err != nil {
		return nil, err
	}
	if err := writeLMERunManifestCreateOnce(path, current); err != nil {
		return nil, err
	}
	return current, nil
}

func buildLMERunManifestAt(
	ctx context.Context,
	request lmeRunManifestRequest,
	artifactBase string,
	deps lmeProvenanceDependencies,
) (*lmeRunManifest, error) {
	if deps.now == nil {
		deps.now = time.Now
	}
	code, _, unavailable := collectLMECodeProvenance(ctx, deps)
	var caseManifest *dataset.LongMemEvalManifest
	if request.Config.ManifestPath != "" {
		loadedManifest, err := dataset.LoadLongMemEvalManifest(request.Config.ManifestPath)
		if err != nil {
			return nil, fmt.Errorf("load LongMemEval case manifest provenance: %w", err)
		}
		caseManifest = loadedManifest
	}
	artifacts, _, artifactUnavailable, err := collectLMERunArtifactsAt(
		request.Config,
		artifactBase,
	)
	if err != nil {
		return nil, err
	}
	for key, value := range artifactUnavailable {
		unavailable[key] = value
	}
	run := lmeRunIdentity{
		Scenario:                     strings.TrimSpace(request.Scenario),
		Backend:                      strings.TrimSpace(request.Backend),
		Table:                        strings.TrimSpace(request.Table),
		ModelName:                    strings.TrimSpace(request.Config.ModelName),
		EmbedModelName:               strings.TrimSpace(request.Config.EmbedModelName),
		LLMEndpointFingerprint:       request.Config.LLMEndpointFingerprint,
		EmbeddingEndpointFingerprint: request.Config.EmbeddingEndpointFingerprint,
		TokenizerName:                strings.TrimSpace(request.Config.BuildTokenizer),
		EffectiveTopK:                lmeRetrievalTopK,
		BuildProtocol:                lmeBuildProtocol,
		BackendVersion:               sanitizeProvenanceIdentifier(request.Config.Mem0Version),
		BackendRevision:              sanitizeProvenanceIdentifier(request.Config.Mem0Revision),
	}
	if run.Scenario == "auto" {
		run.AutoUpdatePolicy = string(request.Config.AutoUpdatePolicy)
		run.ConversationExtraction = string(request.Config.ConversationExtraction)
		run.TemporalContext = lmeAutoTemporalContext
	} else if run.Scenario == "mem0_oss" {
		run.TemporalContext = lmeMem0TemporalContext
	}
	if caseManifest != nil {
		run.CaseManifestSchemaVersion = caseManifest.SchemaVersion
		run.CaseManifestMethod = string(caseManifest.Method)
		run.CaseManifestSplit = caseManifest.Split
		run.CaseManifestLegacy = caseManifest.IsLegacy()
	}
	_ = validateLMERunIdentity(run, unavailable)
	manifest := &lmeRunManifest{
		SchemaVersion: lmeRunManifestSchemaVersion,
		CreatedAt:     deps.now().UTC(),
		Code:          code,
		Artifacts:     artifacts,
		CaseIDs:       append([]string(nil), request.CaseIDs...),
		Run:           run,
		Config:        buildLMESanitizedConfig(request.Config),
		Unavailable:   unavailable,
	}
	manifest.OfficialBlockers = deriveLMERunManifestBlockers(manifest)
	manifest.Reproducible = len(manifest.OfficialBlockers) == 0
	manifest.OfficialStatus = lmeOfficialStatusBlocked
	if manifest.Reproducible {
		manifest.OfficialStatus = lmeOfficialStatusEligible
	}
	digest, err := calculateLMERunCompatibilityDigest(manifest)
	if err != nil {
		return nil, err
	}
	manifest.CompatibilityDigest = digest
	comparisonDigest, err := calculateLMEComparisonDigest(manifest)
	if err != nil {
		return nil, err
	}
	manifest.ComparisonDigest = comparisonDigest
	return manifest, nil
}

func collectLMERunArtifactsAt(
	cfg lmeRunConfig,
	artifactBase string,
) (lmeArtifactSet, []string, map[string]string, error) {
	datasetArtifact, err := collectLMEArtifact(cfg.DatasetPath, true)
	if err != nil {
		return lmeArtifactSet{}, nil, nil, fmt.Errorf("collect dataset provenance: %w", err)
	}
	datasetArtifact.Path = ""
	caseManifest, err := collectLMEArtifact(cfg.ManifestPath, false)
	if err != nil {
		return lmeArtifactSet{}, nil, nil, err
	}
	caseManifest.Path = ""
	sharedArtifactBase := artifactBase
	if sharedArtifactBase != "" {
		sharedArtifactBase = filepath.Dir(sharedArtifactBase)
	}
	canonicalReplay, err := collectLMEArtifactAt(cfg.ReplayRoot, false, sharedArtifactBase)
	if err != nil {
		return lmeArtifactSet{}, nil, nil, err
	}
	buildPlan, err := collectLMEArtifactAt(cfg.BuildPlanRoot, false, sharedArtifactBase)
	if err != nil {
		return lmeArtifactSet{}, nil, nil, err
	}
	artifacts := lmeArtifactSet{
		Dataset:         datasetArtifact,
		CaseManifest:    caseManifest,
		CanonicalReplay: canonicalReplay,
		BuildPlan:       buildPlan,
	}
	var blockers []string
	unavailable := make(map[string]string)
	for _, item := range []struct {
		name     string
		artifact lmeArtifactProvenance
	}{
		{name: "case_manifest", artifact: caseManifest},
		{name: "canonical_replay", artifact: canonicalReplay},
		{name: "build_plan", artifact: buildPlan},
	} {
		if item.artifact.Available {
			continue
		}
		unavailable["artifacts."+item.name] = item.artifact.UnavailableReason
		if item.artifact.Configured {
			blockers = append(blockers, item.name+" artifact is configured but unavailable")
		}
	}
	return artifacts, blockers, unavailable, nil
}

func validateLMERunIdentity(run lmeRunIdentity, unavailable map[string]string) []string {
	blockers := validateLMERunRequiredFields(run, unavailable)
	if run.BuildProtocol != "" && run.BuildProtocol != lmeBuildProtocol {
		blockers = append(blockers, fmt.Sprintf(
			"unsupported build protocol %q",
			run.BuildProtocol,
		))
		unavailable["run.build_protocol"] = "only turn-pair is supported"
	}
	if run.Backend == "" {
		unavailable["run.backend"] = "not applicable or not reported"
	}
	if run.Table == "" {
		unavailable["run.table"] = "not applicable or backend-managed"
	}
	blockers = append(blockers, validateLMERunScenarioIdentity(run, unavailable)...)
	if run.EffectiveTopK <= 0 {
		blockers = append(blockers, "effective_top_k is unavailable")
		unavailable["run.effective_top_k"] = "must be greater than zero"
	}
	if run.EffectiveTopK != 0 && run.EffectiveTopK != lmeRetrievalTopK {
		blockers = append(blockers, fmt.Sprintf(
			"effective_top_k is %d, want %d",
			run.EffectiveTopK,
			lmeRetrievalTopK,
		))
	}
	return append(blockers, validateLMERunManifestIdentity(run, unavailable)...)
}

func validateLMERunRequiredFields(run lmeRunIdentity, unavailable map[string]string) []string {
	fields := []struct {
		name  string
		value string
	}{
		{"scenario", run.Scenario},
		{"model_name", run.ModelName},
		{"embed_model_name", run.EmbedModelName},
		{"llm_endpoint_fingerprint", run.LLMEndpointFingerprint},
		{"embedding_endpoint_fingerprint", run.EmbeddingEndpointFingerprint},
		{"tokenizer_name", run.TokenizerName},
		{"build_protocol", run.BuildProtocol},
		{"temporal_context", run.TemporalContext},
	}
	var blockers []string
	for _, field := range fields {
		if field.value != "" {
			continue
		}
		blockers = append(blockers, field.name+" is unavailable")
		unavailable["run."+field.name] = "not configured"
	}
	return blockers
}

func validateLMERunScenarioIdentity(run lmeRunIdentity, unavailable map[string]string) []string {
	var blockers []string
	if run.Scenario == "auto" && run.AutoUpdatePolicy == "" {
		blockers = append(blockers, "auto_update_policy is unavailable")
		unavailable["run.auto_update_policy"] = "Auto runs must identify the update-policy treatment"
	}
	if run.Scenario == "auto" && run.ConversationExtraction == "" {
		blockers = append(blockers, "conversation_extraction is unavailable")
		unavailable["run.conversation_extraction"] =
			"Auto runs must identify the conversation-extraction treatment"
	}
	switch run.Scenario {
	case "auto":
		if run.TemporalContext != "" && run.TemporalContext != lmeAutoTemporalContext {
			blockers = append(blockers, fmt.Sprintf(
				"Auto temporal_context is %q, want %q",
				run.TemporalContext,
				lmeAutoTemporalContext,
			))
		}
	case "mem0_oss":
		if run.TemporalContext != "" && run.TemporalContext != lmeMem0TemporalContext {
			blockers = append(blockers, fmt.Sprintf(
				"Mem0 OSS temporal_context is %q, want %q",
				run.TemporalContext,
				lmeMem0TemporalContext,
			))
		}
	default:
	}
	return blockers
}

func validateLMERunManifestIdentity(run lmeRunIdentity, unavailable map[string]string) []string {
	var blockers []string
	if run.CaseManifestSchemaVersion != dataset.LongMemEvalManifestSchemaVersion {
		blockers = append(blockers, fmt.Sprintf(
			"case_manifest_schema_version is %d, want %d",
			run.CaseManifestSchemaVersion,
			dataset.LongMemEvalManifestSchemaVersion,
		))
		unavailable["run.case_manifest_schema_version"] = "the current rich case manifest schema is required"
	}
	if run.CaseManifestMethod == "" {
		blockers = append(blockers, "case_manifest_method is unavailable")
		unavailable["run.case_manifest_method"] = "a reproducible selection method is required"
	}
	return blockers
}

func deriveLMERunManifestBlockers(manifest *lmeRunManifest) []string {
	if manifest == nil {
		return []string{"run manifest is missing"}
	}
	unavailable := make(map[string]string)
	blockers := validateLMERunIdentity(manifest.Run, unavailable)
	blockers = append(blockers, validateLMEManifestMethodology(manifest.Run)...)
	blockers = append(blockers, validateLMECodeProvenance(manifest.Code)...)
	blockers = append(blockers, validateLMEArtifactProvenance(manifest.Artifacts)...)
	blockers = append(blockers, validateLMEMem0Provenance(manifest)...)
	blockers = append(blockers, validateLMEManifestConfig(manifest)...)
	return uniqueSortedStrings(blockers)
}

func validateLMEManifestMethodology(run lmeRunIdentity) []string {
	var blockers []string
	if run.Scenario != "auto" && run.Scenario != "mem0_oss" {
		blockers = append(blockers, "scenario is not eligible for maintained comparison")
	}
	if run.CaseManifestLegacy {
		blockers = append(blockers, "case manifest uses the legacy case_ids-only schema")
	}
	switch dataset.LongMemEvalManifestMethod(run.CaseManifestMethod) {
	case dataset.LongMemEvalManifestMethodFullCategory:
		if run.CaseManifestSplit != "" {
			blockers = append(blockers, "full-category case manifest must not declare a split")
		}
	case dataset.LongMemEvalManifestMethodStratifiedSHA256:
		if run.CaseManifestSplit != dataset.LongMemEvalManifestSplitDev &&
			run.CaseManifestSplit != dataset.LongMemEvalManifestSplitHoldout {
			blockers = append(blockers, "sampled case manifest must declare a dev or holdout split")
		}
	case dataset.LongMemEvalManifestMethodLegacyFirst:
		blockers = append(blockers, "legacy-first case selection is not eligible for maintained comparison")
	}
	return blockers
}

func validateLMECodeProvenance(code lmeCodeProvenance) []string {
	var blockers []string
	if code.Benchmark.Revision == "" {
		blockers = append(blockers, "benchmark Git revision is unavailable")
	}
	switch code.Benchmark.DirtyState {
	case lmeDirtyStateDirty:
		blockers = append(blockers, "benchmark worktree is dirty")
	case lmeDirtyStateClean:
	default:
		blockers = append(blockers, "benchmark worktree state is unavailable")
	}
	if code.RootRevision == "" {
		blockers = append(blockers, "trpc-agent-go root module revision is unavailable")
	}
	if len(code.AgentModules) == 0 {
		blockers = append(blockers, "trpc-agent-go module provenance is unavailable")
	}
	for _, module := range code.AgentModules {
		if module.LocalReplacement {
			blockers = append(blockers, fmt.Sprintf("module %s uses a local replacement", module.Path))
		}
		if !module.Resolved {
			blockers = append(blockers, fmt.Sprintf("module %s is unresolved", module.Path))
		}
		if module.Revision == "" {
			blockers = append(blockers, fmt.Sprintf("module %s revision is unavailable", module.Path))
		}
	}
	return blockers
}

func validateLMEArtifactProvenance(artifacts lmeArtifactSet) []string {
	var blockers []string
	for _, item := range []struct {
		name         string
		artifact     lmeArtifactProvenance
		pathRequired bool
	}{
		{name: "dataset", artifact: artifacts.Dataset},
		{name: "case_manifest", artifact: artifacts.CaseManifest},
		{name: "canonical_replay", artifact: artifacts.CanonicalReplay, pathRequired: true},
		{name: "build_plan", artifact: artifacts.BuildPlan, pathRequired: true},
	} {
		if !item.artifact.Configured || !item.artifact.Available ||
			!strings.HasPrefix(item.artifact.Digest, lmeDigestAlgorithm+":") ||
			(item.pathRequired && (item.artifact.Path == "" || filepath.IsAbs(item.artifact.Path))) {
			blockers = append(blockers, item.name+" artifact is unavailable")
		}
	}
	return blockers
}

func validateLMEMem0Provenance(manifest *lmeRunManifest) []string {
	if manifest.Run.Scenario != "mem0_oss" {
		return nil
	}
	var blockers []string
	if strings.TrimSpace(manifest.Run.BackendVersion) == "" {
		blockers = append(blockers, "Mem0 OSS version is unavailable")
	}
	revision := strings.TrimSpace(manifest.Run.BackendRevision)
	if revision == "" {
		blockers = append(blockers, "Mem0 OSS commit or image digest is unavailable")
	} else if !lmeImmutableRevisionPattern.MatchString(revision) {
		blockers = append(blockers, "Mem0 OSS revision must be a full Git commit or image digest")
	}
	for _, key := range []string{"mem0_preflight_digest", "mem0_environment_lock_digest"} {
		if !strings.HasPrefix(lmeConfigString(manifest.Config, key), lmeDigestAlgorithm+":") {
			blockers = append(blockers, key+" is unavailable")
		}
	}
	if !lmeConfigBool(manifest.Config, "mem0_observation_prompt_verified") {
		blockers = append(blockers, "Mem0 OSS observation-prompt capability is not verified")
	}
	if lmeConfigString(manifest.Config, "mem0_runtime_llm_model") != manifest.Run.ModelName {
		blockers = append(blockers, "Mem0 OSS runtime LLM model does not match the benchmark model")
	}
	if lmeConfigString(manifest.Config, "mem0_runtime_embed_model") != manifest.Run.EmbedModelName {
		blockers = append(blockers, "Mem0 OSS runtime embedding model does not match the benchmark model")
	}
	return blockers
}

func validateLMEManifestConfig(manifest *lmeRunManifest) []string {
	var blockers []string
	if manifest.Run.Scenario == "auto" && lmeConfigBool(manifest.Config, "auto_qa_only") {
		blockers = append(blockers, "auto QA-only reuse is not eligible for a maintained build comparison")
	}
	if got := lmeConfigString(manifest.Config, "temporal_reference_source"); got != lmeTemporalReferenceSource {
		blockers = append(blockers, fmt.Sprintf(
			"temporal_reference_source is %q, want %q",
			got,
			lmeTemporalReferenceSource,
		))
	}
	if got := lmeConfigString(manifest.Config, "temporal_reference_format"); got != lmeTemporalReferenceFormat {
		blockers = append(blockers, fmt.Sprintf(
			"temporal_reference_format is %q, want %q",
			got,
			lmeTemporalReferenceFormat,
		))
	}
	traceMode := lmeConfigString(manifest.Config, "trace_content_mode")
	if traceMode == string(lmeTraceContentFull) {
		blockers = append(blockers, "full build-trace content is local diagnostic only")
	}
	if traceMode != string(lmeTraceContentHash) && traceMode != string(lmeTraceContentNone) {
		blockers = append(blockers, "maintained build-trace content mode must be hash or none")
	}
	return blockers
}

func calculateLMERunCompatibilityDigest(manifest *lmeRunManifest) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("nil LongMemEval run manifest")
	}
	compatibility := lmeRunCompatibility{
		Reproducible:     manifest.Reproducible,
		OfficialStatus:   manifest.OfficialStatus,
		OfficialBlockers: append([]string(nil), manifest.OfficialBlockers...),
		Code:             manifest.Code,
		Artifacts:        compatibleLMEArtifacts(manifest.Artifacts),
		CaseIDs:          append([]string(nil), manifest.CaseIDs...),
		Run:              manifest.Run,
		Config:           normalizeLMEProvenanceConfig(manifest.Config),
	}
	data, err := json.Marshal(compatibility)
	if err != nil {
		return "", fmt.Errorf("marshal LongMemEval run compatibility: %w", err)
	}
	digest := sha256.Sum256(data)
	return lmeDigestAlgorithm + ":" + hex.EncodeToString(digest[:]), nil
}

func calculateLMEComparisonDigest(manifest *lmeRunManifest) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("nil LongMemEval run manifest")
	}
	comparisonCode := manifest.Code
	comparisonCode.Benchmark.Source = ""
	comparison := lmeComparisonCompatibility{
		Code:      comparisonCode,
		Artifacts: compatibleLMEArtifacts(manifest.Artifacts),
		CaseIDs:   append([]string(nil), manifest.CaseIDs...),
		Run: lmeComparisonRun{
			ModelName:                    manifest.Run.ModelName,
			EmbedModelName:               manifest.Run.EmbedModelName,
			LLMEndpointFingerprint:       manifest.Run.LLMEndpointFingerprint,
			EmbeddingEndpointFingerprint: manifest.Run.EmbeddingEndpointFingerprint,
			TokenizerName:                manifest.Run.TokenizerName,
			EffectiveTopK:                manifest.Run.EffectiveTopK,
			BuildProtocol:                manifest.Run.BuildProtocol,
		},
		Config: normalizeLMEProvenanceConfig(selectLMEComparisonConfig(manifest.Config)),
	}
	data, err := json.Marshal(comparison)
	if err != nil {
		return "", fmt.Errorf("marshal LongMemEval comparison compatibility: %w", err)
	}
	digest := sha256.Sum256(data)
	return lmeDigestAlgorithm + ":" + hex.EncodeToString(digest[:]), nil
}

func compatibleLMEArtifacts(artifacts lmeArtifactSet) lmeCompatibleArtifacts {
	convert := func(artifact lmeArtifactProvenance) lmeCompatibleArtifact {
		return lmeCompatibleArtifact{
			Configured: artifact.Configured,
			Available:  artifact.Available,
			Digest:     artifact.Digest,
		}
	}
	return lmeCompatibleArtifacts{
		Dataset:         convert(artifacts.Dataset),
		CaseManifest:    convert(artifacts.CaseManifest),
		CanonicalReplay: convert(artifacts.CanonicalReplay),
		BuildPlan:       convert(artifacts.BuildPlan),
	}
}

func compareLMERunManifests(stored, current *lmeRunManifest) error {
	if stored.SchemaVersion != current.SchemaVersion {
		return fmt.Errorf("schema version changed from %d to %d", stored.SchemaVersion, current.SchemaVersion)
	}
	oldArtifacts := compatibleLMEArtifacts(stored.Artifacts)
	newArtifacts := compatibleLMEArtifacts(current.Artifacts)
	artifacts := []struct {
		name            string
		stored, current lmeCompatibleArtifact
	}{
		{"dataset", oldArtifacts.Dataset, newArtifacts.Dataset},
		{"case manifest", oldArtifacts.CaseManifest, newArtifacts.CaseManifest},
		{"canonical replay", oldArtifacts.CanonicalReplay, newArtifacts.CanonicalReplay},
		{"build plan", oldArtifacts.BuildPlan, newArtifacts.BuildPlan},
	}
	for _, artifact := range artifacts {
		if !reflect.DeepEqual(artifact.stored, artifact.current) {
			return fmt.Errorf("%s artifact changed", artifact.name)
		}
	}
	if err := compareLMERunIdentity(stored.Run, current.Run); err != nil {
		return err
	}
	if !reflect.DeepEqual(stored.CaseIDs, current.CaseIDs) {
		return errors.New("case IDs or order changed")
	}
	if !reflect.DeepEqual(stored.Code, current.Code) {
		return errors.New("benchmark or trpc-agent-go code provenance changed")
	}
	if !equalLMEJSON(stored.Config, current.Config) {
		return errors.New("effective configuration changed")
	}
	if stored.CompatibilityDigest != current.CompatibilityDigest {
		return errors.New("compatibility digest changed")
	}
	if stored.ComparisonDigest != current.ComparisonDigest {
		return errors.New("comparison digest changed")
	}
	return nil
}

func compareLMERunIdentity(stored, current lmeRunIdentity) error {
	fields := []struct {
		name            string
		stored, current any
	}{
		{"scenario", stored.Scenario, current.Scenario},
		{"backend", stored.Backend, current.Backend},
		{"table", stored.Table, current.Table},
		{"model", stored.ModelName, current.ModelName},
		{"embedding model", stored.EmbedModelName, current.EmbedModelName},
		{"tokenizer", stored.TokenizerName, current.TokenizerName},
		{"effective top-k", stored.EffectiveTopK, current.EffectiveTopK},
		{"build protocol", stored.BuildProtocol, current.BuildProtocol},
		{"case manifest schema", stored.CaseManifestSchemaVersion, current.CaseManifestSchemaVersion},
		{"case manifest method", stored.CaseManifestMethod, current.CaseManifestMethod},
		{"case manifest split", stored.CaseManifestSplit, current.CaseManifestSplit},
		{"case manifest legacy status", stored.CaseManifestLegacy, current.CaseManifestLegacy},
		{"backend version", stored.BackendVersion, current.BackendVersion},
		{"backend revision", stored.BackendRevision, current.BackendRevision},
	}
	for _, field := range fields {
		if !reflect.DeepEqual(field.stored, field.current) {
			return fmt.Errorf("%s changed from %v to %v", field.name, field.stored, field.current)
		}
	}
	return nil
}

// Run manifest configuration normalization.

func validateLMEProxyRunID(runID string) error {
	if runID == "" {
		return nil
	}
	if len(runID) > 128 {
		return fmt.Errorf("LongMemEval Mem0 proxy run ID exceeds 128 bytes")
	}
	for _, r := range runID {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' ||
			r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' {
			continue
		}
		return fmt.Errorf(
			"LongMemEval Mem0 proxy run ID contains unsupported character %q",
			r,
		)
	}
	return nil
}

func lmeConfigBool(config map[string]any, key string) bool {
	value, _ := config[key].(bool)
	return value
}

func lmeConfigString(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}

func buildLMESanitizedConfig(cfg lmeRunConfig) map[string]any {
	raw := map[string]any{
		"question_types":                   append([]string(nil), cfg.QuestionTypes...),
		"max_tasks":                        cfg.MaxTasks,
		"retrieval_top_k":                  cfg.RetrievalTopK,
		"build_max_tokens":                 cfg.BuildMaxTokens,
		"build_tokenizer":                  cfg.BuildTokenizer,
		"build_tokenizer_model":            cfg.BuildTokenizerModel,
		"build_tokenizer_encoding":         cfg.BuildTokenizerEncoding,
		"replay_digest":                    cfg.ReplayDigest,
		"build_plan_digest":                cfg.BuildPlanDigest,
		"build_plan_root":                  sanitizeLocation(cfg.BuildPlanRoot),
		"build_stats":                      cfg.BuildStats,
		"temporal_reference_source":        lmeTemporalReferenceSource,
		"temporal_reference_format":        lmeTemporalReferenceFormat,
		"max_retries":                      cfg.MaxRetries,
		"answer_max_tokens":                cfg.AnswerMaxTokens,
		"judge_max_tokens":                 cfg.JudgeMaxTokens,
		"auto_extraction_wait":             cfg.AutoExtractionWait.String(),
		"auto_qa_only":                     cfg.AutoQAOnly,
		"auto_update_policy":               string(cfg.AutoUpdatePolicy),
		"conversation_extraction":          cfg.ConversationExtraction,
		"embedding_cache_enabled":          cfg.EmbeddingCacheEnabled,
		"embedding_cache_path":             sanitizeLocation(cfg.EmbeddingCachePath),
		"transport_retry_enabled":          cfg.TransportRetryEnabled,
		"transport_retry_strategy":         cfg.TransportRetryStrategy,
		"full_qa_log":                      cfg.FullQALog,
		"replay_root":                      sanitizeLocation(cfg.ReplayRoot),
		"mem0_host":                        cfg.Mem0Host,
		"mem0_auth_configured":             cfg.Mem0APIKeySet,
		"mem0_version":                     sanitizeProvenanceIdentifier(cfg.Mem0Version),
		"mem0_revision":                    sanitizeProvenanceIdentifier(cfg.Mem0Revision),
		"mem0_preflight_path":              sanitizeLocation(cfg.Mem0PreflightPath),
		"mem0_preflight_digest":            cfg.Mem0PreflightDigest,
		"mem0_environment_lock_digest":     cfg.Mem0EnvironmentLockDigest,
		"mem0_runtime_llm_model":           sanitizeProvenanceIdentifier(cfg.Mem0RuntimeLLMModel),
		"mem0_runtime_embed_model":         sanitizeProvenanceIdentifier(cfg.Mem0RuntimeEmbedModel),
		"mem0_observation_prompt_verified": cfg.Mem0ObservationPromptVerified,
		"mem0_ingest_timeout":              cfg.Mem0IngestTimeout.String(),
		"mem0_proxy_usage_log":             sanitizeLocation(cfg.Mem0ProxyUsageLog),
		"mem0_proxy_run_id":                sanitizeProvenanceIdentifier(cfg.Mem0ProxyRunID),
		"trace_content_mode":               string(cfg.TraceContentMode),
		"trace_gzip":                       cfg.TraceGzip,
	}
	return sanitizeProvenanceMap(raw)
}

func normalizeLMEProvenanceConfig(config map[string]any) map[string]any {
	if config == nil {
		return nil
	}
	data, err := json.Marshal(config)
	if err != nil {
		return config
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var normalized map[string]any
	if err := decoder.Decode(&normalized); err != nil {
		return config
	}
	value, ok := normalizeLMEProvenanceValue(normalized).(map[string]any)
	if !ok {
		return config
	}
	return value
}

func normalizeLMEProvenanceValue(value any) any {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := strconv.ParseFloat(string(typed), 64)
		if err == nil && parsed == 0 {
			return json.Number("0")
		}
		return typed
	case float64:
		if typed == 0 {
			typed = 0
		}
		data, _ := json.Marshal(typed)
		return json.Number(string(data))
	case float32:
		number := float64(typed)
		if number == 0 {
			number = 0
		}
		data, _ := json.Marshal(number)
		return json.Number(string(data))
	case map[string]any:
		normalized := make(map[string]any, len(typed))
		for key, item := range typed {
			normalized[key] = normalizeLMEProvenanceValue(item)
		}
		return normalized
	case []any:
		normalized := make([]any, len(typed))
		for i, item := range typed {
			normalized[i] = normalizeLMEProvenanceValue(item)
		}
		return normalized
	default:
		return value
	}
}

func selectLMEComparisonConfig(config map[string]any) map[string]any {
	keys := []string{
		"question_types",
		"max_tasks",
		"retrieval_top_k",
		"build_max_tokens",
		"build_tokenizer",
		"build_tokenizer_model",
		"build_tokenizer_encoding",
		"build_stats",
		"replay_digest",
		"build_plan_digest",
		"max_retries",
		"answer_max_tokens",
		"judge_max_tokens",
		"transport_retry_enabled",
		"transport_retry_strategy",
		"temporal_reference_source",
		"temporal_reference_format",
	}
	selected := make(map[string]any, len(keys))
	for _, key := range keys {
		selected[key] = config[key]
	}
	return selected
}
