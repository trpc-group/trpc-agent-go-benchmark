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
	"strconv"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

type runConfigDocument struct {
	RunID           string                   `json:"run_id"`
	Target          string                   `json:"target"`
	GeneratedAt     time.Time                `json:"generated_at"`
	Dataset         runConfigDataset         `json:"dataset"`
	Selection       runConfigSelection       `json:"selection"`
	Model           runConfigModel           `json:"model"`
	Runner          runConfigRunner          `json:"runner"`
	Concurrency     runConfigConcurrency     `json:"concurrency"`
	Verifier        runConfigVerifier        `json:"verifier"`
	Artifacts       runConfigArtifacts       `json:"artifacts"`
	ResultSummary   importSummary            `json:"result_summary"`
	ServiceFindings runConfigServiceFindings `json:"service_findings,omitempty"`
	Notes           []string                 `json:"notes,omitempty"`
	SourceFiles     runConfigSourceFiles     `json:"source_files"`
	Checks          map[string]doctorCheck   `json:"checks,omitempty"`
	ModelConfig     map[string]string        `json:"model_config,omitempty"`
	Commands        map[string]commandResult `json:"commands,omitempty"`
}

type runConfigDataset struct {
	Name             string   `json:"name"`
	Split            string   `json:"split"`
	Revision         string   `json:"revision,omitempty"`
	CaseCount        int      `json:"case_count"`
	CaseListHash     string   `json:"case_list_hash"`
	CasesJSONLSHA256 string   `json:"cases_jsonl_sha256"`
	HintsTextPolicy  string   `json:"hints_text_policy"`
	SourceFields     []string `json:"source_fields,omitempty"`
	ExcludedFields   []string `json:"excluded_fields,omitempty"`
}

type runConfigSelection struct {
	CaseCount    int    `json:"case_count"`
	CaseListHash string `json:"case_list_hash"`
}

type runConfigModel struct {
	Strategy        string            `json:"strategy"`
	Name            string            `json:"name"`
	MiniModelName   string            `json:"mini_model_name,omitempty"`
	EndpointID      string            `json:"endpoint_id,omitempty"`
	Parameters      map[string]string `json:"parameters,omitempty"`
	ConfigReference string            `json:"config_reference,omitempty"`
}

type runConfigRunner struct {
	Type                string `json:"type"`
	MiniSWEAgentVersion string `json:"mini_swe_agent_version,omitempty"`
	MiniExtra           string `json:"mini_extra,omitempty"`
	BaseConfig          string `json:"base_config,omitempty"`
	PrivateConfig       string `json:"private_config,omitempty"`
	StartedAt           string `json:"started_at,omitempty"`
	FinishedAt          string `json:"finished_at,omitempty"`
	DurationMS          int64  `json:"duration_ms,omitempty"`
}

type runConfigConcurrency struct {
	AgentGenerationWorkers int `json:"agent_generation_workers"`
	HarnessWorkers         int `json:"harness_workers"`
}

type runConfigVerifier struct {
	Type             string `json:"type"`
	HarnessRunID     string `json:"harness_run_id,omitempty"`
	Python           string `json:"python,omitempty"`
	SWEBenchVersion  string `json:"swebench_version,omitempty"`
	SWEBenchRevision string `json:"swebench_revision,omitempty"`
	PackagePath      string `json:"package_path,omitempty"`
	DockerHost       string `json:"docker_host,omitempty"`
	TimeoutSec       int    `json:"timeout_seconds"`
	CacheLevel       string `json:"cache_level,omitempty"`
	Clean            bool   `json:"clean"`
	StartedAt        string `json:"started_at,omitempty"`
	FinishedAt       string `json:"finished_at,omitempty"`
	DurationMS       int64  `json:"duration_ms,omitempty"`
}

type runConfigArtifacts struct {
	CasesManifest       string `json:"cases_manifest"`
	CasesJSONL          string `json:"cases_jsonl,omitempty"`
	RunnerOutputDir     string `json:"runner_output_dir,omitempty"`
	RunnerLog           string `json:"runner_log,omitempty"`
	MiniRawDir          string `json:"mini_raw_dir,omitempty"`
	MiniLog             string `json:"mini_log,omitempty"`
	Predictions         string `json:"predictions,omitempty"`
	PredictionsSHA256   string `json:"predictions_sha256,omitempty"`
	VerifierReportDir   string `json:"verifier_report_dir,omitempty"`
	HarnessReport       string `json:"harness_report,omitempty"`
	HarnessReportSHA256 string `json:"harness_report_sha256,omitempty"`
	ImportedDir         string `json:"imported_dir,omitempty"`
	ImportedCases       string `json:"imported_cases,omitempty"`
	ImportSummary       string `json:"import_summary"`
}

type runConfigServiceFindings struct {
	RateLimitErrors          int `json:"rate_limit_errors,omitempty"`
	ServiceUnavailableErrors int `json:"service_unavailable_errors,omitempty"`
	WorkerUnavailableErrors  int `json:"worker_unavailable_errors,omitempty"`
}

type runConfigSourceFiles struct {
	DoctorManifest   string `json:"doctor_manifest,omitempty"`
	CasesManifest    string `json:"cases_manifest"`
	RunMiniManifest  string `json:"run_mini_manifest,omitempty"`
	RunnerManifest   string `json:"runner_manifest,omitempty"`
	ShardsManifest   string `json:"shards_manifest,omitempty"`
	VerifierManifest string `json:"verifier_manifest"`
	ImportSummary    string `json:"import_summary"`
}

type runnerManifest struct {
	RunID                   string            `json:"run_id"`
	RunnerType              string            `json:"runner_type"`
	ObservationCodec        string            `json:"observation_codec,omitempty"`
	FrameworkModule         string            `json:"framework_module,omitempty"`
	FrameworkVersion        string            `json:"framework_version,omitempty"`
	SourceRevision          string            `json:"source_revision,omitempty"`
	SourceModified          bool              `json:"source_modified"`
	BinarySHA256            string            `json:"binary_sha256,omitempty"`
	CasesSHA256             string            `json:"cases_sha256,omitempty"`
	ModelConfigSHA256       string            `json:"model_config_sha256,omitempty"`
	EnvironmentConfigSHA256 string            `json:"environment_config_sha256,omitempty"`
	SelectedInstancesSHA256 string            `json:"selected_instances_sha256,omitempty"`
	CommandTimeout          string            `json:"command_timeout,omitempty"`
	CaseTimeout             string            `json:"case_timeout,omitempty"`
	StartedAt               time.Time         `json:"started_at"`
	FinishedAt              time.Time         `json:"finished_at"`
	DurationMS              int64             `json:"duration_ms"`
	OutputDir               string            `json:"output_dir"`
	CaseCount               int               `json:"case_count"`
	Workers                 int               `json:"workers,omitempty"`
	Predictions             string            `json:"predictions"`
	ModelConfig             map[string]string `json:"model_config,omitempty"`
	Status                  string            `json:"status,omitempty"`
}

func runRunConfig(args []string) error {
	fs := newFlagSet("run-config")
	runID := fs.String("run-id", "", "run id")
	target := fs.String("target", "baseline", "path-safe target label")
	output := fs.String("output", "", "output run_config.json path; defaults to results/runs/<run-id>/run_config.json")
	casesManifestPath := fs.String("cases-manifest", "", "cases.manifest.json path")
	runMiniManifestPath := fs.String("run-mini-manifest", "", "run-mini-manifest.json path; deprecated alias for mini baseline")
	runnerManifestPath := fs.String("runner-manifest", "", "generic runner manifest path")
	shardsManifestPath := fs.String("shards-manifest", "", "summarize-shards output path for sharded mini baseline")
	verifierManifestPath := fs.String("verifier-manifest", "", "verifier_manifest.json path")
	importSummaryPath := fs.String("import-summary", "", "normalized import summary JSON path")
	harnessReportPath := fs.String("harness-report", "", "optional official harness report JSON path")
	doctorPath := fs.String("doctor", "", "optional doctor.json path")
	modelName := fs.String("model-name", "", "model name used for this run")
	miniModelName := fs.String("mini-model-name", "", "mini-SWE-agent model_name value")
	endpointID := fs.String("endpoint-id", "", "non-secret model endpoint identifier")
	temperature := fs.String("temperature", "", "model temperature")
	reasoningEffort := fs.String("reasoning-effort", "", "model reasoning effort")
	timeout := fs.String("timeout", "", "model timeout")
	notes := fs.String("notes", "", "semicolon-separated run notes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"run-id":            *runID,
		"cases-manifest":    *casesManifestPath,
		"verifier-manifest": *verifierManifestPath,
		"import-summary":    *importSummaryPath,
		"model-name":        *modelName,
	} {
		if err := required(fs, name, value); err != nil {
			return err
		}
	}
	if err := validateArtifactName("run id", *runID); err != nil {
		return err
	}
	if err := validateTargetLabel(*target); err != nil {
		return err
	}
	runnerSources := 0
	for _, path := range []string{*runMiniManifestPath, *runnerManifestPath, *shardsManifestPath} {
		if strings.TrimSpace(path) != "" {
			runnerSources++
		}
	}
	if runnerSources != 1 {
		return fmt.Errorf("provide exactly one of -runner-manifest, -run-mini-manifest, or -shards-manifest")
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID, "run_config.json")
	}

	var casesManifest prepareDataManifest
	if err := readJSONFile(*casesManifestPath, &casesManifest); err != nil {
		return fmt.Errorf("read cases manifest: %w", err)
	}
	if err := validateCasesContent(casesManifest); err != nil {
		return err
	}
	var miniManifest runMiniManifest
	hasShardsManifest := strings.TrimSpace(*shardsManifestPath) != ""
	hasMiniManifest := strings.TrimSpace(*runMiniManifestPath) != ""
	if hasMiniManifest {
		if err := readJSONFile(*runMiniManifestPath, &miniManifest); err != nil {
			return fmt.Errorf("read run-mini manifest: %w", err)
		}
	}
	var genericManifest runnerManifest
	if !hasMiniManifest && !hasShardsManifest {
		if err := readJSONFile(*runnerManifestPath, &genericManifest); err != nil {
			return fmt.Errorf("read runner manifest: %w", err)
		}
	}
	var shardManifest shardsManifest
	if hasShardsManifest {
		if err := readJSONFile(*shardsManifestPath, &shardManifest); err != nil {
			return fmt.Errorf("read shards manifest: %w", err)
		}
	}
	var verifierManifest verifyManifest
	if err := readJSONFile(*verifierManifestPath, &verifierManifest); err != nil {
		return fmt.Errorf("read verifier manifest: %w", err)
	}
	var summary importSummary
	if err := readJSONFile(*importSummaryPath, &summary); err != nil {
		return fmt.Errorf("read import summary: %w", err)
	}
	if err := validateRunConfigInputs(
		*runID,
		*target,
		casesManifest,
		miniManifest,
		genericManifest,
		shardManifest,
		verifierManifest,
		summary,
		hasMiniManifest,
		hasShardsManifest,
	); err != nil {
		return err
	}
	selection, err := validateRunConfigSelection(
		casesManifest,
		genericManifest,
		shardManifest,
		verifierManifest,
		*importSummaryPath,
		summary,
		*target,
		*harnessReportPath,
		hasMiniManifest,
		hasShardsManifest,
	)
	if err != nil {
		return err
	}
	predictionsSHA256, err := validatePredictionsBinding(
		verifierManifest,
		genericManifest.RunnerType == "trpc-agent-go-native",
	)
	if err != nil {
		return err
	}
	harnessReportSHA256, err := validateHarnessReportBinding(
		verifierManifest,
		*harnessReportPath,
		genericManifest.RunnerType == "trpc-agent-go-native",
	)
	if err != nil {
		return err
	}
	harnessRunID := verifierManifest.Report.HarnessRunID
	if strings.TrimSpace(*harnessReportPath) != "" && strings.TrimSpace(harnessRunID) == "" {
		harnessRunID = verifierManifest.RunID + "-" + verifierManifest.Target
	}
	if err := validateGenericRunnerModelName(genericManifest, *modelName); err != nil {
		return err
	}

	var doctor doctorReport
	hasDoctor := false
	if strings.TrimSpace(*doctorPath) != "" {
		if err := readJSONFile(*doctorPath, &doctor); err != nil {
			return fmt.Errorf("read doctor manifest: %w", err)
		}
		hasDoctor = true
	}

	parameters := map[string]string{}
	for k, v := range map[string]string{
		"temperature":      *temperature,
		"reasoning_effort": *reasoningEffort,
		"timeout":          *timeout,
	} {
		if strings.TrimSpace(v) != "" {
			parameters[k] = v
		}
	}
	if len(parameters) == 0 {
		parameters = nil
	}

	runnerType := "mini-swe-agent"
	runnerStartedAt := formatTime(miniManifest.StartedAt)
	runnerFinishedAt := formatTime(miniManifest.FinishedAt)
	runnerDurationMS := miniManifest.DurationMS
	agentWorkers := miniManifest.Config.Workers
	configReference := miniManifest.Config.MiniConfig
	predictionsPath := filepath.Join(miniManifest.Config.OutputDir, "preds.json")
	outputDir := miniManifest.Config.OutputDir
	runnerLog := miniManifest.Command.LogPath
	miniExtra := miniManifest.Config.MiniExtra
	baseConfig := miniManifest.Config.BaseConfig
	privateConfig := miniManifest.Config.MiniConfig
	serviceFindings := scanServiceFindings(runnerLog)
	miniRawDir := outputDir
	miniLog := runnerLog
	if !hasMiniManifest {
		runnerType = defaultIfEmpty(genericManifest.RunnerType, "unknown")
		runnerStartedAt = formatTime(genericManifest.StartedAt)
		runnerFinishedAt = formatTime(genericManifest.FinishedAt)
		runnerDurationMS = genericManifest.DurationMS
		agentWorkers = genericManifest.Workers
		predictionsPath = genericManifest.Predictions
		outputDir = genericManifest.OutputDir
		serviceFindings = runConfigServiceFindings{}
		miniRawDir = ""
		miniLog = ""
	}
	if hasShardsManifest {
		runnerType = "mini-swe-agent-sharded"
		runnerStartedAt = shardManifest.StartedAt
		runnerFinishedAt = shardManifest.FinishedAt
		runnerDurationMS = shardManifest.WallDurationMS
		agentWorkers = maxShardWorkers(shardManifest)
		configReference = ""
		predictionsPath = verifierManifest.Config.Predictions
		outputDir = filepath.Dir(absPath(*shardsManifestPath))
		runnerLog = ""
		miniExtra = ""
		baseConfig = ""
		privateConfig = ""
		serviceFindings = runConfigServiceFindings{}
		miniRawDir = ""
		miniLog = ""
	}

	doc := runConfigDocument{
		RunID:       *runID,
		Target:      *target,
		GeneratedAt: time.Now().UTC(),
		Dataset: runConfigDataset{
			Name:             casesManifest.Dataset,
			Split:            casesManifest.Split,
			Revision:         casesManifest.Revision,
			CaseCount:        casesManifest.CaseCount,
			CaseListHash:     casesManifest.CaseListHash,
			CasesJSONLSHA256: casesManifest.CasesJSONLSHA256,
			HintsTextPolicy:  casesManifest.HintsTextPolicy,
			SourceFields:     casesManifest.SourceFields,
			ExcludedFields:   casesManifest.ExcludedFields,
		},
		Selection: selection,
		Model: runConfigModel{
			Strategy:        "single",
			Name:            *modelName,
			MiniModelName:   *miniModelName,
			EndpointID:      *endpointID,
			Parameters:      parameters,
			ConfigReference: configReference,
		},
		Runner: runConfigRunner{
			Type:                runnerType,
			MiniSWEAgentVersion: miniSWEAgentVersion(doctor),
			MiniExtra:           miniExtra,
			BaseConfig:          baseConfig,
			PrivateConfig:       privateConfig,
			StartedAt:           runnerStartedAt,
			FinishedAt:          runnerFinishedAt,
			DurationMS:          runnerDurationMS,
		},
		Concurrency: runConfigConcurrency{
			AgentGenerationWorkers: agentWorkers,
			HarnessWorkers:         verifierManifest.Config.Workers,
		},
		Verifier: runConfigVerifier{
			Type:             "official-upstream-harness",
			HarnessRunID:     harnessRunID,
			Python:           verifierManifest.Config.Python,
			SWEBenchVersion:  defaultIfEmpty(verifierManifest.Harness.Version, sweBenchVersion(doctor)),
			SWEBenchRevision: verifierManifest.Harness.Revision,
			PackagePath:      verifierManifest.Harness.PackagePath,
			DockerHost:       verifierManifest.Config.DockerHost,
			TimeoutSec:       verifierManifest.Config.TimeoutSec,
			CacheLevel:       verifierManifest.Config.CacheLevel,
			Clean:            verifierManifest.Config.Clean,
			StartedAt:        formatTime(verifierManifest.StartedAt),
			FinishedAt:       formatTime(verifierManifest.FinishedAt),
			DurationMS:       verifierManifest.DurationMS,
		},
		Artifacts: runConfigArtifacts{
			CasesManifest:       absPath(*casesManifestPath),
			CasesJSONL:          filepath.Join(casesManifest.OutputDir, "cases.jsonl"),
			RunnerOutputDir:     outputDir,
			RunnerLog:           runnerLog,
			MiniRawDir:          miniRawDir,
			MiniLog:             miniLog,
			Predictions:         predictionsPath,
			PredictionsSHA256:   predictionsSHA256,
			VerifierReportDir:   verifierManifest.Config.OutputDir,
			HarnessReport:       absPath(*harnessReportPath),
			HarnessReportSHA256: harnessReportSHA256,
			ImportedDir:         filepath.Dir(filepath.Dir(absPath(*importSummaryPath))),
			ImportedCases:       filepath.Join(filepath.Dir(filepath.Dir(absPath(*importSummaryPath))), "cases.jsonl"),
			ImportSummary:       absPath(*importSummaryPath),
		},
		ResultSummary:   summary,
		ServiceFindings: serviceFindings,
		Notes:           splitNotes(*notes),
		SourceFiles: runConfigSourceFiles{
			DoctorManifest:   absPath(*doctorPath),
			CasesManifest:    absPath(*casesManifestPath),
			RunMiniManifest:  absPath(*runMiniManifestPath),
			RunnerManifest:   absPath(*runnerManifestPath),
			ShardsManifest:   absPath(*shardsManifestPath),
			VerifierManifest: absPath(*verifierManifestPath),
			ImportSummary:    absPath(*importSummaryPath),
		},
	}
	if strings.TrimSpace(doc.Artifacts.CasesJSONL) == "cases.jsonl" {
		doc.Artifacts.CasesJSONL = ""
	}
	if hasDoctor {
		doc.Checks = doctor.Checks
		doc.Commands = doctor.Commands
		doc.ModelConfig = doctor.ModelConfig
		if doc.Model.EndpointID == "" {
			doc.Model.EndpointID = doctor.ModelConfig["OPENAI_BASE_URL"]
		}
	}
	return writeJSON(*output, doc)
}

func validateRunConfigInputs(
	runID string,
	target string,
	cases prepareDataManifest,
	mini runMiniManifest,
	generic runnerManifest,
	shards shardsManifest,
	verifier verifyManifest,
	summary importSummary,
	hasMini bool,
	hasShards bool,
) error {
	if cases.CaseCount < 1 || strings.TrimSpace(cases.CaseListHash) == "" {
		return fmt.Errorf("cases manifest is incomplete: case_count=%d case_list_hash=%q", cases.CaseCount, cases.CaseListHash)
	}
	selectedCaseCount := cases.CaseCount
	if len(verifier.Config.InstanceIDs) > 0 {
		selectedCaseCount = len(verifier.Config.InstanceIDs)
	}
	if selectedCaseCount < 1 || selectedCaseCount > cases.CaseCount {
		return fmt.Errorf(
			"selected case count %d is outside full cases manifest size %d",
			selectedCaseCount,
			cases.CaseCount,
		)
	}
	if verifier.RunID != runID {
		return fmt.Errorf("verifier run_id %q does not match %q", verifier.RunID, runID)
	}
	if verifier.Target != target {
		return fmt.Errorf("verifier target %q does not match %q", verifier.Target, target)
	}
	if summary.SchemaVersion != importSchemaVersion {
		return fmt.Errorf("unsupported import summary schema_version %d", summary.SchemaVersion)
	}
	if summary.Target != target {
		return fmt.Errorf("import summary target %q does not match %q", summary.Target, target)
	}
	if summary.Total != selectedCaseCount {
		return fmt.Errorf("import summary total %d does not match selected case count %d", summary.Total, selectedCaseCount)
	}
	countTotal := 0
	for status, count := range summary.Counts {
		if count < 0 {
			return fmt.Errorf("import summary count %q is negative", status)
		}
		countTotal += count
	}
	if countTotal != summary.Total {
		return fmt.Errorf("import summary counts total %d does not match total %d", countTotal, summary.Total)
	}
	if verifier.Config.Dataset != cases.Dataset || verifier.Config.Split != cases.Split {
		return fmt.Errorf(
			"verifier dataset %q/%q does not match cases %q/%q",
			verifier.Config.Dataset,
			verifier.Config.Split,
			cases.Dataset,
			cases.Split,
		)
	}
	if verifier.Config.Workers < 1 || verifier.Config.TimeoutSec < 1 {
		return fmt.Errorf(
			"verifier runtime limits are invalid: workers=%d timeout_seconds=%d",
			verifier.Config.Workers,
			verifier.Config.TimeoutSec,
		)
	}
	if strings.TrimSpace(verifier.Harness.Version) == "" {
		return fmt.Errorf("verifier harness version is empty")
	}
	if verifier.Command.ExitCode != 0 {
		return fmt.Errorf("verifier command failed with exit code %d", verifier.Command.ExitCode)
	}

	runnerPredictions := ""
	switch {
	case hasMini:
		if mini.RunID != runID {
			return fmt.Errorf("run-mini run_id %q does not match %q", mini.RunID, runID)
		}
		if mini.Command.ExitCode != 0 {
			return fmt.Errorf("run-mini command failed with exit code %d", mini.Command.ExitCode)
		}
		runnerPredictions = filepath.Join(mini.Config.OutputDir, "preds.json")
	case hasShards:
		canonicalIdentity, err := normalizeShardRunnerIdentity(shards.RunnerIdentity)
		if err != nil {
			return fmt.Errorf("shards runner identity is invalid: %w", err)
		}
		if shards.ExpectedCases != selectedCaseCount {
			return fmt.Errorf(
				"shards expected cases %d does not match selected case count %d",
				shards.ExpectedCases,
				selectedCaseCount,
			)
		}
		if shards.AcceptedCases != shards.ExpectedCases || shards.MissingCases != 0 || shards.InvalidCases != 0 || shards.DuplicateCases != 0 {
			return fmt.Errorf(
				"shards are incomplete: expected=%d accepted=%d missing=%d invalid=%d duplicate=%d",
				shards.ExpectedCases,
				shards.AcceptedCases,
				shards.MissingCases,
				shards.InvalidCases,
				shards.DuplicateCases,
			)
		}
		for _, shard := range shards.Shards {
			if shard.Status != "accepted" || shard.FailureReason != "" {
				return fmt.Errorf("shard %q is not accepted: status=%q reason=%q", shard.RunID, shard.Status, shard.FailureReason)
			}
			shardIdentity, err := normalizeShardRunnerIdentity(shard.RunnerIdentity)
			if err != nil {
				return fmt.Errorf("shard %q runner identity is invalid: %w", shard.RunID, err)
			}
			if mismatch := shardRunnerIdentityMismatch(canonicalIdentity, shardIdentity); mismatch != "" {
				return fmt.Errorf("shard %q runner identity mismatch: %s", shard.RunID, mismatch)
			}
			selectedSHA256, err := selectedInstancesSHA256(shard.ExpectedIDs)
			if err != nil {
				return fmt.Errorf("shard %q selected instances are invalid: %w", shard.RunID, err)
			}
			if shard.SelectedInstancesSHA256 != selectedSHA256 {
				return fmt.Errorf(
					"shard %q selected_instances_sha256 %q does not match expected %q",
					shard.RunID,
					shard.SelectedInstancesSHA256,
					selectedSHA256,
				)
			}
		}
		runnerPredictions = verifier.Config.Predictions
	default:
		if generic.RunID != runID {
			return fmt.Errorf("runner run_id %q does not match %q", generic.RunID, runID)
		}
		if generic.CaseCount != selectedCaseCount {
			return fmt.Errorf(
				"runner case count %d does not match selected case count %d",
				generic.CaseCount,
				selectedCaseCount,
			)
		}
		if generic.Status != "" && generic.Status != "completed" && generic.Status != "completed_with_errors" {
			return fmt.Errorf("runner status %q is not a supported terminal status", generic.Status)
		}
		if generic.RunnerType == "mini-swe-agent-go" || generic.RunnerType == "trpc-agent-go-native" {
			if generic.Workers < 1 {
				return fmt.Errorf("runner workers %d is not positive", generic.Workers)
			}
			identity := miniGoShardRunnerIdentity(generic)
			if generic.RunnerType == "trpc-agent-go-native" {
				identity = nativeRunnerIdentity(generic)
			}
			if _, err := normalizeShardRunnerIdentity(identity); err != nil {
				return fmt.Errorf("runner identity is invalid: %w", err)
			}
			if !isHexIdentifier(generic.SelectedInstancesSHA256, 64) {
				return fmt.Errorf(
					"runner selected_instances_sha256 %q is not a SHA-256 digest",
					generic.SelectedInstancesSHA256,
				)
			}
			if generic.CaseCount == cases.CaseCount && generic.SelectedInstancesSHA256 != cases.CaseListHash {
				return fmt.Errorf(
					"runner selected_instances_sha256 %q does not match case_list_hash %q",
					generic.SelectedInstancesSHA256,
					cases.CaseListHash,
				)
			}
			if !isHexIdentifier(cases.CasesJSONLSHA256, 64) {
				return fmt.Errorf("cases manifest cases_jsonl_sha256 %q is not a SHA-256 digest", cases.CasesJSONLSHA256)
			}
			if generic.CasesSHA256 != cases.CasesJSONLSHA256 {
				return fmt.Errorf(
					"runner cases_sha256 %q does not match cases manifest cases_jsonl_sha256 %q",
					generic.CasesSHA256,
					cases.CasesJSONLSHA256,
				)
			}
		}
		runnerPredictions = generic.Predictions
	}
	if !sameArtifactPath(runnerPredictions, verifier.Config.Predictions) {
		return fmt.Errorf(
			"runner predictions %q do not match verifier predictions %q",
			runnerPredictions,
			verifier.Config.Predictions,
		)
	}
	return nil
}

func validateRunConfigSelection(
	cases prepareDataManifest,
	generic runnerManifest,
	shards shardsManifest,
	verifier verifyManifest,
	importSummaryPath string,
	summary importSummary,
	target string,
	harnessReportPath string,
	hasMini bool,
	hasShards bool,
) (runConfigSelection, error) {
	if _, err := validatePredictionsBinding(
		verifier,
		generic.RunnerType == "trpc-agent-go-native",
	); err != nil {
		return runConfigSelection{}, err
	}
	if _, err := validateHarnessReportBinding(
		verifier,
		harnessReportPath,
		generic.RunnerType == "trpc-agent-go-native",
	); err != nil {
		return runConfigSelection{}, err
	}
	fullCasesPath := filepath.Join(cases.OutputDir, "cases.jsonl")
	fullCases, err := readCases(fullCasesPath, nil)
	if err != nil {
		return runConfigSelection{}, fmt.Errorf("read full cases for selection validation: %w", err)
	}
	fullCasesByID := make(map[string]contract.Case, len(fullCases))
	for _, fullCase := range fullCases {
		fullCasesByID[fullCase.InstanceID] = fullCase
	}
	fullIDs := caseIDs(fullCases)
	fullHash, err := selectedInstancesSHA256(fullIDs)
	if err != nil {
		return runConfigSelection{}, fmt.Errorf("hash full cases selection: %w", err)
	}
	if len(fullIDs) != cases.CaseCount {
		return runConfigSelection{}, fmt.Errorf(
			"full cases count %d does not match cases manifest %d",
			len(fullIDs),
			cases.CaseCount,
		)
	}
	if fullHash != cases.CaseListHash {
		return runConfigSelection{}, fmt.Errorf(
			"full cases hash %q does not match cases manifest case_list_hash %q",
			fullHash,
			cases.CaseListHash,
		)
	}

	predictionsPath := strings.TrimSpace(verifier.Config.Predictions)
	if predictionsPath == "" || predictionsPath == "gold" {
		return runConfigSelection{}, fmt.Errorf("runner predictions path is unavailable for selection validation")
	}
	predictions, err := readPredictions(predictionsPath)
	if err != nil {
		return runConfigSelection{}, fmt.Errorf("read runner predictions for selection validation: %w", err)
	}
	predictionIDs := make([]string, 0, len(predictions))
	for id := range predictions {
		predictionIDs = append(predictionIDs, id)
	}
	selectionHash, err := selectedInstancesSHA256(predictionIDs)
	if err != nil {
		return runConfigSelection{}, fmt.Errorf("hash runner predictions selection: %w", err)
	}
	selection := runConfigSelection{CaseCount: len(predictionIDs), CaseListHash: selectionHash}
	if err := validateSelectionSubset(predictionIDs, fullIDs); err != nil {
		return runConfigSelection{}, err
	}

	switch {
	case hasMini:
	case hasShards:
		var declaredIDs []string
		for _, shard := range shards.Shards {
			declaredIDs = append(declaredIDs, shard.ExpectedIDs...)
		}
		if len(declaredIDs) != shards.ExpectedCases {
			return runConfigSelection{}, fmt.Errorf(
				"shards declare %d selected cases but enumerate %d expected IDs",
				shards.ExpectedCases,
				len(declaredIDs),
			)
		}
		if err := validateSelectionIdentity("shards expected IDs", declaredIDs, selection); err != nil {
			return runConfigSelection{}, err
		}
	default:
		if generic.CaseCount != selection.CaseCount {
			return runConfigSelection{}, fmt.Errorf(
				"runner case count %d does not match predictions count %d",
				generic.CaseCount,
				selection.CaseCount,
			)
		}
		if generic.SelectedInstancesSHA256 != selection.CaseListHash {
			return runConfigSelection{}, fmt.Errorf(
				"runner selected_instances_sha256 %q does not match predictions selection hash %q",
				generic.SelectedInstancesSHA256,
				selection.CaseListHash,
			)
		}
	}

	importedCasesPath := filepath.Join(filepath.Dir(filepath.Dir(absPath(importSummaryPath))), "cases.jsonl")
	importedCases, err := readAndValidateImportedCases(importedCasesPath, summary, target)
	if err != nil {
		return runConfigSelection{}, fmt.Errorf("read imported cases for selection validation: %w", err)
	}
	importedIDs := make([]string, 0, len(importedCases))
	for _, imported := range importedCases {
		importedIDs = append(importedIDs, imported.InstanceID)
	}
	if err := validateSelectionIdentity("imported cases", importedIDs, selection); err != nil {
		return runConfigSelection{}, err
	}
	importedRoot := filepath.Dir(filepath.Dir(absPath(importSummaryPath)))
	if err := validateImportedCaseBindings(
		importedCases,
		predictions,
		fullCasesByID,
		selection.CaseCount == cases.CaseCount && selection.CaseListHash == cases.CaseListHash,
		importedRoot,
		target,
		harnessReportPath,
	); err != nil {
		return runConfigSelection{}, err
	}
	if generic.RunnerType == "trpc-agent-go-native" {
		if err := validateNativePredictionModelNames(predictions, generic); err != nil {
			return runConfigSelection{}, err
		}
		if err := validateNativeImportedBundles(
			importedCases,
			predictions,
			importedRoot,
			target,
			generic,
			selection,
		); err != nil {
			return runConfigSelection{}, err
		}
	}

	verifierIDs := verifier.Config.InstanceIDs
	if len(verifierIDs) == 0 {
		if selection.CaseCount != cases.CaseCount || selection.CaseListHash != cases.CaseListHash {
			return runConfigSelection{}, fmt.Errorf("verifier manifest has no instance_ids for filtered selection")
		}
		verifierIDs = fullIDs
	}
	if err := validateSelectionIdentity("verifier instance_ids", verifierIDs, selection); err != nil {
		return runConfigSelection{}, err
	}
	return selection, nil
}

func validatePredictionsBinding(verifier verifyManifest, requireAttested bool) (string, error) {
	sourcePath := strings.TrimSpace(verifier.Config.Predictions)
	snapshotPath := strings.TrimSpace(verifier.Config.PredictionsSnapshot)
	expectedSHA256 := strings.TrimSpace(verifier.Config.PredictionsSHA256)
	hasAttestation := snapshotPath != "" || expectedSHA256 != ""
	if strings.TrimSpace(verifier.PredictionsError) != "" {
		return "", fmt.Errorf("verifier predictions attestation failed: %s", verifier.PredictionsError)
	}
	if sourcePath == "gold" {
		if requireAttested {
			return "", fmt.Errorf("native finalization requires file-backed predictions attestation, not gold")
		}
		if hasAttestation {
			return "", fmt.Errorf("gold predictions must not carry a file snapshot attestation")
		}
		return "", nil
	}
	if !hasAttestation {
		if requireAttested {
			return "", fmt.Errorf("native predictions have no verify-time snapshot attestation")
		}
		return "", nil
	}
	if sourcePath == "" || snapshotPath == "" || expectedSHA256 == "" {
		return "", fmt.Errorf("verifier predictions snapshot attestation is incomplete")
	}
	if !isSHA256Hex(expectedSHA256) {
		return "", fmt.Errorf("verifier predictions sha256 %q is not a SHA-256 digest", expectedSHA256)
	}
	outputDir := filepath.Clean(absPath(verifier.Config.OutputDir))
	snapshotAbs := filepath.Clean(absPath(snapshotPath))
	if filepath.Dir(snapshotAbs) != outputDir {
		return "", fmt.Errorf(
			"verifier predictions snapshot %q is not directly inside output_dir %q",
			snapshotAbs,
			outputDir,
		)
	}
	if !strings.HasPrefix(filepath.Base(snapshotAbs), "predictions.snapshot.") {
		return "", fmt.Errorf("verifier predictions snapshot %q has an unexpected filename", snapshotAbs)
	}
	sourceData, err := readRegularArtifact(sourcePath)
	if err != nil {
		return "", fmt.Errorf("read runner predictions for verifier binding: %w", err)
	}
	snapshotData, err := readRegularArtifact(snapshotAbs)
	if err != nil {
		return "", fmt.Errorf("read verifier predictions snapshot: %w", err)
	}
	sourceSHA256 := fmt.Sprintf("%x", sha256.Sum256(sourceData))
	snapshotSHA256 := fmt.Sprintf("%x", sha256.Sum256(snapshotData))
	if sourceSHA256 != expectedSHA256 {
		return "", fmt.Errorf(
			"runner predictions SHA-256 %q does not match verifier manifest %q",
			sourceSHA256,
			expectedSHA256,
		)
	}
	if snapshotSHA256 != expectedSHA256 {
		return "", fmt.Errorf(
			"verifier predictions snapshot SHA-256 %q does not match manifest %q",
			snapshotSHA256,
			expectedSHA256,
		)
	}
	if err := validateCommandFlagArtifactPath(verifier.Command.Command, "-p", snapshotAbs); err != nil {
		return "", fmt.Errorf("verifier command predictions binding: %w", err)
	}
	return expectedSHA256, nil
}

func validateCommandFlagArtifactPath(command []string, flag, expected string) error {
	values := make([]string, 0, 1)
	for i := 0; i < len(command); i++ {
		if command[i] != flag {
			continue
		}
		if i+1 >= len(command) {
			return fmt.Errorf("flag %s has no value", flag)
		}
		values = append(values, command[i+1])
		i++
	}
	if len(values) != 1 {
		return fmt.Errorf("flag %s appears %d times", flag, len(values))
	}
	if !sameArtifactPath(values[0], expected) {
		return fmt.Errorf("flag %s value %q does not match artifact %q", flag, values[0], expected)
	}
	return nil
}

func validateHarnessReportBinding(
	verifier verifyManifest,
	harnessReportPath string,
	requireAttested bool,
) (string, error) {
	if strings.TrimSpace(verifier.ReportError) != "" {
		return "", fmt.Errorf("verifier report attestation failed: %s", verifier.ReportError)
	}
	if strings.TrimSpace(harnessReportPath) == "" {
		if requireAttested {
			return "", fmt.Errorf("native finalization requires --harness-report with verify-time attestation")
		}
		return "", nil
	}
	expectedHarnessRunID := verifier.RunID + "-" + verifier.Target
	hasAttestation := strings.TrimSpace(verifier.Report.HarnessRunID) != "" ||
		strings.TrimSpace(verifier.Report.Path) != "" ||
		strings.TrimSpace(verifier.Report.SHA256) != ""
	if requireAttested && !hasAttestation {
		return "", fmt.Errorf("native harness report has no verify-time report attestation")
	}
	if hasAttestation {
		if verifier.Report.HarnessRunID != expectedHarnessRunID {
			return "", fmt.Errorf(
				"verifier report harness_run_id %q does not match %q",
				verifier.Report.HarnessRunID,
				expectedHarnessRunID,
			)
		}
		if !sameArtifactPath(verifier.Report.Path, harnessReportPath) {
			return "", fmt.Errorf(
				"harness report %q does not match verifier-bound report %q",
				harnessReportPath,
				verifier.Report.Path,
			)
		}
	}
	reportPath := absPath(harnessReportPath)
	outputDir := absPath(verifier.Config.OutputDir)
	if filepath.Dir(reportPath) != filepath.Clean(outputDir) {
		return "", fmt.Errorf(
			"verifier-bound harness report %q is not directly inside output_dir %q",
			reportPath,
			outputDir,
		)
	}
	if !strings.HasSuffix(filepath.Base(reportPath), "."+expectedHarnessRunID+".json") {
		return "", fmt.Errorf(
			"verifier-bound harness report %q does not belong to harness run %q",
			reportPath,
			expectedHarnessRunID,
		)
	}
	data, err := readRegularArtifact(reportPath)
	if err != nil {
		return "", fmt.Errorf("read verifier-bound harness report: %w", err)
	}
	actualSHA256 := fmt.Sprintf("%x", sha256.Sum256(data))
	if hasAttestation {
		if !isSHA256Hex(verifier.Report.SHA256) {
			return "", fmt.Errorf("verifier report sha256 %q is not a SHA-256 digest", verifier.Report.SHA256)
		}
		if actualSHA256 != verifier.Report.SHA256 {
			return "", fmt.Errorf(
				"harness report SHA-256 %q does not match verifier manifest %q",
				actualSHA256,
				verifier.Report.SHA256,
			)
		}
	}
	if !sameArtifactPath(verifier.Command.Dir, verifier.Config.OutputDir) {
		return "", fmt.Errorf(
			"verifier command dir %q does not match output_dir %q",
			verifier.Command.Dir,
			verifier.Config.OutputDir,
		)
	}
	if err := validateCommandFlagValue(verifier.Command.Command, "--report_dir", verifier.Config.OutputDir); err != nil {
		return "", fmt.Errorf("verifier command report binding: %w", err)
	}
	if err := validateCommandFlagValue(verifier.Command.Command, "-id", expectedHarnessRunID); err != nil {
		return "", fmt.Errorf("verifier command run-id binding: %w", err)
	}
	return actualSHA256, nil
}

func validateCommandFlagValue(command []string, flag, expected string) error {
	values := make([]string, 0, 1)
	for i := 0; i < len(command); i++ {
		if command[i] != flag {
			continue
		}
		if i+1 >= len(command) {
			return fmt.Errorf("flag %s has no value", flag)
		}
		values = append(values, command[i+1])
		i++
	}
	if len(values) != 1 {
		return fmt.Errorf("expected exactly one %s flag, found %d", flag, len(values))
	}
	if flag == "--report_dir" {
		if !sameArtifactPath(values[0], expected) {
			return fmt.Errorf("flag %s value %q does not match %q", flag, values[0], expected)
		}
		return nil
	}
	if values[0] != expected {
		return fmt.Errorf("flag %s value %q does not match %q", flag, values[0], expected)
	}
	return nil
}

var allowedImportedMainStatuses = map[string]struct{}{
	"resolved":    {},
	"unresolved":  {},
	"error":       {},
	"empty_patch": {},
	"incomplete":  {},
}

func validateImportedCaseBindings(
	rows []importedCase,
	predictions map[string]contract.Prediction,
	expectedCases map[string]contract.Case,
	requireExactCaseMetadata bool,
	importedRoot string,
	target string,
	harnessReportPath string,
) error {
	harness := contract.NewHarnessIndex()
	expectedVerifierRef := ""
	if strings.TrimSpace(harnessReportPath) != "" {
		if _, err := readRegularArtifact(harnessReportPath); err != nil {
			return fmt.Errorf("read harness report for imported case validation: %w", err)
		}
		var err error
		harness, err = readHarnessReport(harnessReportPath)
		if err != nil {
			return fmt.Errorf("read harness report for imported case validation: %w", err)
		}
		selectedCases := make([]contract.Case, 0, len(rows))
		for _, row := range rows {
			selectedCases = append(selectedCases, contract.Case{InstanceID: row.InstanceID})
		}
		if err := validateHarnessIndex(harness, selectedCases); err != nil {
			return fmt.Errorf("validate harness report for imported case validation: %w", err)
		}
		expectedVerifierRef = absPath(harnessReportPath)
	}

	for _, row := range rows {
		status := row.Result.MainStatus
		if _, ok := allowedImportedMainStatuses[status]; !ok {
			return fmt.Errorf("imported instance %q has unsupported result.main_status %q", row.InstanceID, status)
		}
		expectedCase, ok := expectedCases[row.InstanceID]
		if !ok {
			return fmt.Errorf("imported instance %q has no matching full case metadata", row.InstanceID)
		}
		metadataEmpty := row.Repo == "" && row.BaseCommit == ""
		metadataExact := row.Repo == expectedCase.Repo && row.BaseCommit == expectedCase.BaseCommit
		if (requireExactCaseMetadata || !metadataEmpty) && !metadataExact {
			return fmt.Errorf(
				"imported instance %q repo/base_commit %q/%q do not match full case metadata %q/%q",
				row.InstanceID,
				row.Repo,
				row.BaseCommit,
				expectedCase.Repo,
				expectedCase.BaseCommit,
			)
		}
		prediction, ok := predictions[row.InstanceID]
		if !ok {
			return fmt.Errorf("imported instance %q has no matching prediction", row.InstanceID)
		}
		if row.Result.ModelNameOrPath != prediction.ModelNameOrPath {
			return fmt.Errorf(
				"imported instance %q model_name_or_path %q does not match prediction %q",
				row.InstanceID,
				row.Result.ModelNameOrPath,
				prediction.ModelNameOrPath,
			)
		}

		expectedPatchPath := ""
		patchArtifactPath := filepath.Join(importedRoot, "patches", target, row.InstanceID+".patch")
		if strings.TrimSpace(prediction.ModelPatch) != "" {
			expectedPatchPath = filepath.Join("patches", target, row.InstanceID+".patch")
			patchData, err := readRegularArtifact(patchArtifactPath)
			if err != nil {
				return fmt.Errorf("read imported patch for %s: %w", row.InstanceID, err)
			}
			if string(patchData) != prediction.ModelPatch {
				return fmt.Errorf("imported patch for %s does not exactly match prediction model_patch", row.InstanceID)
			}
		} else if _, err := os.Lstat(patchArtifactPath); err == nil {
			return fmt.Errorf("imported instance %q has an unexpected patch artifact for an empty prediction", row.InstanceID)
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("inspect imported patch for %s: %w", row.InstanceID, err)
		}
		if row.Result.PatchPath != expectedPatchPath {
			return fmt.Errorf(
				"imported instance %q patch_path %q does not match fixed path %q",
				row.InstanceID,
				row.Result.PatchPath,
				expectedPatchPath,
			)
		}
		expectedStats := artifact.ComputePatchStats(prediction.ModelPatch)
		if !equalPatchStats(row.Result.PatchStats, expectedStats) {
			return fmt.Errorf(
				"imported instance %q patch_stats %+v do not match prediction stats %+v",
				row.InstanceID,
				row.Result.PatchStats,
				expectedStats,
			)
		}

		if row.Result.VerifierResultRef != expectedVerifierRef {
			return fmt.Errorf(
				"imported instance %q verifier_result_ref %q does not match harness report %q",
				row.InstanceID,
				row.Result.VerifierResultRef,
				expectedVerifierRef,
			)
		}
		expectedStatus, expectedReason := classify(row.InstanceID, true, prediction.ModelPatch, harness)
		if row.Result.MainStatus != expectedStatus || row.Result.FailureReason != expectedReason {
			return fmt.Errorf(
				"imported instance %q result status/reason %q/%q does not match recomputed %q/%q",
				row.InstanceID,
				row.Result.MainStatus,
				row.Result.FailureReason,
				expectedStatus,
				expectedReason,
			)
		}
	}
	return nil
}

func validateNativeImportedBundles(
	rows []importedCase,
	predictions map[string]contract.Prediction,
	importedRoot string,
	target string,
	manifest runnerManifest,
	selection runConfigSelection,
) error {
	for _, row := range rows {
		prediction := predictions[row.InstanceID]
		caseDir := filepath.Join(manifest.OutputDir, row.InstanceID)
		rawTracePath := filepath.Join(caseDir, row.InstanceID+".native.json")
		rawTrace, err := readRegularArtifact(rawTracePath)
		if err != nil {
			return fmt.Errorf("read native result bundle for %s: %w", row.InstanceID, err)
		}
		trace, err := parseNativeTraceEnvelope(rawTrace, row.InstanceID)
		if err != nil {
			return fmt.Errorf("validate native result bundle for %s: %w", row.InstanceID, err)
		}
		if err := validateNativeTraceIdentity(row.InstanceID, trace, manifest, selection); err != nil {
			return err
		}
		if trace.ModelPatch != prediction.ModelPatch {
			return fmt.Errorf("native result bundle for %s model_patch does not match prediction", row.InstanceID)
		}

		expectedTracePath := filepath.Join("traces", target, row.InstanceID+".json")
		if row.Result.TracePath != expectedTracePath {
			return fmt.Errorf(
				"imported instance %q trace_path %q does not match fixed path %q",
				row.InstanceID,
				row.Result.TracePath,
				expectedTracePath,
			)
		}
		normalizedTrace, err := readRegularArtifact(filepath.Join(importedRoot, expectedTracePath))
		if err != nil {
			return fmt.Errorf("read normalized native trace for %s: %w", row.InstanceID, err)
		}
		if !bytes.Equal(normalizedTrace, redactJSONBytes(rawTrace)) {
			return fmt.Errorf("normalized native trace for %s does not exactly match the redacted raw trace", row.InstanceID)
		}
		usage, err := extractNativeUsage(rawTrace, row.InstanceID)
		if err != nil {
			return fmt.Errorf("extract native usage for %s: %w", row.InstanceID, err)
		}
		if row.Result.Usage != usage {
			return fmt.Errorf(
				"imported native usage for %s %+v does not match raw bundle %+v",
				row.InstanceID,
				row.Result.Usage,
				usage,
			)
		}

		responsesPath := filepath.Join(caseDir, row.InstanceID+".responses.json")
		responsesData, err := readRegularArtifact(responsesPath)
		if err != nil {
			return fmt.Errorf("read native responses bundle for %s: %w", row.InstanceID, err)
		}
		var responses []json.RawMessage
		if err := json.Unmarshal(responsesData, &responses); err != nil {
			return fmt.Errorf("decode native responses bundle for %s: %w", row.InstanceID, err)
		}
		if len(responses) != trace.ResponseCount {
			return fmt.Errorf(
				"native responses bundle for %s has count %d, trace declares %d",
				row.InstanceID,
				len(responses),
				trace.ResponseCount,
			)
		}
		responsesSHA256 := fmt.Sprintf("%x", sha256.Sum256(responsesData))
		if responsesSHA256 != trace.ResponsesSHA256 {
			return fmt.Errorf(
				"native responses bundle for %s has SHA-256 %q, trace declares %q",
				row.InstanceID,
				responsesSHA256,
				trace.ResponsesSHA256,
			)
		}
	}
	return nil
}

func validateNativeTraceIdentity(
	instanceID string,
	trace nativeTraceEnvelope,
	manifest runnerManifest,
	selection runConfigSelection,
) error {
	checks := []struct {
		name string
		got  string
		want string
	}{
		{"info.run_id", trace.Info.RunID, manifest.RunID},
		{"info.observation_codec", trace.Info.ObservationCodec, manifest.ObservationCodec},
		{"info.source_revision", trace.Info.SourceRevision, manifest.SourceRevision},
		{"info.binary_sha256", trace.Info.BinarySHA256, manifest.BinarySHA256},
		{"info.model_config_sha256", trace.Info.ModelConfigSHA256, manifest.ModelConfigSHA256},
		{"info.environment_config_sha256", trace.Info.EnvironmentConfigSHA256, manifest.EnvironmentConfigSHA256},
		{"info.cases_sha256", trace.Info.CasesSHA256, manifest.CasesSHA256},
		{"info.command_timeout", trace.Info.CommandTimeout, manifest.CommandTimeout},
		{"info.case_timeout", trace.Info.CaseTimeout, manifest.CaseTimeout},
		{"info.selected_instances_sha256", trace.Info.SelectedInstancesSHA256, selection.CaseListHash},
	}
	for _, check := range checks {
		if check.got != check.want {
			return fmt.Errorf(
				"native result bundle for %s %s %q does not match runner manifest/selection %q",
				instanceID,
				check.name,
				check.got,
				check.want,
			)
		}
	}
	if trace.Info.SourceModified != manifest.SourceModified {
		return fmt.Errorf(
			"native result bundle for %s info.source_modified=%t does not match runner manifest %t",
			instanceID,
			trace.Info.SourceModified,
			manifest.SourceModified,
		)
	}
	if trace.Info.Workers != manifest.Workers {
		return fmt.Errorf(
			"native result bundle for %s info.workers=%d does not match runner manifest %d",
			instanceID,
			trace.Info.Workers,
			manifest.Workers,
		)
	}
	return nil
}

func readRegularArtifact(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("artifact %s is a symbolic link", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("artifact %s is not a regular file", path)
	}
	return os.ReadFile(path)
}

func equalPatchStats(a, b artifact.PatchStats) bool {
	if a.AddedLines != b.AddedLines || a.DeletedLines != b.DeletedLines || a.PatchLines != b.PatchLines {
		return false
	}
	if len(a.ChangedFiles) != len(b.ChangedFiles) {
		return false
	}
	for i := range a.ChangedFiles {
		if a.ChangedFiles[i] != b.ChangedFiles[i] {
			return false
		}
	}
	return true
}

func readAndValidateImportedCases(
	path string,
	summary importSummary,
	target string,
) ([]importedCase, error) {
	if summary.SchemaVersion != importSchemaVersion {
		return nil, fmt.Errorf("unsupported import summary schema_version %d", summary.SchemaVersion)
	}
	if summary.Target != target {
		return nil, fmt.Errorf("import summary target %q does not match %q", summary.Target, target)
	}
	if summary.Total < 0 {
		return nil, fmt.Errorf("import summary total %d is negative", summary.Total)
	}

	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	rows := make([]importedCase, 0, summary.Total)
	seen := make(map[string]struct{}, summary.Total)
	counts := make(map[string]int)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := scanner.Bytes()
		if strings.TrimSpace(string(line)) == "" {
			return nil, fmt.Errorf("line %d is empty", lineNumber)
		}
		var row importedCase
		if err := json.Unmarshal(line, &row); err != nil {
			return nil, fmt.Errorf("decode line %d: %w", lineNumber, err)
		}
		if row.SchemaVersion != importSchemaVersion {
			return nil, fmt.Errorf(
				"line %d instance %q has schema_version %d, want %d",
				lineNumber,
				row.InstanceID,
				row.SchemaVersion,
				importSchemaVersion,
			)
		}
		if err := validateArtifactName("imported instance id", row.InstanceID); err != nil {
			return nil, fmt.Errorf("line %d: %w", lineNumber, err)
		}
		if _, exists := seen[row.InstanceID]; exists {
			return nil, fmt.Errorf("duplicate imported instance id %q", row.InstanceID)
		}
		seen[row.InstanceID] = struct{}{}
		if row.Target != summary.Target || row.Target != target {
			return nil, fmt.Errorf(
				"line %d instance %q target %q does not match import summary/current target %q",
				lineNumber,
				row.InstanceID,
				row.Target,
				target,
			)
		}
		if strings.TrimSpace(row.Result.MainStatus) == "" {
			return nil, fmt.Errorf("line %d instance %q has empty result.main_status", lineNumber, row.InstanceID)
		}
		if strings.TrimSpace(row.Result.MainStatus) != row.Result.MainStatus {
			return nil, fmt.Errorf(
				"line %d instance %q has non-canonical result.main_status %q",
				lineNumber,
				row.InstanceID,
				row.Result.MainStatus,
			)
		}
		if _, ok := allowedImportedMainStatuses[row.Result.MainStatus]; !ok {
			return nil, fmt.Errorf(
				"line %d instance %q has unsupported result.main_status %q",
				lineNumber,
				row.InstanceID,
				row.Result.MainStatus,
			)
		}
		counts[row.Result.MainStatus]++
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan imported cases: %w", err)
	}
	if len(rows) != summary.Total {
		return nil, fmt.Errorf("imported cases total %d does not match import summary total %d", len(rows), summary.Total)
	}
	if len(counts) != len(summary.Counts) {
		return nil, fmt.Errorf("imported status counts %v do not match import summary counts %v", counts, summary.Counts)
	}
	for status, count := range counts {
		if summary.Counts[status] != count {
			return nil, fmt.Errorf("imported status counts %v do not match import summary counts %v", counts, summary.Counts)
		}
	}
	return rows, nil
}

func validateSelectionIdentity(source string, ids []string, selection runConfigSelection) error {
	if len(ids) != selection.CaseCount {
		return fmt.Errorf(
			"%s count %d does not match selected case count %d",
			source,
			len(ids),
			selection.CaseCount,
		)
	}
	hash, err := selectedInstancesSHA256(ids)
	if err != nil {
		return fmt.Errorf("hash %s: %w", source, err)
	}
	if hash != selection.CaseListHash {
		return fmt.Errorf(
			"%s hash %q does not match selected case hash %q",
			source,
			hash,
			selection.CaseListHash,
		)
	}
	return nil
}

func validateSelectionSubset(selectedIDs, fullIDs []string) error {
	full := make(map[string]struct{}, len(fullIDs))
	for _, id := range fullIDs {
		full[id] = struct{}{}
	}
	for _, id := range selectedIDs {
		if _, ok := full[id]; !ok {
			return fmt.Errorf("selected instance %q is not present in full cases manifest", id)
		}
	}
	return nil
}

func caseIDs(cases []contract.Case) []string {
	ids := make([]string, 0, len(cases))
	for _, c := range cases {
		ids = append(ids, c.InstanceID)
	}
	return ids
}

func validateGenericRunnerModelName(manifest runnerManifest, modelName string) error {
	if manifest.RunnerType != "trpc-agent-go-native" {
		return nil
	}
	actual := manifest.ModelConfig["MODEL_NAME"]
	expected := modelName
	if actual != strings.TrimSpace(actual) {
		return fmt.Errorf("native runner MODEL_NAME %q is not canonical", actual)
	}
	if expected != strings.TrimSpace(expected) {
		return fmt.Errorf("native -model-name %q is not canonical", expected)
	}
	if actual == "" {
		return fmt.Errorf("native runner manifest has no MODEL_NAME")
	}
	if actual != expected {
		return fmt.Errorf("native runner MODEL_NAME %q does not match -model-name %q", actual, expected)
	}
	return nil
}

func validateNativePredictionModelNames(
	predictions map[string]contract.Prediction,
	manifest runnerManifest,
) error {
	modelName := manifest.ModelConfig["MODEL_NAME"]
	if modelName == "" {
		return fmt.Errorf("native runner manifest has no MODEL_NAME for prediction attribution")
	}
	if modelName != strings.TrimSpace(modelName) {
		return fmt.Errorf("native runner MODEL_NAME %q is not canonical for prediction attribution", modelName)
	}
	expected := "trpc-agent-go/" + modelName
	ids := make([]string, 0, len(predictions))
	for id := range predictions {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if predictions[id].ModelNameOrPath != expected {
			return fmt.Errorf(
				"native prediction %q model_name_or_path %q does not match runner identity %q",
				id,
				predictions[id].ModelNameOrPath,
				expected,
			)
		}
	}
	return nil
}

func validateCasesContent(manifest prepareDataManifest) error {
	if strings.TrimSpace(manifest.CasesJSONLSHA256) == "" {
		return nil
	}
	if strings.TrimSpace(manifest.OutputDir) == "" {
		return fmt.Errorf("cases manifest has cases_jsonl_sha256 but no output_dir")
	}
	path := filepath.Join(manifest.OutputDir, "cases.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read cases.jsonl for content verification: %w", err)
	}
	actual := fmt.Sprintf("%x", sha256.Sum256(data))
	if actual != manifest.CasesJSONLSHA256 {
		return fmt.Errorf("cases.jsonl SHA-256 %q does not match cases manifest %q", actual, manifest.CasesJSONLSHA256)
	}
	return nil
}

func sameArtifactPath(a, b string) bool {
	if strings.TrimSpace(a) == "" || strings.TrimSpace(b) == "" {
		return false
	}
	return filepath.Clean(absPath(a)) == filepath.Clean(absPath(b))
}

func maxShardWorkers(manifest shardsManifest) int {
	maxWorkers := 0
	for _, shard := range manifest.Shards {
		if shard.Workers > maxWorkers {
			maxWorkers = shard.Workers
		}
	}
	return maxWorkers
}

func readJSONFile(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return err
	}
	return nil
}

func miniSWEAgentVersion(report doctorReport) string {
	texts := []string{}
	if check, ok := report.Checks["mini_extra_help"]; ok {
		texts = append(texts, check.Detail)
	}
	if cmd, ok := report.Commands["mini_extra_help"]; ok {
		texts = append(texts, cmd.Stdout, cmd.Stderr)
	}
	for _, text := range texts {
		fields := strings.Fields(text)
		for i, field := range fields {
			if strings.EqualFold(field, "version") && i+1 < len(fields) {
				return strings.Trim(fields[i+1], " ,.;")
			}
			if strings.HasPrefix(field, "v") && len(field) > 1 && isVersionLike(field[1:]) {
				return field
			}
			if isVersionLike(field) {
				return field
			}
		}
	}
	return ""
}

func sweBenchVersion(report doctorReport) string {
	if check, ok := report.Checks["swebench_version"]; ok && strings.TrimSpace(check.Detail) != "" {
		return strings.TrimSpace(check.Detail)
	}
	if cmd, ok := report.Commands["swebench_version"]; ok && strings.TrimSpace(cmd.Stdout) != "" {
		return strings.TrimSpace(firstLine(cmd.Stdout))
	}
	return ""
}

func isVersionLike(s string) bool {
	parts := strings.Split(strings.Trim(s, " ,.;"), ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if _, err := strconv.Atoi(strings.Trim(part, "v")); err != nil {
			return false
		}
	}
	return true
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func scanServiceFindings(logPath string) runConfigServiceFindings {
	if strings.TrimSpace(logPath) == "" {
		return runConfigServiceFindings{}
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		return runConfigServiceFindings{}
	}
	text := string(data)
	return runConfigServiceFindings{
		RateLimitErrors:          strings.Count(text, "RateLimitError"),
		ServiceUnavailableErrors: strings.Count(text, "ServiceUnavailableError"),
		WorkerUnavailableErrors:  strings.Count(text, "All workers for model"),
	}
}

func splitNotes(raw string) []string {
	parts := strings.Split(raw, ";")
	out := []string{}
	for _, part := range parts {
		note := strings.TrimSpace(part)
		if note != "" {
			out = append(out, note)
		}
	}
	return out
}
