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
	"fmt"
	"os"
	"regexp"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
)

var (
	targetLabelPattern  = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)
	artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
)

const (
	defaultDatasetName = "princeton-nlp/SWE-bench_Verified"
	defaultSplit       = "test"
	defaultDockerHost  = "unix:///var/run/docker.sock"
)

type envConfig = modelconfig.EnvConfig

func loadEnvFile(path string) (envConfig, error) {
	return modelconfig.LoadEnvFile(path)
}

func loadModelConfig(path string) (envConfig, error) {
	return modelconfig.Load(path)
}

func loadMiniSWEAgentYAML(path string) (envConfig, error) {
	return modelconfig.LoadMiniSWEAgentYAML(path)
}

func stripInlineYAMLComment(s string) string {
	return modelconfig.StripInlineYAMLComment(s)
}

func cleanYAMLScalar(s string) string {
	return modelconfig.CleanYAMLScalar(s)
}

func modelHTTPHeaders(cfg envConfig) map[string]string {
	return modelconfig.HTTPHeaders(cfg)
}

func writeJSON(path string, v any) error {
	return artifact.WriteJSON(path, v)
}

func absPath(path string) string {
	return artifact.AbsPath(path)
}

func ensureDir(path string) error {
	return artifact.EnsureDir(path)
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}

func validateTargetLabel(target string) error {
	if len(target) == 0 || len(target) > 128 || !targetLabelPattern.MatchString(target) {
		return fmt.Errorf(
			"invalid target %q: use a lowercase slug such as baseline, mini-go, or tag",
			target,
		)
	}
	return nil
}

func validateArtifactName(kind, value string) error {
	if len(value) == 0 || len(value) > 128 || value == "." || value == ".." || !artifactNamePattern.MatchString(value) {
		return fmt.Errorf("invalid %s %q: use 1-128 letters, digits, dots, underscores, or hyphens", kind, value)
	}
	return nil
}
