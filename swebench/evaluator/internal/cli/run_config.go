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
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type runConfigDocument struct {
	RunID           string                   `json:"run_id"`
	Target          string                   `json:"target"`
	GeneratedAt     time.Time                `json:"generated_at"`
	Dataset         runConfigDataset         `json:"dataset"`
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
	Name            string   `json:"name"`
	Split           string   `json:"split"`
	Revision        string   `json:"revision,omitempty"`
	CaseCount       int      `json:"case_count"`
	CaseListHash    string   `json:"case_list_hash"`
	HintsTextPolicy string   `json:"hints_text_policy"`
	SourceFields    []string `json:"source_fields,omitempty"`
	ExcludedFields  []string `json:"excluded_fields,omitempty"`
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
	Type               string              `json:"type"`
	Mode               string              `json:"mode,omitempty"`
	Python             string              `json:"python,omitempty"`
	SWEBenchVersion    string              `json:"swebench_version,omitempty"`
	DockerHost         string              `json:"docker_host,omitempty"`
	ManagedHTTPBin     *managedHTTPBinInfo `json:"managed_httpbin,omitempty"`
	CacheLevel         string              `json:"cache_level,omitempty"`
	Clean              bool                `json:"clean"`
	HarnessPatched     bool                `json:"harness_patched"`
	CompatPatch        bool                `json:"compat_patch"`
	CalibrationPatches []string            `json:"calibration_patches,omitempty"`
	StartedAt          string              `json:"started_at,omitempty"`
	FinishedAt         string              `json:"finished_at,omitempty"`
	DurationMS         int64               `json:"duration_ms,omitempty"`
}

type runConfigArtifacts struct {
	CasesManifest     string `json:"cases_manifest"`
	CasesJSONL        string `json:"cases_jsonl,omitempty"`
	RunnerOutputDir   string `json:"runner_output_dir,omitempty"`
	RunnerLog         string `json:"runner_log,omitempty"`
	MiniRawDir        string `json:"mini_raw_dir,omitempty"`
	MiniLog           string `json:"mini_log,omitempty"`
	Predictions       string `json:"predictions,omitempty"`
	VerifierReportDir string `json:"verifier_report_dir,omitempty"`
	HarnessReport     string `json:"harness_report,omitempty"`
	ImportedDir       string `json:"imported_dir,omitempty"`
	ImportedCases     string `json:"imported_cases,omitempty"`
	ImportSummary     string `json:"import_summary"`
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
	RunID       string            `json:"run_id"`
	RunnerType  string            `json:"runner_type"`
	StartedAt   time.Time         `json:"started_at"`
	FinishedAt  time.Time         `json:"finished_at"`
	DurationMS  int64             `json:"duration_ms"`
	OutputDir   string            `json:"output_dir"`
	CaseCount   int               `json:"case_count"`
	Workers     int               `json:"workers,omitempty"`
	Predictions string            `json:"predictions"`
	ModelConfig map[string]string `json:"model_config,omitempty"`
	Status      string            `json:"status,omitempty"`
}

func runRunConfig(args []string) error {
	fs := flag.NewFlagSet("run-config", flag.ExitOnError)
	runID := fs.String("run-id", "", "run id")
	target := fs.String("target", "baseline", "baseline or native")
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
	if strings.TrimSpace(*runnerManifestPath) == "" && strings.TrimSpace(*shardsManifestPath) == "" {
		*runnerManifestPath = *runMiniManifestPath
	}
	if strings.TrimSpace(*runnerManifestPath) == "" && strings.TrimSpace(*shardsManifestPath) == "" {
		return fmt.Errorf("missing required flag -runner-manifest, -run-mini-manifest, or -shards-manifest")
	}
	if *output == "" {
		*output = filepath.Join("results", "runs", *runID, "run_config.json")
	}

	var casesManifest prepareDataManifest
	if err := readJSONFile(*casesManifestPath, &casesManifest); err != nil {
		return fmt.Errorf("read cases manifest: %w", err)
	}
	var miniManifest runMiniManifest
	hasShardsManifest := strings.TrimSpace(*shardsManifestPath) != ""
	hasMiniManifest := strings.TrimSpace(*runMiniManifestPath) != "" || strings.Contains(filepath.Base(*runnerManifestPath), "run-mini")
	if hasMiniManifest {
		if err := readJSONFile(*runnerManifestPath, &miniManifest); err != nil {
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
		hasMiniManifest = false
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
			Name:            casesManifest.Dataset,
			Split:           casesManifest.Split,
			Revision:        casesManifest.Revision,
			CaseCount:       casesManifest.CaseCount,
			CaseListHash:    casesManifest.CaseListHash,
			HintsTextPolicy: casesManifest.HintsTextPolicy,
			SourceFields:    casesManifest.SourceFields,
			ExcludedFields:  casesManifest.ExcludedFields,
		},
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
			Type:               "official-local-harness",
			Mode:               verifierManifest.Config.VerifierMode,
			Python:             verifierManifest.Config.Python,
			SWEBenchVersion:    sweBenchVersion(doctor),
			DockerHost:         verifierManifest.Config.DockerHost,
			ManagedHTTPBin:     verifierManifest.ManagedHTTPBin,
			CacheLevel:         verifierManifest.Config.CacheLevel,
			Clean:              verifierManifest.Config.Clean,
			HarnessPatched:     verifierManifest.HarnessPatched,
			CompatPatch:        verifierManifest.Config.CompatPatch,
			CalibrationPatches: verifierManifest.CalibrationPatches,
			StartedAt:          formatTime(verifierManifest.StartedAt),
			FinishedAt:         formatTime(verifierManifest.FinishedAt),
			DurationMS:         verifierManifest.DurationMS,
		},
		Artifacts: runConfigArtifacts{
			CasesManifest:     absPath(*casesManifestPath),
			CasesJSONL:        filepath.Join(casesManifest.OutputDir, "cases.jsonl"),
			RunnerOutputDir:   outputDir,
			RunnerLog:         runnerLog,
			MiniRawDir:        miniRawDir,
			MiniLog:           miniLog,
			Predictions:       predictionsPath,
			VerifierReportDir: verifierManifest.Config.OutputDir,
			HarnessReport:     absPath(*harnessReportPath),
			ImportedDir:       filepath.Dir(filepath.Dir(absPath(*importSummaryPath))),
			ImportedCases:     filepath.Join(filepath.Dir(filepath.Dir(absPath(*importSummaryPath))), "cases.jsonl"),
			ImportSummary:     absPath(*importSummaryPath),
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
