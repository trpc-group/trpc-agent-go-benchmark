//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"math"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go/model/pricing"
)

func TestPrepareResumeUsesPredictionsAsBoundary(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	if err := artifact.WriteJSON(path, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	selected := []contract.Case{{InstanceID: "case-a"}, {InstanceID: "case-b"}}
	preds, pending, skipped, err := prepareResume(path, selected, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(preds) != 1 || !reflect.DeepEqual(pending, []contract.Case{{InstanceID: "case-b"}}) || !reflect.DeepEqual(skipped, []string{"case-a"}) {
		t.Fatalf("preds=%#v pending=%#v skipped=%#v", preds, pending, skipped)
	}
}

func TestEstimateUsageCostMatchesHistoricalBilling(t *testing.T) {
	estimate, err := estimateUsageCost(pricing.RateCard{
		Currency: "CNY", UncachedInput: 8, CachedInput: 2, Output: 28,
	}, usageSummary{
		PromptTokens: 254957626, CachedTokens: 250148160,
		CompletionTokens: 3842970, TotalTokens: 258800596,
	})
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(estimate.TotalCost-646.375208) > 0.0000001 {
		t.Fatalf("total cost = %.9f", estimate.TotalCost)
	}
}

func TestPrepareResumeRedoRemovesSelectedPrediction(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	if err := artifact.WriteJSON(path, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
		"other":  {InstanceID: "other", ModelPatch: "keep"},
	}); err != nil {
		t.Fatal(err)
	}
	selected := []contract.Case{{InstanceID: "case-a"}}
	preds, pending, skipped, err := prepareResume(path, selected, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := preds["case-a"]; ok || preds["other"].ModelPatch != "keep" || !reflect.DeepEqual(pending, selected) || len(skipped) != 0 {
		t.Fatalf("preds=%#v pending=%#v skipped=%#v", preds, pending, skipped)
	}
}

func TestResolveBillingAgentName(t *testing.T) {
	got, err := resolveBillingAgentName("swebench", "codec-xml", "codec-e1")
	if err != nil || got != "swebench-codec-xml" {
		t.Fatalf("got %q, err %v", got, err)
	}
	if _, err := resolveBillingAgentName("swebench", "codec xml", "codec-e1"); err == nil {
		t.Fatal("invalid billing tag succeeded")
	}
}

func TestWorkspacePreloadOptOutRequiresCodeSearch(t *testing.T) {
	err := Run([]string{
		"trpc-agent-go-impl",
		"-run-id=test",
		"-model-config=unused",
		"-workspace-preload=false",
	})
	if err == nil || !strings.Contains(err.Error(), "requires -code-search") {
		t.Fatalf("error = %v, want code-search requirement", err)
	}
}

func TestASTWorkspaceRepresentationRequiresCodeSearch(t *testing.T) {
	err := Run([]string{
		"trpc-agent-go-impl",
		"-run-id=test",
		"-model-config=unused",
		"-workspace-representation=ast-code",
	})
	if err == nil || !strings.Contains(err.Error(), "requires -code-search") {
		t.Fatalf("error = %v, want code-search requirement", err)
	}
}

func TestWorkspaceRepresentationRejectsUnknownValue(t *testing.T) {
	err := Run([]string{
		"trpc-agent-go-impl",
		"-run-id=test",
		"-model-config=unused",
		"-workspace-representation=unknown",
	})
	if err == nil || !strings.Contains(err.Error(), "unsupported workspace representation") {
		t.Fatalf("error = %v, want unsupported representation", err)
	}
}
