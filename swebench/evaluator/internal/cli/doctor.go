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
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type doctorReport struct {
	RunID           string                   `json:"run_id"`
	StartedAt       time.Time                `json:"started_at"`
	FinishedAt      time.Time                `json:"finished_at"`
	OutputDir       string                   `json:"output_dir"`
	DockerHost      string                   `json:"docker_host"`
	HTTPBinURL      string                   `json:"httpbin_url,omitempty"`
	HTTPBinCABundle string                   `json:"httpbin_ca_bundle,omitempty"`
	Checks          map[string]doctorCheck   `json:"checks"`
	ModelConfig     map[string]string        `json:"model_config,omitempty"`
	Commands        map[string]commandResult `json:"commands"`
	Notes           []string                 `json:"notes,omitempty"`
}

type doctorCheck struct {
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

func runDoctor(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	runID := fs.String("run-id", "doctor-smoke", "run id for output paths")
	output := fs.String("output", "../results/runs/doctor-smoke", "output directory")
	python := fs.String("python", envOrDefault("PYTHON", "python"), "python executable")
	miniExtra := fs.String("mini-extra", envOrDefault("MINI_EXTRA", "mini-extra"), "mini-extra executable")
	docker := fs.String("docker", envOrDefault("DOCKER", "docker"), "docker executable")
	dockerHost := fs.String("docker-host", envOrDefault("DOCKER_HOST", defaultDockerHost), "Docker host")
	httpbinURL := fs.String("httpbin-url", os.Getenv("SWEBENCH_HTTPBIN_URL"), "optional HTTPBin-compatible endpoint for calibrated verifier checks")
	httpbinCABundle := fs.String("httpbin-ca-bundle", os.Getenv("SWEBENCH_HTTPBIN_CA_BUNDLE"), "optional CA bundle for calibrated verifier containers")
	modelConfig := fs.String("model-config", "../config/models/glm-5.2.local.yaml", "ignored model YAML config for model smoke")
	timeout := fs.Duration("model-timeout", 60*time.Second, "model smoke timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if err := ensureDir(*output); err != nil {
		return err
	}

	report := doctorReport{
		RunID:           *runID,
		StartedAt:       time.Now().UTC(),
		OutputDir:       absPath(*output),
		DockerHost:      *dockerHost,
		HTTPBinURL:      strings.TrimSpace(*httpbinURL),
		HTTPBinCABundle: strings.TrimSpace(*httpbinCABundle),
		Checks:          map[string]doctorCheck{},
		Commands:        map[string]commandResult{},
	}

	env := []string{"DOCKER_HOST=" + *dockerHost}
	report.Commands["python_version"] = runCapture(ctx, "", nil, *python, "--version")
	report.Commands["mini_extra_help"] = runCapture(ctx, "", nil, *miniExtra, "swebench", "--help")
	report.Commands["swebench_version"] = runCapture(ctx, "", nil, *python, "-c", "import importlib.metadata as m; print(m.version('swebench'))")
	report.Commands["docker_info"] = runCapture(ctx, "", env, *docker, "info", "--format", "server={{.ServerVersion}} root={{.DockerRootDir}} driver={{.Driver}} arch={{.Architecture}}")
	report.Commands["docker_version"] = runCapture(ctx, "", env, *docker, "version", "--format", "client={{.Client.Version}} server={{.Server.Version}} api={{.Server.APIVersion}}")
	report.Commands["dataset_load"] = runCapture(ctx, "", nil, *python, "-c", "from datasets import load_dataset; ds=load_dataset('"+defaultDatasetName+"', split='"+defaultSplit+"'); print(len(ds)); print(ds[0]['instance_id'])")
	if strings.TrimSpace(*httpbinURL) != "" {
		report.Commands["httpbin_smoke"] = httpbinSmoke(ctx, *httpbinURL, *timeout)
	} else {
		report.Checks["httpbin_smoke"] = doctorCheck{Status: "skip", Detail: "not configured"}
	}

	for name, cmd := range report.Commands {
		if cmd.ExitCode == 0 {
			report.Checks[name] = doctorCheck{Status: "ok", Detail: firstLine(cmd.Stdout)}
		} else {
			report.Checks[name] = doctorCheck{Status: "fail", Detail: strings.TrimSpace(cmd.Stderr + "\n" + cmd.Stdout)}
		}
	}

	if strings.TrimSpace(*modelConfig) != "" {
		modelResult, safeCfg := modelSmoke(ctx, *modelConfig, *timeout)
		report.ModelConfig = safeCfg
		report.Commands["model_smoke"] = modelResult
		if modelResult.ExitCode == 0 {
			report.Checks["model_smoke"] = doctorCheck{Status: "ok", Detail: firstLine(modelResult.Stdout)}
		} else {
			report.Checks["model_smoke"] = doctorCheck{Status: "fail", Detail: strings.TrimSpace(modelResult.Error + "\n" + modelResult.Stdout + "\n" + modelResult.Stderr)}
		}
	}

	report.FinishedAt = time.Now().UTC()
	if err := writeJSON(filepath.Join(*output, "doctor.json"), report); err != nil {
		return err
	}
	if err := writeDoctorLog(filepath.Join(*output, "doctor.log"), report); err != nil {
		return err
	}
	printDoctorSummary(report)
	return nil
}

func modelSmoke(ctx context.Context, configPath string, timeout time.Duration) (commandResult, map[string]string) {
	start := time.Now()
	cfg, err := loadModelConfig(configPath)
	safe := map[string]string{}
	for k, v := range cfg {
		if isSecretKey(k) {
			safe[k] = "<redacted>"
		} else {
			safe[k] = v
		}
	}
	res := commandResult{
		Command:   []string{"HTTP", "POST", "/chat/completions"},
		StartedAt: start.UTC(),
	}
	if err != nil {
		res.ExitCode = -1
		res.Error = err.Error()
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
		return res, safe
	}
	baseURL := strings.TrimRight(cfg["OPENAI_BASE_URL"], "/")
	model := cfg["MODEL_NAME"]
	if baseURL == "" || model == "" {
		res.ExitCode = -1
		res.Error = "OPENAI_BASE_URL and MODEL_NAME are required"
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
		return res, safe
	}
	body := map[string]any{
		"model": model,
		"messages": []map[string]string{{
			"role":    "user",
			"content": "hi",
		}},
		"max_tokens": 16,
	}
	if v := strings.TrimSpace(cfg["MODEL_TEMPERATURE"]); v != "" {
		body["temperature"] = json.Number(v)
	}
	if v := strings.TrimSpace(cfg["MODEL_REASONING_EFFORT"]); v != "" {
		body["reasoning_effort"] = v
	}
	data, _ := json.Marshal(body)

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(data))
	if err != nil {
		res.ExitCode = -1
		res.Error = err.Error()
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
		return res, safe
	}
	req.Header.Set("Content-Type", "application/json")
	if v := strings.TrimSpace(cfg["OPENAI_API_KEY"]); v != "" {
		req.Header.Set("Authorization", "Bearer "+v)
	}
	for _, key := range []string{"X_SMG_ROUTING_KEY", "X_SMG_AGENT_NAME", "X_SMG_PROVIDER"} {
		if v := strings.TrimSpace(cfg[key]); v != "" {
			req.Header.Set(strings.ReplaceAll(key, "_", "-"), v)
		}
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.ExitCode = -1
		res.Error = err.Error()
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
		return res, safe
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	res.FinishedAt = time.Now().UTC()
	res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
	res.Stdout = fmt.Sprintf("status=%d body=%s", resp.StatusCode, string(redactJSONBytes(respBody)))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.ExitCode = 0
	} else {
		res.ExitCode = resp.StatusCode
		res.Error = resp.Status
	}
	return res, safe
}

func writeDoctorLog(path string, report doctorReport) error {
	var b strings.Builder
	fmt.Fprintf(&b, "run_id=%s\n", report.RunID)
	fmt.Fprintf(&b, "output_dir=%s\n", report.OutputDir)
	fmt.Fprintf(&b, "docker_host=%s\n\n", report.DockerHost)
	for name, check := range report.Checks {
		fmt.Fprintf(&b, "[%s] %s\n%s\n\n", check.Status, name, check.Detail)
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func printDoctorSummary(report doctorReport) {
	names := []string{
		"python_version",
		"swebench_version",
		"docker_info",
		"docker_version",
		"dataset_load",
		"mini_extra_help",
		"httpbin_smoke",
		"model_smoke",
	}
	ok := 0
	fail := 0
	fmt.Printf("doctor: %s\n", report.RunID)
	for _, name := range names {
		check, exists := report.Checks[name]
		if !exists {
			continue
		}
		switch check.Status {
		case "ok":
			ok++
		case "skip":
		default:
			fail++
		}
		if check.Detail != "" {
			fmt.Printf("[%s] %s: %s\n", check.Status, name, check.Detail)
		} else {
			fmt.Printf("[%s] %s\n", check.Status, name)
		}
	}
	fmt.Printf("summary: ok=%d fail=%d report=%s\n", ok, fail, filepath.Join(report.OutputDir, "doctor.json"))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

func httpbinSmoke(ctx context.Context, url string, timeout time.Duration) commandResult {
	start := time.Now()
	res := commandResult{
		Command:   []string{"HTTP", "GET", strings.TrimRight(url, "/") + "/get"},
		StartedAt: start.UTC(),
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, strings.TrimRight(url, "/")+"/get", nil)
	if err != nil {
		res.ExitCode = -1
		res.Error = err.Error()
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
		return res
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		res.ExitCode = -1
		res.Error = err.Error()
		res.FinishedAt = time.Now().UTC()
		res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
		return res
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	res.FinishedAt = time.Now().UTC()
	res.DurationMS = res.FinishedAt.Sub(start).Milliseconds()
	res.Stdout = fmt.Sprintf("status=%d body=%s", resp.StatusCode, string(body))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		res.ExitCode = 0
	} else {
		res.ExitCode = resp.StatusCode
		res.Error = resp.Status
	}
	return res
}
