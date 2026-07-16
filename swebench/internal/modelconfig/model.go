//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package modelconfig

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/model/pricing"
)

// EnvConfig is the normalized model/environment configuration consumed by runners.
type EnvConfig map[string]string

// Load reads either a mini-SWE-agent YAML config or a simple KEY=VALUE env file.
func Load(path string) (EnvConfig, error) {
	lower := strings.ToLower(path)
	if strings.Contains(lower, ".yaml") || strings.Contains(lower, ".yml") {
		return LoadMiniSWEAgentYAML(path)
	}
	return LoadEnvFile(path)
}

// Pricing reads an optional framework rate card from normalized model
// configuration. The boolean is false when no pricing fields are present.
func Pricing(cfg EnvConfig) (pricing.RateCard, bool, error) {
	keys := []string{
		"PRICE_CURRENCY", "PRICE_UNIT_TOKENS", "PRICE_UNCACHED_INPUT",
		"PRICE_CACHED_INPUT", "PRICE_OUTPUT",
	}
	enabled := false
	for _, key := range keys {
		if strings.TrimSpace(cfg[key]) != "" {
			enabled = true
			break
		}
	}
	if !enabled {
		return pricing.RateCard{}, false, nil
	}
	card := pricing.RateCard{Currency: strings.TrimSpace(cfg["PRICE_CURRENCY"])}
	var err error
	if value := strings.TrimSpace(cfg["PRICE_UNIT_TOKENS"]); value != "" {
		card.UnitTokens, err = strconv.ParseInt(value, 10, 64)
		if err != nil {
			return pricing.RateCard{}, false, fmt.Errorf("parse PRICE_UNIT_TOKENS: %w", err)
		}
	}
	for key, target := range map[string]*float64{
		"PRICE_UNCACHED_INPUT": &card.UncachedInput,
		"PRICE_CACHED_INPUT":   &card.CachedInput,
		"PRICE_OUTPUT":         &card.Output,
	} {
		value := strings.TrimSpace(cfg[key])
		if value == "" {
			return pricing.RateCard{}, false, fmt.Errorf("%s is required when pricing is enabled", key)
		}
		*target, err = strconv.ParseFloat(value, 64)
		if err != nil {
			return pricing.RateCard{}, false, fmt.Errorf("parse %s: %w", key, err)
		}
	}
	if err := card.Validate(); err != nil {
		return pricing.RateCard{}, false, err
	}
	return card, true, nil
}

// LoadEnvFile reads KEY=VALUE files.
func LoadEnvFile(path string) (EnvConfig, error) {
	cfg := EnvConfig{}
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

// LoadMiniSWEAgentYAML reads the model subset of a mini-SWE-agent YAML config.
func LoadMiniSWEAgentYAML(path string) (EnvConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	cfg := EnvConfig{}
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
		val = strings.TrimSpace(StripInlineYAMLComment(val))
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
		setMiniYAMLModelValue(cfg, strings.Join(pathParts, "."), CleanYAMLScalar(val))
	}
	return cfg, nil
}

// StripInlineYAMLComment removes comments while preserving quoted values.
func StripInlineYAMLComment(s string) string {
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

// CleanYAMLScalar returns an unquoted YAML scalar for the simple config subset.
func CleanYAMLScalar(s string) string {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, `"'`)
	if unquoted, err := strconv.Unquote(`"` + strings.ReplaceAll(s, `"`, `\"`) + `"`); err == nil {
		return unquoted
	}
	return s
}

func setMiniYAMLModelValue(cfg EnvConfig, path, value string) {
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
	case "pricing.currency":
		cfg["PRICE_CURRENCY"] = value
	case "pricing.unit_tokens":
		cfg["PRICE_UNIT_TOKENS"] = value
	case "pricing.uncached_input":
		cfg["PRICE_UNCACHED_INPUT"] = value
	case "pricing.cached_input":
		cfg["PRICE_CACHED_INPUT"] = value
	case "pricing.output":
		cfg["PRICE_OUTPUT"] = value
	}
}
