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
	"os"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
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
