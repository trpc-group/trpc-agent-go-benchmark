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
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMiniSWEAgentYAMLPricing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.yaml")
	content := []byte(`model:
  model_name: openai/glm52
pricing:
  currency: CNY
  unit_tokens: 1000000
  uncached_input: 8
  cached_input: 2
  output: 28
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	card, enabled, err := Pricing(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !enabled || card.Currency != "CNY" || card.UnitTokens != 1000000 ||
		card.UncachedInput != 8 || card.CachedInput != 2 || card.Output != 28 {
		t.Fatalf("pricing = %+v, enabled = %v", card, enabled)
	}
}

func TestPricingDisabledWithoutFields(t *testing.T) {
	_, enabled, err := Pricing(EnvConfig{"MODEL_NAME": "glm52"})
	if err != nil || enabled {
		t.Fatalf("enabled = %v, err = %v", enabled, err)
	}
}
