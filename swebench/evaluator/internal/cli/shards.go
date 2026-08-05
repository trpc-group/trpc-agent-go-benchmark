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
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

type shardsManifest struct {
	GeneratedAt          time.Time           `json:"generated_at"`
	PlanPath             string              `json:"plan_path"`
	RunsRoot             string              `json:"runs_root"`
	RawSubdir            string              `json:"raw_subdir"`
	ExpectedCases        int                 `json:"expected_cases"`
	AcceptedCases        int                 `json:"accepted_cases"`
	MissingCases         int                 `json:"missing_cases"`
	InvalidCases         int                 `json:"invalid_cases"`
	DuplicateCases       int                 `json:"duplicate_cases"`
	MissingIDs           []string            `json:"missing_ids"`
	InvalidIDs           []string            `json:"invalid_ids"`
	DuplicateIDs         []string            `json:"duplicate_ids"`
	StartedAt            string              `json:"started_at,omitempty"`
	FinishedAt           string              `json:"finished_at,omitempty"`
	WallDurationMS       int64               `json:"wall_duration_ms,omitempty"`
	CumulativeDurationMS int64               `json:"cumulative_duration_ms"`
	ExitStatusCounts     map[string]int      `json:"exit_status_counts"`
	RunnerIdentity       shardRunnerIdentity `json:"runner_identity"`
	Shards               []shardSummary      `json:"shards"`
}

type shardRunnerIdentity struct {
	ManifestKind            string `json:"manifest_kind"`
	RunnerType              string `json:"runner_type"`
	ObservationCodec        string `json:"observation_codec,omitempty"`
	FrameworkModule         string `json:"framework_module,omitempty"`
	FrameworkVersion        string `json:"framework_version,omitempty"`
	SourceRevision          string `json:"source_revision,omitempty"`
	SourceModified          bool   `json:"source_modified"`
	BinarySHA256            string `json:"binary_sha256,omitempty"`
	CasesSHA256             string `json:"cases_sha256,omitempty"`
	ModelConfigSHA256       string `json:"model_config_sha256,omitempty"`
	EnvironmentConfigSHA256 string `json:"environment_config_sha256,omitempty"`
	CommandTimeout          string `json:"command_timeout,omitempty"`
	CaseTimeout             string `json:"case_timeout,omitempty"`
}

type shardSummary struct {
	Index                   int                 `json:"index"`
	Name                    string              `json:"name"`
	RunID                   string              `json:"run_id"`
	RawDir                  string              `json:"raw_dir"`
	Status                  string              `json:"status"`
	FailureReason           string              `json:"failure_reason,omitempty"`
	ExpectedCount           int                 `json:"expected_count"`
	PredictionsCount        int                 `json:"predictions_count"`
	PredictionsSHA256       string              `json:"predictions_sha256,omitempty"`
	SelectedInstancesSHA256 string              `json:"selected_instances_sha256,omitempty"`
	AcceptedCount           int                 `json:"accepted_count"`
	MissingCount            int                 `json:"missing_count"`
	InvalidCount            int                 `json:"invalid_count"`
	EmptyPatchCount         int                 `json:"empty_patch_count"`
	Workers                 int                 `json:"workers,omitempty"`
	StartedAt               string              `json:"started_at,omitempty"`
	FinishedAt              string              `json:"finished_at,omitempty"`
	DurationMS              int64               `json:"duration_ms,omitempty"`
	ExitStatusCounts        map[string]int      `json:"exit_status_counts"`
	RunnerIdentity          shardRunnerIdentity `json:"runner_identity"`
	ExpectedIDs             []string            `json:"expected_ids"`
	Cases                   []shardCaseSummary  `json:"cases"`
	identityValidated       bool                `json:"-"`
}

type shardCaseSummary struct {
	InstanceID    string `json:"instance_id"`
	Status        string `json:"status"`
	ExitStatus    string `json:"exit_status,omitempty"`
	Reason        string `json:"reason,omitempty"`
	HasPrediction bool   `json:"has_prediction"`
	EmptyPatch    bool   `json:"empty_patch,omitempty"`
	PatchChars    int    `json:"patch_chars,omitempty"`
	TracePath     string `json:"trace_path,omitempty"`
}

func runSummarizeShards(args []string) error {
	fs := newFlagSet("summarize-shards")
	planPath := fs.String("plan", "", "batch plan.json path")
	runsRoot := fs.String("runs-root", "results/runs", "directory containing shard run directories")
	rawSubdir := fs.String("raw-subdir", filepath.Join("raw", "mini"), "raw output subdirectory under each run")
	output := fs.String("output", "", "output shards manifest path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "plan", *planPath); err != nil {
		return err
	}
	if err := required(fs, "output", *output); err != nil {
		return err
	}
	var plan batchPlan
	if err := readJSONFile(*planPath, &plan); err != nil {
		return fmt.Errorf("read plan: %w", err)
	}
	manifest, err := summarizeShardPlan(plan, *planPath, *runsRoot, *rawSubdir)
	if err != nil {
		return err
	}
	if err := writeJSON(*output, manifest); err != nil {
		return err
	}
	fmt.Printf("accepted=%d missing=%d invalid=%d duplicate=%d shards=%d\nmanifest=%s\n",
		manifest.AcceptedCases, manifest.MissingCases, manifest.InvalidCases, manifest.DuplicateCases, len(manifest.Shards), *output)
	return nil
}

func summarizeShardPlan(plan batchPlan, planPath, runsRoot, rawSubdir string) (shardsManifest, error) {
	if err := validateBatchPlan(plan); err != nil {
		return shardsManifest{}, fmt.Errorf("validate batch plan: %w", err)
	}
	manifest := shardsManifest{
		GeneratedAt:      time.Now().UTC(),
		PlanPath:         absPath(planPath),
		RunsRoot:         absPath(runsRoot),
		RawSubdir:        rawSubdir,
		ExpectedCases:    plan.CaseCount,
		MissingIDs:       []string{},
		InvalidIDs:       []string{},
		DuplicateIDs:     []string{},
		ExitStatusCounts: map[string]int{},
	}
	acceptedSeen := map[string]int{}
	var canonicalIdentity shardRunnerIdentity
	canonicalIdentitySet := false
	var earliest, latest time.Time
	for _, batch := range plan.Batches {
		shard := summarizeShard(batch, runsRoot, rawSubdir)
		if shard.identityValidated {
			if !canonicalIdentitySet {
				canonicalIdentity = shard.RunnerIdentity
				canonicalIdentitySet = true
				manifest.RunnerIdentity = canonicalIdentity
			} else if mismatch := shardRunnerIdentityMismatch(canonicalIdentity, shard.RunnerIdentity); mismatch != "" {
				shard.Status = "failed"
				if shard.FailureReason != "" {
					shard.FailureReason += "; "
				}
				shard.FailureReason += "runner identity mismatch: " + mismatch
			}
		}
		manifest.Shards = append(manifest.Shards, shard)
		manifest.MissingCases += shard.MissingCount
		manifest.InvalidCases += shard.InvalidCount
		manifest.CumulativeDurationMS += shard.DurationMS
		for status, count := range shard.ExitStatusCounts {
			manifest.ExitStatusCounts[status] += count
		}
		for _, c := range shard.Cases {
			switch c.Status {
			case "accepted":
				acceptedSeen[c.InstanceID]++
			case "missing":
				manifest.MissingIDs = append(manifest.MissingIDs, c.InstanceID)
			default:
				manifest.InvalidIDs = append(manifest.InvalidIDs, c.InstanceID)
			}
		}
		if shard.StartedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, shard.StartedAt); err == nil {
				if earliest.IsZero() || t.Before(earliest) {
					earliest = t
				}
			}
		}
		if shard.FinishedAt != "" {
			if t, err := time.Parse(time.RFC3339Nano, shard.FinishedAt); err == nil {
				if latest.IsZero() || t.After(latest) {
					latest = t
				}
			}
		}
	}
	for id, count := range acceptedSeen {
		if count > 1 {
			manifest.DuplicateIDs = append(manifest.DuplicateIDs, id)
		}
	}
	sort.Strings(manifest.MissingIDs)
	sort.Strings(manifest.InvalidIDs)
	sort.Strings(manifest.DuplicateIDs)
	manifest.AcceptedCases = len(acceptedSeen)
	manifest.DuplicateCases = len(manifest.DuplicateIDs)
	if !earliest.IsZero() {
		manifest.StartedAt = earliest.Format(time.RFC3339Nano)
	}
	if !latest.IsZero() {
		manifest.FinishedAt = latest.Format(time.RFC3339Nano)
	}
	if !earliest.IsZero() && !latest.IsZero() && latest.After(earliest) {
		manifest.WallDurationMS = latest.Sub(earliest).Milliseconds()
	}
	return manifest, nil
}

func validateBatchPlan(plan batchPlan) error {
	if plan.CaseCount < 1 || len(plan.Batches) == 0 {
		return fmt.Errorf("batch plan contains no cases")
	}
	if plan.BatchCount != len(plan.Batches) {
		return fmt.Errorf("batch_count %d does not match %d batches", plan.BatchCount, len(plan.Batches))
	}
	seenRuns := map[string]struct{}{}
	seenCases := map[string]struct{}{}
	total := 0
	for position, batch := range plan.Batches {
		if batch.Index != position {
			return fmt.Errorf("batch at position %d has index %d", position, batch.Index)
		}
		if err := validateArtifactName("batch name", batch.Name); err != nil {
			return err
		}
		if err := validateArtifactName("batch run id", batch.RunID); err != nil {
			return err
		}
		if _, ok := seenRuns[batch.RunID]; ok {
			return fmt.Errorf("duplicate batch run id %q", batch.RunID)
		}
		seenRuns[batch.RunID] = struct{}{}
		if batch.Size != len(batch.InstanceIDs) || batch.Size == 0 {
			return fmt.Errorf("batch %s size %d does not match %d instance ids", batch.Name, batch.Size, len(batch.InstanceIDs))
		}
		for _, id := range batch.InstanceIDs {
			if err := validateArtifactName("batch instance id", id); err != nil {
				return err
			}
			if _, ok := seenCases[id]; ok {
				return fmt.Errorf("duplicate planned instance id %q", id)
			}
			seenCases[id] = struct{}{}
		}
		total += batch.Size
	}
	if total != plan.CaseCount {
		return fmt.Errorf("case_count %d does not match %d planned instances", plan.CaseCount, total)
	}
	return nil
}

func summarizeShard(batch batchPlanItem, runsRoot, rawSubdir string) shardSummary {
	rawDir := filepath.Join(runsRoot, batch.RunID, rawSubdir)
	shard := shardSummary{
		Index:            batch.Index,
		Name:             batch.Name,
		RunID:            batch.RunID,
		RawDir:           absPath(rawDir),
		Status:           "failed",
		ExpectedCount:    len(batch.InstanceIDs),
		ExitStatusCounts: map[string]int{},
		ExpectedIDs:      append([]string(nil), batch.InstanceIDs...),
	}
	loadShardRunnerManifest(batch, rawDir, &shard)
	preds, predictionsSHA256, err := readShardPredictionsSnapshot(filepath.Join(rawDir, "preds.json"))
	if err == nil {
		shard.PredictionsCount = len(preds)
		shard.PredictionsSHA256 = predictionsSHA256
	} else if shard.FailureReason == "" {
		shard.FailureReason = "missing or invalid preds.json"
	}
	expected := make(map[string]struct{}, len(batch.InstanceIDs))
	for _, id := range batch.InstanceIDs {
		expected[id] = struct{}{}
	}
	unexpected := make([]string, 0)
	for id := range preds {
		if _, ok := expected[id]; !ok {
			unexpected = append(unexpected, id)
		}
	}
	sort.Strings(unexpected)
	for _, id := range unexpected {
		shard.Cases = append(shard.Cases, shardCaseSummary{
			InstanceID:    id,
			Status:        "invalid",
			Reason:        "unexpected prediction",
			HasPrediction: true,
		})
		shard.InvalidCount++
	}
	for _, id := range batch.InstanceIDs {
		caseSummary := summarizeShardCase(rawDir, id, preds)
		shard.Cases = append(shard.Cases, caseSummary)
		switch caseSummary.Status {
		case "accepted":
			shard.AcceptedCount++
			if caseSummary.EmptyPatch {
				shard.EmptyPatchCount++
			}
			shard.ExitStatusCounts[caseSummary.ExitStatus]++
		case "missing":
			shard.MissingCount++
		default:
			shard.InvalidCount++
		}
	}
	switch {
	case shard.FailureReason != "":
		shard.Status = "failed"
	case shard.MissingCount == 0 && shard.InvalidCount == 0:
		shard.Status = "accepted"
	case shard.AcceptedCount > 0:
		shard.Status = "partial"
	default:
		shard.Status = "failed"
	}
	return shard
}

func loadShardRunnerManifest(batch batchPlanItem, rawDir string, shard *shardSummary) {
	legacyPath := filepath.Join(rawDir, "run-mini-manifest.json")
	miniGoPath := filepath.Join(rawDir, "mini-go-runner-manifest.json")
	legacyExists, legacyErr := artifactPathExists(legacyPath)
	miniGoExists, miniGoErr := artifactPathExists(miniGoPath)
	if legacyErr != nil {
		shard.FailureReason = fmt.Sprintf("inspect run-mini-manifest.json: %v", legacyErr)
		return
	}
	if miniGoErr != nil {
		shard.FailureReason = fmt.Sprintf("inspect mini-go-runner-manifest.json: %v", miniGoErr)
		return
	}
	if legacyExists && miniGoExists {
		shard.FailureReason = "ambiguous runner manifests: both run-mini-manifest.json and mini-go-runner-manifest.json exist"
		return
	}
	if !legacyExists && !miniGoExists {
		shard.FailureReason = "missing runner manifest: expected run-mini-manifest.json or mini-go-runner-manifest.json"
		return
	}
	if legacyExists {
		loadLegacyShardManifest(batch, rawDir, legacyPath, shard)
		return
	}
	loadMiniGoShardManifest(batch, rawDir, miniGoPath, shard)
}

func loadLegacyShardManifest(batch batchPlanItem, rawDir, manifestPath string, shard *shardSummary) {
	var manifest runMiniManifest
	if err := readJSONFile(manifestPath, &manifest); err != nil {
		shard.FailureReason = "missing or invalid run-mini-manifest.json"
		return
	}
	shard.Workers = manifest.Config.Workers
	shard.StartedAt = manifest.StartedAt.UTC().Format(time.RFC3339Nano)
	shard.FinishedAt = manifest.FinishedAt.UTC().Format(time.RFC3339Nano)
	shard.DurationMS = manifest.DurationMS
	shard.RunnerIdentity = shardRunnerIdentity{
		ManifestKind: "legacy-run-mini",
		RunnerType:   "mini-swe-agent-python",
	}
	if manifest.Command.ExitCode != 0 {
		shard.FailureReason = fmt.Sprintf("run-mini exit code %d", manifest.Command.ExitCode)
	} else if manifest.RunID != batch.RunID {
		shard.FailureReason = fmt.Sprintf("run-mini run_id %q does not match %q", manifest.RunID, batch.RunID)
	} else if !sameArtifactPath(manifest.Config.OutputDir, rawDir) {
		shard.FailureReason = fmt.Sprintf("run-mini output_dir %q does not match %q", manifest.Config.OutputDir, rawDir)
	} else {
		shard.identityValidated = true
	}
}

func loadMiniGoShardManifest(batch batchPlanItem, rawDir, manifestPath string, shard *shardSummary) {
	var manifest runnerManifest
	if err := readJSONFile(manifestPath, &manifest); err != nil {
		shard.FailureReason = fmt.Sprintf("invalid mini-go-runner-manifest.json: %v", err)
		return
	}
	shard.Workers = manifest.Workers
	shard.StartedAt = formatTime(manifest.StartedAt)
	shard.FinishedAt = formatTime(manifest.FinishedAt)
	shard.DurationMS = manifest.DurationMS
	shard.SelectedInstancesSHA256 = manifest.SelectedInstancesSHA256
	identity, identityErr := normalizeShardRunnerIdentity(miniGoShardRunnerIdentity(manifest))
	shard.RunnerIdentity = identity
	if identityErr != nil {
		shard.FailureReason = fmt.Sprintf("mini-go runner identity is invalid: %v", identityErr)
		return
	}
	if err := validateMiniGoShardManifest(batch, rawDir, manifest); err != nil {
		shard.FailureReason = err.Error()
	} else {
		shard.identityValidated = true
	}
}

func validateMiniGoShardManifest(batch batchPlanItem, rawDir string, manifest runnerManifest) error {
	if manifest.RunID != batch.RunID {
		return fmt.Errorf("mini-go run_id %q does not match %q", manifest.RunID, batch.RunID)
	}
	if manifest.RunnerType != "mini-swe-agent-go" {
		return fmt.Errorf("mini-go runner_type %q is not %q", manifest.RunnerType, "mini-swe-agent-go")
	}
	if _, err := normalizeShardRunnerIdentity(miniGoShardRunnerIdentity(manifest)); err != nil {
		return fmt.Errorf("mini-go runner identity is invalid: %w", err)
	}
	if !sameArtifactPath(manifest.OutputDir, rawDir) {
		return fmt.Errorf("mini-go output_dir %q does not match %q", manifest.OutputDir, rawDir)
	}
	expectedPredictions := filepath.Join(rawDir, "preds.json")
	if !sameArtifactPath(manifest.Predictions, expectedPredictions) {
		return fmt.Errorf("mini-go predictions %q do not match %q", manifest.Predictions, expectedPredictions)
	}
	if manifest.CaseCount != len(batch.InstanceIDs) {
		return fmt.Errorf("mini-go case_count %d does not match %d planned instances", manifest.CaseCount, len(batch.InstanceIDs))
	}
	expectedSelectedSHA256, err := selectedInstancesSHA256(batch.InstanceIDs)
	if err != nil {
		return fmt.Errorf("mini-go selected instances are invalid: %w", err)
	}
	if manifest.SelectedInstancesSHA256 != expectedSelectedSHA256 {
		return fmt.Errorf(
			"mini-go selected_instances_sha256 %q does not match planned instances %q",
			manifest.SelectedInstancesSHA256,
			expectedSelectedSHA256,
		)
	}
	if manifest.Workers < 1 {
		return fmt.Errorf("mini-go workers %d is not positive", manifest.Workers)
	}
	if manifest.StartedAt.IsZero() || manifest.FinishedAt.IsZero() {
		return fmt.Errorf("mini-go start and finish times must be present")
	}
	if manifest.FinishedAt.Before(manifest.StartedAt) {
		return fmt.Errorf("mini-go finished_at precedes started_at")
	}
	expectedDuration := manifest.FinishedAt.Sub(manifest.StartedAt).Milliseconds()
	if manifest.DurationMS != expectedDuration {
		return fmt.Errorf("mini-go duration_ms %d does not match elapsed time %d", manifest.DurationMS, expectedDuration)
	}
	if manifest.Status != "completed" && manifest.Status != "completed_with_errors" {
		return fmt.Errorf("mini-go status %q is not terminal", manifest.Status)
	}
	return nil
}

func miniGoShardRunnerIdentity(manifest runnerManifest) shardRunnerIdentity {
	return shardRunnerIdentity{
		ManifestKind:            "mini-go",
		RunnerType:              manifest.RunnerType,
		ObservationCodec:        manifest.ObservationCodec,
		FrameworkModule:         manifest.FrameworkModule,
		FrameworkVersion:        manifest.FrameworkVersion,
		SourceRevision:          manifest.SourceRevision,
		SourceModified:          manifest.SourceModified,
		BinarySHA256:            manifest.BinarySHA256,
		CasesSHA256:             manifest.CasesSHA256,
		ModelConfigSHA256:       manifest.ModelConfigSHA256,
		EnvironmentConfigSHA256: manifest.EnvironmentConfigSHA256,
		CommandTimeout:          manifest.CommandTimeout,
		CaseTimeout:             manifest.CaseTimeout,
	}
}

func validateShardRunnerIdentity(identity shardRunnerIdentity) error {
	_, err := normalizeShardRunnerIdentity(identity)
	return err
}

func normalizeShardRunnerIdentity(identity shardRunnerIdentity) (shardRunnerIdentity, error) {
	switch identity.ManifestKind {
	case "legacy-run-mini":
		if identity.RunnerType != "mini-swe-agent-python" {
			return shardRunnerIdentity{}, fmt.Errorf("legacy runner_type %q is not %q", identity.RunnerType, "mini-swe-agent-python")
		}
		return identity, nil
	case "mini-go":
	default:
		return shardRunnerIdentity{}, fmt.Errorf("unsupported manifest_kind %q", identity.ManifestKind)
	}
	if identity.RunnerType != "mini-swe-agent-go" {
		return shardRunnerIdentity{}, fmt.Errorf("runner_type %q is not %q", identity.RunnerType, "mini-swe-agent-go")
	}
	if identity.ObservationCodec != "xml" && identity.ObservationCodec != "json" && identity.ObservationCodec != "text" {
		return shardRunnerIdentity{}, fmt.Errorf("observation_codec %q is not xml, json, or text", identity.ObservationCodec)
	}
	if identity.FrameworkModule != "trpc.group/trpc-go/trpc-agent-go" {
		return shardRunnerIdentity{}, fmt.Errorf("framework_module %q is not the tRPC-Agent-Go module", identity.FrameworkModule)
	}
	if !isFrameworkVersion(identity.FrameworkVersion) {
		return shardRunnerIdentity{}, fmt.Errorf("framework_version %q is empty or invalid", identity.FrameworkVersion)
	}
	if !isHexIdentifier(identity.SourceRevision, 40, 64) {
		return shardRunnerIdentity{}, fmt.Errorf("source_revision %q is not a full Git revision", identity.SourceRevision)
	}
	for _, field := range []struct {
		name  string
		value string
	}{
		{"binary_sha256", identity.BinarySHA256},
		{"cases_sha256", identity.CasesSHA256},
		{"model_config_sha256", identity.ModelConfigSHA256},
		{"environment_config_sha256", identity.EnvironmentConfigSHA256},
	} {
		if !isHexIdentifier(field.value, 64) {
			return shardRunnerIdentity{}, fmt.Errorf("%s %q is not a SHA-256 digest", field.name, field.value)
		}
	}
	commandTimeout, err := positiveDuration("command_timeout", identity.CommandTimeout)
	if err != nil {
		return shardRunnerIdentity{}, err
	}
	caseTimeout, err := positiveDuration("case_timeout", identity.CaseTimeout)
	if err != nil {
		return shardRunnerIdentity{}, err
	}
	identity.CommandTimeout = commandTimeout.String()
	identity.CaseTimeout = caseTimeout.String()
	return identity, nil
}

func positiveDuration(name, value string) (time.Duration, error) {
	duration, err := time.ParseDuration(value)
	if err != nil || duration <= 0 {
		return 0, fmt.Errorf("%s %q is not a positive duration", name, value)
	}
	return duration, nil
}

func selectedInstancesSHA256(instanceIDs []string) (string, error) {
	if len(instanceIDs) == 0 {
		return "", fmt.Errorf("selected instance list is empty")
	}
	ids := append([]string(nil), instanceIDs...)
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if err := validateArtifactName("selected instance id", id); err != nil {
			return "", err
		}
		if _, exists := seen[id]; exists {
			return "", fmt.Errorf("duplicate selected instance id %q", id)
		}
		seen[id] = struct{}{}
	}
	sort.Strings(ids)
	hash := sha256.New()
	for _, id := range ids {
		_, _ = hash.Write([]byte(id))
		_, _ = hash.Write([]byte{'\n'})
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

func isFrameworkVersion(value string) bool {
	return len(value) >= 2 && value[0] == 'v' && value[1] >= '0' && value[1] <= '9' &&
		strings.TrimSpace(value) == value && !strings.ContainsAny(value, " \t\r\n")
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
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
			return false
		}
	}
	return true
}

func shardRunnerIdentityMismatch(canonical, candidate shardRunnerIdentity) string {
	fields := []struct {
		name      string
		canonical string
		candidate string
	}{
		{"manifest_kind", canonical.ManifestKind, candidate.ManifestKind},
		{"runner_type", canonical.RunnerType, candidate.RunnerType},
		{"observation_codec", canonical.ObservationCodec, candidate.ObservationCodec},
		{"framework_module", canonical.FrameworkModule, candidate.FrameworkModule},
		{"framework_version", canonical.FrameworkVersion, candidate.FrameworkVersion},
		{"source_revision", canonical.SourceRevision, candidate.SourceRevision},
		{"binary_sha256", canonical.BinarySHA256, candidate.BinarySHA256},
		{"cases_sha256", canonical.CasesSHA256, candidate.CasesSHA256},
		{"model_config_sha256", canonical.ModelConfigSHA256, candidate.ModelConfigSHA256},
		{"environment_config_sha256", canonical.EnvironmentConfigSHA256, candidate.EnvironmentConfigSHA256},
		{"command_timeout", canonical.CommandTimeout, candidate.CommandTimeout},
		{"case_timeout", canonical.CaseTimeout, candidate.CaseTimeout},
	}
	for _, field := range fields {
		if field.canonical != field.candidate {
			return fmt.Sprintf("%s %q does not match canonical %q", field.name, field.candidate, field.canonical)
		}
	}
	if canonical.SourceModified != candidate.SourceModified {
		return fmt.Sprintf("source_modified %t does not match canonical %t", candidate.SourceModified, canonical.SourceModified)
	}
	return ""
}

func artifactPathExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func summarizeShardCase(rawDir, instanceID string, preds map[string]contract.Prediction) shardCaseSummary {
	tracePath := filepath.Join(rawDir, instanceID, instanceID+".traj.json")
	relTrace := relPath(rawDir, tracePath)
	data, err := os.ReadFile(tracePath)
	if err != nil {
		return shardCaseSummary{InstanceID: instanceID, Status: "missing", Reason: "missing trajectory", TracePath: relTrace}
	}
	exitStatus := extractExitStatus(data)
	if strings.TrimSpace(exitStatus) == "" {
		return shardCaseSummary{InstanceID: instanceID, Status: "invalid", Reason: "missing exit status", TracePath: relTrace}
	}
	pred, ok := preds[instanceID]
	if !ok {
		return shardCaseSummary{
			InstanceID: instanceID,
			Status:     "invalid",
			ExitStatus: exitStatus,
			Reason:     "missing prediction",
			TracePath:  relTrace,
		}
	}
	patch := pred.ModelPatch
	return shardCaseSummary{
		InstanceID:    instanceID,
		Status:        "accepted",
		ExitStatus:    exitStatus,
		HasPrediction: true,
		EmptyPatch:    strings.TrimSpace(patch) == "",
		PatchChars:    len(patch),
		TracePath:     relTrace,
	}
}

func extractExitStatus(data []byte) string {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return ""
	}
	if info, ok := raw["info"].(map[string]any); ok {
		if status, ok := info["exit_status"].(string); ok && strings.TrimSpace(status) != "" {
			return status
		}
	}
	if messages, ok := raw["messages"].([]any); ok {
		for i := len(messages) - 1; i >= 0; i-- {
			msg, ok := messages[i].(map[string]any)
			if !ok {
				continue
			}
			if extra, ok := msg["extra"].(map[string]any); ok {
				if status, ok := extra["exit_status"].(string); ok && strings.TrimSpace(status) != "" {
					return status
				}
			}
			if role, _ := msg["role"].(string); role == "exit" {
				if content, ok := msg["content"].(string); ok && strings.TrimSpace(content) != "" {
					return content
				}
			}
		}
	}
	return ""
}

func runMergePredictions(args []string) error {
	fs := newFlagSet("merge-predictions")
	shardsPath := fs.String("shards", "", "shards manifest JSON path")
	casesPath := fs.String("cases", "", "optional canonical cases.jsonl path for output order and completeness check")
	output := fs.String("output", "", "merged preds.json output path")
	allowMissing := fs.Bool("allow-missing", false, "allow missing canonical cases")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := required(fs, "shards", *shardsPath); err != nil {
		return err
	}
	if err := required(fs, "output", *output); err != nil {
		return err
	}
	var manifest shardsManifest
	if err := readJSONFile(*shardsPath, &manifest); err != nil {
		return fmt.Errorf("read shards manifest: %w", err)
	}
	preds, err := acceptedPredictions(manifest)
	if err != nil {
		return err
	}
	ordered, err := orderPredictions(preds, *casesPath, *allowMissing)
	if err != nil {
		return err
	}
	out := map[string]contract.Prediction{}
	for _, pred := range ordered {
		out[pred.InstanceID] = pred
	}
	if err := writeJSON(*output, out); err != nil {
		return err
	}
	fmt.Printf("merged=%d\npredictions=%s\n", len(out), *output)
	return nil
}

func acceptedPredictions(manifest shardsManifest) (map[string]contract.Prediction, error) {
	out := map[string]contract.Prediction{}
	for _, shard := range manifest.Shards {
		if shard.Status == "superseded" {
			continue
		}
		if shard.Status != "accepted" && shard.Status != "partial" {
			return nil, fmt.Errorf(
				"shard %s is not mergeable: status=%q reason=%q",
				shard.RunID,
				shard.Status,
				shard.FailureReason,
			)
		}
		if strings.TrimSpace(shard.FailureReason) != "" {
			return nil, fmt.Errorf("shard %s is not mergeable: %s", shard.RunID, shard.FailureReason)
		}
		preds, predictionsSHA256, err := readShardPredictionsSnapshot(filepath.Join(shard.RawDir, "preds.json"))
		if err != nil {
			if shard.AcceptedCount == 0 {
				continue
			}
			return nil, fmt.Errorf("read predictions for shard %s: %w", shard.RunID, err)
		}
		if strings.TrimSpace(shard.PredictionsSHA256) == "" {
			return nil, fmt.Errorf("shard %s has no recorded preds.json SHA-256", shard.RunID)
		}
		if predictionsSHA256 != shard.PredictionsSHA256 {
			return nil, fmt.Errorf(
				"shard %s preds.json SHA-256 %s does not match summarized %s",
				shard.RunID,
				predictionsSHA256,
				shard.PredictionsSHA256,
			)
		}
		for _, c := range shard.Cases {
			if c.Status != "accepted" {
				continue
			}
			pred, ok := preds[c.InstanceID]
			if !ok {
				return nil, fmt.Errorf("accepted case %s is missing from shard %s preds.json", c.InstanceID, shard.RunID)
			}
			if _, exists := out[c.InstanceID]; exists {
				return nil, fmt.Errorf("duplicate accepted prediction for %s", c.InstanceID)
			}
			out[c.InstanceID] = pred
		}
	}
	return out, nil
}

func readShardPredictionsSnapshot(path string) (map[string]contract.Prediction, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	digest := sha256.Sum256(data)
	predictions, err := parseShardPredictions(data, path)
	if err != nil {
		return nil, "", err
	}
	return predictions, fmt.Sprintf("%x", digest), nil
}

func parseShardPredictions(data []byte, source string) (map[string]contract.Prediction, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("predictions file %s is empty", source)
	}
	if json.Valid(data) && len(trimmed) > 0 && trimmed[0] == '{' {
		if err := rejectShardDuplicatePredictionKeys(data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", source, err)
		}
	}
	var byID map[string]contract.Prediction
	if err := json.Unmarshal(data, &byID); err == nil && byID != nil {
		if len(byID) == 0 {
			return nil, fmt.Errorf("predictions file %s contains no predictions", source)
		}
		for id, prediction := range byID {
			if err := validateShardPredictionID(id); err != nil {
				return nil, fmt.Errorf("prediction map key: %w", err)
			}
			if prediction.InstanceID == "" {
				prediction.InstanceID = id
			} else if prediction.InstanceID != id {
				return nil, fmt.Errorf("prediction map key %q does not match instance_id %q", id, prediction.InstanceID)
			}
			byID[id] = prediction
		}
		return byID, nil
	}
	var rows []contract.Prediction
	if err := json.Unmarshal(data, &rows); err == nil {
		return shardPredictionsByID(rows, source)
	}
	rows = nil
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64*1024), 64*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var row contract.Prediction
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", source, lineNumber, err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", source, err)
	}
	return shardPredictionsByID(rows, source)
}

func rejectShardDuplicatePredictionKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("non-string object key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate top-level key %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func shardPredictionsByID(rows []contract.Prediction, source string) (map[string]contract.Prediction, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("predictions file %s contains no predictions", source)
	}
	out := make(map[string]contract.Prediction, len(rows))
	for index, row := range rows {
		if err := validateShardPredictionID(row.InstanceID); err != nil {
			return nil, fmt.Errorf("prediction at index %d: %w", index, err)
		}
		if _, exists := out[row.InstanceID]; exists {
			return nil, fmt.Errorf("duplicate prediction instance_id %q in %s", row.InstanceID, source)
		}
		out[row.InstanceID] = row
	}
	return out, nil
}

func validateShardPredictionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("empty instance_id")
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("surrounding whitespace in instance_id %q", id)
	}
	return nil
}

func orderPredictions(preds map[string]contract.Prediction, casesPath string, allowMissing bool) ([]contract.Prediction, error) {
	if strings.TrimSpace(casesPath) == "" {
		ids := make([]string, 0, len(preds))
		for id := range preds {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		out := make([]contract.Prediction, 0, len(ids))
		for _, id := range ids {
			out = append(out, preds[id])
		}
		return out, nil
	}
	cases, err := readCases(casesPath, nil)
	if err != nil {
		return nil, err
	}
	out := make([]contract.Prediction, 0, len(cases))
	missing := []string{}
	for _, c := range cases {
		pred, ok := preds[c.InstanceID]
		if !ok {
			missing = append(missing, c.InstanceID)
			continue
		}
		out = append(out, pred)
	}
	if len(missing) > 0 && !allowMissing {
		return nil, fmt.Errorf("missing %d canonical predictions: %s", len(missing), strings.Join(firstStrings(missing, 10), ", "))
	}
	return out, nil
}

func firstStrings(values []string, n int) []string {
	if len(values) <= n {
		return values
	}
	out := append([]string(nil), values[:n]...)
	out = append(out, "...")
	return out
}
