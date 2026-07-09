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
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultDatasetName = "princeton-nlp/SWE-bench_Verified"
	defaultSplit       = "test"
	defaultDockerHost  = "unix:///var/run/docker.sock"
)

type envConfig map[string]string

func loadEnvFile(path string) (envConfig, error) {
	cfg := envConfig{}
	if strings.TrimSpace(path) == "" {
		return cfg, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(val)
		val = strings.Trim(val, `"'`)
		cfg[key] = val
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func loadModelConfig(path string) (envConfig, error) {
	lower := strings.ToLower(path)
	if strings.Contains(lower, ".yaml") || strings.Contains(lower, ".yml") {
		return loadMiniSWEAgentYAML(path)
	}
	return loadEnvFile(path)
}

func loadMiniSWEAgentYAML(path string) (envConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := envConfig{}
	type stackEntry struct {
		indent int
		key    string
	}
	var stack []stackEntry
	for _, raw := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(raw) == "" || strings.HasPrefix(strings.TrimSpace(raw), "#") {
			continue
		}
		indent := len(raw) - len(strings.TrimLeft(raw, " "))
		line := strings.TrimSpace(raw)
		if strings.HasPrefix(line, "- ") {
			continue
		}
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		val = strings.TrimSpace(stripInlineYAMLComment(val))
		for len(stack) > 0 && stack[len(stack)-1].indent >= indent {
			stack = stack[:len(stack)-1]
		}
		pathParts := make([]string, 0, len(stack)+1)
		for _, item := range stack {
			pathParts = append(pathParts, item.key)
		}
		pathParts = append(pathParts, key)
		if val == "" {
			stack = append(stack, stackEntry{indent: indent, key: key})
			continue
		}
		setMiniYAMLModelValue(cfg, strings.Join(pathParts, "."), cleanYAMLScalar(val))
	}
	return cfg, nil
}

func stripInlineYAMLComment(s string) string {
	inSingle := false
	inDouble := false
	for i, r := range s {
		switch r {
		case '\'':
			if !inDouble {
				inSingle = !inSingle
			}
		case '"':
			if !inSingle {
				inDouble = !inDouble
			}
		case '#':
			if !inSingle && !inDouble {
				return strings.TrimSpace(s[:i])
			}
		}
	}
	return s
}

func cleanYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return unquoted
	}
	return s
}

func setMiniYAMLModelValue(cfg envConfig, path, value string) {
	switch path {
	case "model.model_name":
		cfg["MODEL_NAME"] = strings.TrimPrefix(value, "openai/")
	case "model.model_kwargs.api_base":
		cfg["OPENAI_BASE_URL"] = value
	case "model.model_kwargs.api_key":
		cfg["OPENAI_API_KEY"] = value
	case "model.model_kwargs.timeout":
		cfg["MODEL_TIMEOUT_SECONDS"] = value
	case "model.model_kwargs.temperature":
		cfg["MODEL_TEMPERATURE"] = value
	case "model.model_kwargs.reasoning_effort":
		cfg["MODEL_REASONING_EFFORT"] = value
	case "model.model_kwargs.extra_headers.X-SMG-Routing-Key":
		cfg["X_SMG_ROUTING_KEY"] = value
	case "model.model_kwargs.extra_headers.X-SMG-Agent-Name":
		cfg["X_SMG_AGENT_NAME"] = value
	case "model.model_kwargs.extra_headers.X-SMG-Provider":
		cfg["X_SMG_PROVIDER"] = value
	}
}

func writeJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func absPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}

func ensureDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("empty directory")
	}
	return os.MkdirAll(path, 0o755)
}

func envOrDefault(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return fallback
}
