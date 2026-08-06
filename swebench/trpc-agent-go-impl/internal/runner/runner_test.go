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
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
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

func TestPrepareResumeCleanRoomPreStartFailureRequiresRedo(t *testing.T) {
	selectedCase := contract.Case{
		InstanceID: "org__repo-123",
		Repo:       "org/repo",
		BaseCommit: strings.Repeat("1", 40),
	}
	selected := []contract.Case{selectedCase}
	identity := testCleanRoomIdentity(t, true, selectedCase.InstanceID)

	for _, tc := range []struct {
		name string
		redo bool
	}{
		{name: "ordinary resume rejects missing success attestations", redo: false},
		{name: "explicit redo accepts retryable pre-start failure", redo: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			output := t.TempDir()
			path := filepath.Join(output, "preds.json")
			if err := artifact.WriteJSON(path, map[string]contract.Prediction{
				selectedCase.InstanceID: {InstanceID: selectedCase.InstanceID},
			}); err != nil {
				t.Fatal(err)
			}
			writeRetryablePreStartBundle(t, output, selectedCase, identity)

			preds, pending, skipped, err := prepareResume(output, path, selected, tc.redo, identity)
			if !tc.redo {
				if err == nil || !strings.Contains(err.Error(), "verified base commit") {
					t.Fatalf("error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(preds) != 0 || !reflect.DeepEqual(pending, selected) || len(skipped) != 0 {
				t.Fatalf("preds=%#v pending=%#v skipped=%#v", preds, pending, skipped)
			}
		})
	}
}

func TestPrepareResumeRedoPreStartExceptionRemainsFailClosed(t *testing.T) {
	selectedCase := contract.Case{
		InstanceID: "org__repo-123",
		Repo:       "org/repo",
		BaseCommit: strings.Repeat("1", 40),
	}
	identity := testCleanRoomIdentity(t, true, selectedCase.InstanceID)
	tests := []struct {
		name      string
		change    func(*executor.CaseResult)
		wantError string
	}{
		{
			name: "immutable identity mismatch",
			change: func(result *executor.CaseResult) {
				result.Info.ModelConfigSHA256 = strings.Repeat("9", 64)
			},
			wantError: "model config hash",
		},
		{
			name: "non-retryable environment failure",
			change: func(result *executor.CaseResult) {
				result.Info.Retryable = false
			},
			wantError: "verified base commit",
		},
		{
			name: "model activity is not pre-start",
			change: func(result *executor.CaseResult) {
				result.LLMCalls = 1
			},
			wantError: "verified base commit",
		},
		{
			name: "prompt usage is not pre-start",
			change: func(result *executor.CaseResult) {
				result.Usage.PromptTokens = 1
			},
			wantError: "verified base commit",
		},
		{
			name: "cached usage is not pre-start",
			change: func(result *executor.CaseResult) {
				result.Usage.PromptTokensDetails.CachedTokens = 1
			},
			wantError: "verified base commit",
		},
		{
			name: "timing usage is not pre-start",
			change: func(result *executor.CaseResult) {
				result.Usage.TimingInfo = &model.TimingInfo{FirstTokenDuration: time.Nanosecond}
			},
			wantError: "verified base commit",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := t.TempDir()
			path := filepath.Join(output, "preds.json")
			if err := artifact.WriteJSON(path, map[string]contract.Prediction{
				selectedCase.InstanceID: {InstanceID: selectedCase.InstanceID},
			}); err != nil {
				t.Fatal(err)
			}
			result := retryablePreStartResult(t, selectedCase, identity)
			tc.change(&result)
			if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
				t.Fatal(err)
			}

			if _, _, _, err := prepareResume(
				output,
				path,
				[]contract.Case{selectedCase},
				true,
				identity,
			); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
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

func TestPrepareResumeRejectsCleanRoomBoundaryMismatches(t *testing.T) {
	selectedCase := contract.Case{
		InstanceID: "org__repo-123",
		Repo:       "org/repo",
		BaseCommit: strings.Repeat("1", 40),
	}
	cleanIdentity := testCleanRoomIdentity(t, true, selectedCase.InstanceID)
	onlineIdentity := testRunIdentity(t, selectedCase.InstanceID)

	tests := []struct {
		name      string
		actual    runIdentity
		change    func(*runIdentity)
		wantError string
	}{
		{
			name:      "online result cannot resume clean room",
			actual:    onlineIdentity,
			wantError: "clean_room=false, want true",
			change: func(expected *runIdentity) {
				*expected = cleanIdentity
			},
		},
		{
			name:      "clean-room result cannot resume online",
			actual:    cleanIdentity,
			wantError: "clean_room=true, want false",
			change: func(expected *runIdentity) {
				expected.CleanRoom = false
				expected.CleanRoomPolicySHA256 = ""
				expected.OfflineAssetsSHA256 = ""
				expected.ImageSetSHA256 = ""
				expected.DockerImages = nil
			},
		},
		{
			name:      "policy",
			actual:    cleanIdentity,
			wantError: "clean-room policy hash",
			change: func(expected *runIdentity) {
				expected.CleanRoomPolicySHA256 = strings.Repeat("6", 64)
			},
		},
		{
			name:      "assets",
			actual:    cleanIdentity,
			wantError: "offline assets hash",
			change: func(expected *runIdentity) {
				expected.OfflineAssetsSHA256 = strings.Repeat("7", 64)
			},
		},
		{
			name:      "assets cannot disappear",
			actual:    cleanIdentity,
			wantError: "offline assets hash",
			change: func(expected *runIdentity) {
				expected.OfflineAssetsSHA256 = ""
			},
		},
		{
			name:      "images",
			actual:    cleanIdentity,
			wantError: "image-set hash",
			change: func(expected *runIdentity) {
				reference := sweenv.ImageForInstance(selectedCase.InstanceID)
				expected.DockerImages = map[string]sweenv.ImageIdentity{
					reference: {Reference: reference, ID: "sha256:" + strings.Repeat("8", 64)},
				}
				var err error
				expected.ImageSetSHA256, err = sweenv.ImageSetSHA256(expected.DockerImages)
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			output := t.TempDir()
			path := filepath.Join(output, "preds.json")
			if err := artifact.WriteJSON(path, map[string]contract.Prediction{
				selectedCase.InstanceID: {InstanceID: selectedCase.InstanceID, ModelPatch: "patch"},
			}); err != nil {
				t.Fatal(err)
			}
			writeResumeBundleForCase(t, output, selectedCase, "patch", tc.actual)
			expected := tc.actual
			tc.change(&expected)
			if _, _, _, err := prepareResume(
				output,
				path,
				[]contract.Case{selectedCase},
				false,
				expected,
			); err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestValidateResumeResultRejectsDefaultModeCleanRoomResidue(t *testing.T) {
	selectedCase := contract.Case{InstanceID: "org__repo-123", Repo: "org/repo"}
	identity := testRunIdentity(t, selectedCase.InstanceID)
	result := resumeCaseResult(t, selectedCase, "patch", identity)
	result.Info.VerifiedBaseCommit = strings.Repeat("1", 40)
	result.Info.EnvironmentProvenance = &sweenv.Provenance{}
	if err := validateResumeResult(selectedCase, result, identity); err == nil ||
		!strings.Contains(err.Error(), "clean-room case provenance") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateRunIdentityCleanRoomRequirements(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		if err := validateRunIdentity(testCleanRoomIdentity(t, true, "case-a")); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("online rejects provenance", func(t *testing.T) {
		identity := testRunIdentity(t, "case-a")
		identity.CleanRoomPolicySHA256 = strings.Repeat("1", 64)
		if err := validateRunIdentity(identity); err == nil ||
			!strings.Contains(err.Error(), "non-clean-room run identity") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("image map must match image-set hash", func(t *testing.T) {
		identity := testCleanRoomIdentity(t, false, "case-a")
		identity.DockerImages[sweenv.ImageForInstance("case-a")] = sweenv.ImageIdentity{
			Reference: sweenv.ImageForInstance("case-a"),
			ID:        "sha256:" + strings.Repeat("9", 64),
		}
		if err := validateRunIdentity(identity); err == nil ||
			!strings.Contains(err.Error(), "Docker images hash") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestValidateResumeResultRequiresExactAuxiliaryImageRoles(t *testing.T) {
	selectedCase := contract.Case{
		InstanceID: "psf__requests-2317",
		Repo:       "psf/requests",
		BaseCommit: strings.Repeat("1", 40),
	}
	identity := testCleanRoomIdentity(t, true, selectedCase.InstanceID)

	t.Run("exact roles", func(t *testing.T) {
		result := resumeCaseResult(t, selectedCase, "patch", identity)
		if err := validateResumeResult(selectedCase, result, identity); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing httpbin", func(t *testing.T) {
		result := resumeCaseResult(t, selectedCase, "patch", identity)
		delete(result.Info.EnvironmentProvenance.AuxiliaryImages, "httpbin")
		if err := validateResumeResult(selectedCase, result, identity); err == nil ||
			!strings.Contains(err.Error(), "auxiliary image roles") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("wrong network helper", func(t *testing.T) {
		result := resumeCaseResult(t, selectedCase, "patch", identity)
		result.Info.EnvironmentProvenance.AuxiliaryImages["network-helper"] =
			identity.DockerImages[offlineHTTPBinImageReference]
		if err := validateResumeResult(selectedCase, result, identity); err == nil ||
			!strings.Contains(err.Error(), "network-helper image provenance") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("extra role", func(t *testing.T) {
		result := resumeCaseResult(t, selectedCase, "patch", identity)
		result.Info.EnvironmentProvenance.AuxiliaryImages["unexpected"] =
			result.Info.EnvironmentProvenance.Testbed
		if err := validateResumeResult(selectedCase, result, identity); err == nil ||
			!strings.Contains(err.Error(), "auxiliary image roles") {
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

func testCleanRoomIdentity(t *testing.T, withAssets bool, instanceIDs ...string) runIdentity {
	t.Helper()
	identity := testRunIdentity(t, instanceIDs...)
	identity.CleanRoom = true
	identity.CleanRoomPolicySHA256 = strings.Repeat("f", 64)
	if withAssets {
		identity.OfflineAssetsSHA256 = strings.Repeat("0", 64)
	}
	identity.DockerImages = make(map[string]sweenv.ImageIdentity, len(instanceIDs))
	for _, instanceID := range instanceIDs {
		reference := sweenv.ImageForInstance(instanceID)
		identity.DockerImages[reference] = sweenv.ImageIdentity{
			Reference: reference,
			ID:        "sha256:" + strings.Repeat("2", 64),
		}
		if strings.HasPrefix(instanceID, "psf__requests-") {
			identity.DockerImages[offlineHTTPBinImageReference] = sweenv.ImageIdentity{
				Reference: offlineHTTPBinImageReference,
				ID:        "sha256:" + strings.Repeat("3", 64),
			}
		}
	}
	var err error
	identity.ImageSetSHA256, err = sweenv.ImageSetSHA256(identity.DockerImages)
	if err != nil {
		t.Fatal(err)
	}
	return identity
}

func writeResumeBundle(t *testing.T, output, instanceID, patch string, identity runIdentity) {
	t.Helper()
	writeResumeBundleForCase(t, output, contract.Case{InstanceID: instanceID}, patch, identity)
}

func writeResumeBundleForCase(
	t *testing.T,
	output string,
	selectedCase contract.Case,
	patch string,
	identity runIdentity,
) {
	t.Helper()
	result := resumeCaseResult(t, selectedCase, patch, identity)
	if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
		t.Fatal(err)
	}
}

func writeRetryablePreStartBundle(
	t *testing.T,
	output string,
	selectedCase contract.Case,
	identity runIdentity,
) {
	t.Helper()
	result := retryablePreStartResult(t, selectedCase, identity)
	if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
		t.Fatal(err)
	}
}

func retryablePreStartResult(
	t *testing.T,
	selectedCase contract.Case,
	identity runIdentity,
) executor.CaseResult {
	t.Helper()
	result := resumeCaseResult(t, selectedCase, "", identity)
	result.Info.ExitStatus = "Error"
	result.Info.Error = "clean-room setup failed"
	result.Info.ErrorCategory = protocol.ErrorCategoryEnvironment
	result.Info.Retryable = true
	result.Info.VerifiedBaseCommit = ""
	result.Info.EnvironmentProvenance = nil
	return result
}

func resumeCaseResult(
	t *testing.T,
	selectedCase contract.Case,
	patch string,
	identity runIdentity,
) executor.CaseResult {
	t.Helper()
	result := executor.CaseResult{
		InstanceID: selectedCase.InstanceID,
		ModelPatch: patch,
		Info: executor.CaseInfo{
			RunID: identity.RunID, ObservationCodec: identity.ObservationCodec,
			SourceRevision: identity.SourceRevision, SourceModified: identity.SourceModified,
			BinarySHA256: identity.BinarySHA256, ModelConfigSHA256: identity.ModelConfigSHA256,
			EnvironmentConfigSHA256: identity.EnvironmentConfigSHA256,
			CasesSHA256:             identity.CasesSHA256, CommandTimeout: identity.CommandTimeout,
			CaseTimeout:             identity.CaseTimeout,
			SelectedInstancesSHA256: identity.SelectedInstancesSHA256,
			CleanRoom:               identity.CleanRoom,
			CleanRoomPolicySHA256:   identity.CleanRoomPolicySHA256,
			OfflineAssetsSHA256:     identity.OfflineAssetsSHA256,
			ImageSetSHA256:          identity.ImageSetSHA256,
			Repo:                    selectedCase.Repo,
			BaseCommit:              selectedCase.BaseCommit,
			Workers:                 identity.Workers,
			ExitStatus:              "Submitted",
		},
	}
	if identity.CleanRoom {
		result.Info.VerifiedBaseCommit = selectedCase.BaseCommit
		testbed := identity.DockerImages[sweenv.ImageForInstance(selectedCase.InstanceID)]
		auxiliary, err := expectedAuxiliaryImages(selectedCase.InstanceID, identity.DockerImages, testbed)
		if err != nil {
			t.Fatal(err)
		}
		result.Info.EnvironmentProvenance = &sweenv.Provenance{
			Testbed:         testbed,
			AuxiliaryImages: auxiliary,
		}
	}
	return result
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
