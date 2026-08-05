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
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func TestPrepareResumeUsesPredictionsAsBoundary(t *testing.T) {
	output := t.TempDir()
	path := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(path, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	selected := []contract.Case{{InstanceID: "case-a"}, {InstanceID: "case-b"}}
	identity := testRunIdentity(t, "case-a", "case-b")
	writeResumeBundle(t, output, "case-a", "patch", identity)
	preds, pending, skipped, err := prepareResume(output, path, selected, false, identity)
	if err != nil {
		t.Fatal(err)
	}
	if len(preds) != 1 || !reflect.DeepEqual(pending, []contract.Case{{InstanceID: "case-b"}}) || !reflect.DeepEqual(skipped, []string{"case-a"}) {
		t.Fatalf("preds=%#v pending=%#v skipped=%#v", preds, pending, skipped)
	}
}

func TestPrepareResumeRedoRemovesOnlySelectedPrediction(t *testing.T) {
	output := t.TempDir()
	path := filepath.Join(output, "preds.json")
	if err := artifact.WriteJSON(path, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
	}); err != nil {
		t.Fatal(err)
	}
	selected := []contract.Case{{InstanceID: "case-a"}}
	identity := testRunIdentity(t, "case-a")
	writeResumeBundle(t, output, "case-a", "patch", identity)
	preds, pending, skipped, err := prepareResume(output, path, selected, true, identity)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := preds["case-a"]; ok || !reflect.DeepEqual(pending, selected) || len(skipped) != 0 {
		t.Fatalf("preds=%#v pending=%#v skipped=%#v", preds, pending, skipped)
	}
}

func TestPrepareResumeRejectsCorruptPredictions(t *testing.T) {
	output := t.TempDir()
	path := filepath.Join(output, "preds.json")
	if err := os.WriteFile(path, []byte(`{"case-a":{"instance_id":"other"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	identity := testRunIdentity(t, "case-a")
	if _, _, _, err := prepareResume(output, path, []contract.Case{{InstanceID: "case-a"}}, false, identity); err == nil {
		t.Fatal("corrupt predictions were accepted")
	}
}

func TestPrepareResumeRejectsOutsideSelectionAndIdentityMismatch(t *testing.T) {
	selected := []contract.Case{{InstanceID: "case-a"}}
	identity := testRunIdentity(t, "case-a")
	t.Run("outside selection", func(t *testing.T) {
		output := t.TempDir()
		path := filepath.Join(output, "preds.json")
		if err := artifact.WriteJSON(path, map[string]contract.Prediction{
			"other": {InstanceID: "other", ModelPatch: "patch"},
		}); err != nil {
			t.Fatal(err)
		}
		if _, _, _, err := prepareResume(output, path, selected, false, identity); err == nil || !strings.Contains(err.Error(), "current selected instance set") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("identity mismatch", func(t *testing.T) {
		output := t.TempDir()
		path := filepath.Join(output, "preds.json")
		if err := artifact.WriteJSON(path, map[string]contract.Prediction{
			"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
		}); err != nil {
			t.Fatal(err)
		}
		writeResumeBundle(t, output, "case-a", "patch", identity)
		changed := identity
		changed.ModelConfigSHA256 = strings.Repeat("f", 64)
		if _, _, _, err := prepareResume(output, path, selected, false, changed); err == nil || !strings.Contains(err.Error(), "model config hash") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("worker mismatch", func(t *testing.T) {
		output := t.TempDir()
		path := filepath.Join(output, "preds.json")
		if err := artifact.WriteJSON(path, map[string]contract.Prediction{
			"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
		}); err != nil {
			t.Fatal(err)
		}
		writeResumeBundle(t, output, "case-a", "patch", identity)
		changed := identity
		changed.Workers++
		if _, _, _, err := prepareResume(output, path, selected, false, changed); err == nil || !strings.Contains(err.Error(), "workers") {
			t.Fatalf("error = %v", err)
		}
	})
}

func testRunIdentity(t *testing.T, instanceIDs ...string) runIdentity {
	t.Helper()
	selectedHash, err := selectedInstancesSHA256(instanceIDs)
	if err != nil {
		t.Fatal(err)
	}
	return runIdentity{
		RunID: "run-1", ObservationCodec: "xml", SourceRevision: strings.Repeat("a", 40),
		BinarySHA256: strings.Repeat("b", 64), ModelConfigSHA256: strings.Repeat("c", 64),
		EnvironmentConfigSHA256: strings.Repeat("d", 64), CasesSHA256: strings.Repeat("e", 64),
		CommandTimeout: "1m0s", CaseTimeout: "2h0m0s",
		SelectedInstancesSHA256: selectedHash, Workers: 1,
	}
}

func writeResumeBundle(t *testing.T, output, instanceID, patch string, identity runIdentity) {
	t.Helper()
	result := executor.CaseResult{
		InstanceID: instanceID,
		ModelPatch: patch,
		Info: executor.CaseInfo{
			RunID: identity.RunID, ObservationCodec: identity.ObservationCodec,
			SourceRevision: identity.SourceRevision, SourceModified: identity.SourceModified,
			BinarySHA256: identity.BinarySHA256, ModelConfigSHA256: identity.ModelConfigSHA256,
			EnvironmentConfigSHA256: identity.EnvironmentConfigSHA256,
			CasesSHA256:             identity.CasesSHA256, CommandTimeout: identity.CommandTimeout,
			CaseTimeout:             identity.CaseTimeout,
			SelectedInstancesSHA256: identity.SelectedInstancesSHA256,
			Workers:                 identity.Workers,
			ExitStatus:              "Submitted",
		},
	}
	if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePersistedPredictions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	want := map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelNameOrPath: "trpc-agent-go/test", ModelPatch: "patch"},
	}
	if err := artifact.WriteJSON(path, want); err != nil {
		t.Fatal(err)
	}
	if err := validatePersistedPredictions(path, want); err != nil {
		t.Fatal(err)
	}
	want["case-b"] = contract.Prediction{InstanceID: "case-b"}
	if err := validatePersistedPredictions(path, want); err == nil {
		t.Fatal("missing persisted prediction was accepted")
	}
}

func TestPersistPredictionsRemovesStaleBoundaryWhenRedoSetIsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "preds.json")
	if err := artifact.WriteJSON(path, map[string]contract.Prediction{
		"case-a": {InstanceID: "case-a", ModelPatch: "stale"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := persistPredictions(path, map[string]contract.Prediction{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("predictions boundary still exists: %v", err)
	}
	if err := validatePersistedPredictions(path, map[string]contract.Prediction{}); err != nil {
		t.Fatal(err)
	}
}

func TestWriteCaseBundleRequiresBothArtifacts(t *testing.T) {
	for _, failSuffix := range []string{".responses.json", ".native.json"} {
		t.Run(failSuffix, func(t *testing.T) {
			result := executor.CaseResult{
				InstanceID: "case-a", ModelPatch: "patch",
				Info:      executor.CaseInfo{ExitStatus: "Submitted"},
				Responses: []*model.Response{{Done: true}},
			}
			writeJSON := func(path string, value any) error {
				if strings.HasSuffix(path, failSuffix) {
					return errors.New("injected write failure")
				}
				return artifact.WriteJSON(path, value)
			}
			if err := writeCaseBundle(t.TempDir(), &result, writeJSON); err == nil {
				t.Fatal("incomplete bundle was accepted")
			}
			if result.Info.ExitStatus != "ArtifactError" || result.Info.ErrorCategory != protocol.ErrorCategoryArtifact {
				t.Fatalf("result = %+v", result)
			}
		})
	}
}

func TestLoadExistingCaseBundleRequiresMatchingCompleteArtifacts(t *testing.T) {
	output := t.TempDir()
	result := executor.CaseResult{
		InstanceID: "case-a", ModelPatch: "patch",
		Info: executor.CaseInfo{ExitStatus: "Submitted"},
	}
	if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
		t.Fatal(err)
	}
	prediction := contract.Prediction{InstanceID: "case-a", ModelPatch: "patch"}
	if _, err := loadExistingCaseBundle(output, "case-a", prediction); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(output, "case-a", "case-a.responses.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := loadExistingCaseBundle(output, "case-a", prediction); err == nil {
		t.Fatal("missing response artifact was accepted")
	}
}

func TestLoadExistingCaseBundleRejectsChangedResponses(t *testing.T) {
	output := t.TempDir()
	result := executor.CaseResult{
		InstanceID: "case-a", ModelPatch: "patch",
		Info:      executor.CaseInfo{ExitStatus: "Submitted"},
		Responses: []*model.Response{{Done: true}},
	}
	if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
		t.Fatal(err)
	}
	responsesPath := filepath.Join(output, "case-a", "case-a.responses.json")
	if err := artifact.WriteJSON(responsesPath, []*model.Response{{Done: false}}); err != nil {
		t.Fatal(err)
	}
	prediction := contract.Prediction{InstanceID: "case-a", ModelPatch: "patch"}
	if _, err := loadExistingCaseBundle(output, "case-a", prediction); err == nil || !strings.Contains(err.Error(), "SHA-256") {
		t.Fatalf("changed response artifact error = %v", err)
	}
}

func TestFrameworkVersionComesFromBuildInfo(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{
		{Path: "example.com/other", Version: "v1.0.0"},
		{Path: frameworkModulePath, Version: "v1.10.2-0.20260801000000-abcdef123456"},
	}}
	if got := frameworkVersionFromBuildInfo(info); got != "v1.10.2-0.20260801000000-abcdef123456" {
		t.Fatalf("framework version = %q", got)
	}
	info.Deps[1].Replace = &debug.Module{Path: frameworkModulePath, Version: "v1.11.0"}
	if got := frameworkVersionFromBuildInfo(info); got != "v1.11.0" {
		t.Fatalf("replaced framework version = %q", got)
	}
}

func TestModelManifestConfigExcludesEndpointAndHeaderValues(t *testing.T) {
	cfg := modelconfig.EnvConfig{
		"MODEL_NAME": "model", "OPENAI_API_KEY": "secret", "OPENAI_BASE_URL": "https://private.invalid",
		modelconfig.HTTPHeaderPrefix + "X-Custom": "header-secret",
	}
	got := modelManifestConfig(cfg)
	if got["MODEL_NAME"] != "model" || got["HTTP_HEADER_COUNT"] != "1" {
		t.Fatalf("model manifest config = %#v", got)
	}
	for _, value := range got {
		if value == "secret" || value == "header-secret" || value == "https://private.invalid" || value == "X-Custom" {
			t.Fatalf("model manifest leaked sensitive value: %#v", got)
		}
	}
}

func TestValidateArtifactName(t *testing.T) {
	for _, value := range []string{"run-1", "django__django-12345", "a.b_c-d"} {
		if err := validateArtifactName("run id", value); err != nil {
			t.Fatalf("valid name %q: %v", value, err)
		}
	}
	for _, value := range []string{"", "../escape", "contains space", "/absolute"} {
		if err := validateArtifactName("run id", value); err == nil {
			t.Fatalf("invalid name %q succeeded", value)
		}
	}
}
