//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultDatasetName = "princeton-nlp/SWE-bench_Verified"
	defaultSplit       = "test"
	defaultDockerHost  = "tcp://localhost:2375"
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
