//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package environment

import (
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Config is the mini-swe-agent compatible environment subset.
type Config struct {
	Environment struct {
		Env         map[string]string `yaml:"env"`
		Interpreter []string          `yaml:"interpreter"`
	} `yaml:"environment"`
}

// LoadConfig reads the command environment and interpreter from YAML.
func LoadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse environment config %s: %w", path, err)
	}
	if len(cfg.Environment.Interpreter) == 0 {
		return cfg, fmt.Errorf("environment config %s has no interpreter", path)
	}
	if cfg.Environment.Env == nil {
		cfg.Environment.Env = map[string]string{}
	}
	return cfg, nil
}
