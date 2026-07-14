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
	"path/filepath"
	"testing"
)

func TestNormalizeBillingFiltersAgentAndSumsExactly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "billing.json")
	data := `[
{"agent_name":"BenchSWE-codec-json-e1","input_tokens":100,"output_tokens":10,"total_tokens":110,"prompt_cached_tokens":80,"cost":0.01},
{"agent_name":"other","input_tokens":999,"output_tokens":999,"total_tokens":1998,"prompt_cached_tokens":0,"cost":9},
{"agent_name":"BenchSWE-codec-json-e1","input_tokens":"200","output_tokens":"20","total_tokens":"221","prompt_cached_tokens":"150","cost":"0.0025"}
]`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	doc, err := normalizeBilling(path, billingIdentity{
		ObservationCodec: "json",
		BillingAgentName: "BenchSWE-codec-json-e1",
		ExperimentID:     "codec-e1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if doc.InputTokens != 300 || doc.OutputTokens != 30 || doc.TotalTokens != 331 || doc.PromptCachedTokens != 230 {
		t.Fatalf("tokens = %+v", doc)
	}
	if doc.PromptUncachedTokens != 70 || doc.TotalTokenDelta != 1 {
		t.Fatalf("derived tokens = %+v", doc)
	}
	if doc.Cost != "0.0125" {
		t.Fatalf("Cost = %q, want 0.0125", doc.Cost)
	}
	if doc.Source.MatchedRows != 2 || doc.Source.IgnoredRows != 1 || doc.Source.SHA256 == "" {
		t.Fatalf("Source = %+v", doc.Source)
	}
}

func TestNormalizeBillingRejectsCachedTokensAboveInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "billing.json")
	data := `{"agent_name":"BenchSWE-codec-text-e1","input_tokens":1,"output_tokens":1,"total_tokens":2,"prompt_cached_tokens":2,"cost":0}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := normalizeBilling(path, billingIdentity{ObservationCodec: "text", BillingAgentName: "BenchSWE-codec-text-e1", ExperimentID: "codec-e1"})
	if err == nil {
		t.Fatal("normalizeBilling() error = nil")
	}
}

func TestNormalizeBillingRejectsMissingCost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "billing.json")
	data := `{"agent_name":"BenchSWE-codec-xml-e1","input_tokens":1,"output_tokens":1,"total_tokens":2,"prompt_cached_tokens":0}`
	if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := normalizeBilling(path, billingIdentity{ObservationCodec: "xml", BillingAgentName: "BenchSWE-codec-xml-e1", ExperimentID: "codec-e1"})
	if err == nil {
		t.Fatal("normalizeBilling() error = nil")
	}
}
