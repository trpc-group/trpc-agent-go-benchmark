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
	"context"
	"encoding/json"
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
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/observation"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/executor"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/protocol"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
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

func TestToolLoopWarningFlagDefaultsOffAndAcceptsExplicitValues(t *testing.T) {
	for _, tc := range []struct {
		name string
		arg  string
	}{
		{name: "default off"},
		{name: "explicit off", arg: "-tool-loop-warning=false"},
		{name: "explicit on", arg: "-tool-loop-warning=true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{
				"trpc-agent-go-impl",
				"-run-id=test",
				"-model-config=unused",
				"-cases=" + filepath.Join(t.TempDir(), "missing.jsonl"),
			}
			if tc.arg != "" {
				args = append(args, tc.arg)
			}
			err := Run(args)
			if err == nil || !strings.Contains(err.Error(), "hash cases") {
				t.Fatalf("error = %v, want flag parsing to reach cases hashing", err)
			}
		})
	}
}

func TestWorkspaceRetrievalFlagsAreOptInAndFailClosed(t *testing.T) {
	baseArgs := func(t *testing.T) []string {
		t.Helper()
		return []string{
			"trpc-agent-go-impl",
			"-run-id=test",
			"-model-config=unused",
			"-cases=" + filepath.Join(t.TempDir(), "missing.jsonl"),
		}
	}
	for _, tc := range []struct {
		name      string
		arguments []string
		want      string
	}{
		{
			name:      "embedding config without retrieval",
			arguments: []string{"-embedding-config=unused"},
			want:      "-embedding-config requires -code-search=true",
		},
		{
			name:      "representation without retrieval",
			arguments: []string{"-workspace-representation=ast-structured"},
			want:      "-workspace-representation=ast-structured requires -code-search=true",
		},
		{
			name:      "invalid representation",
			arguments: []string{"-code-search=true", "-workspace-representation=unknown"},
			want:      "unsupported workspace representation",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := Run(append(baseArgs(t), tc.arguments...))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}

	for _, arguments := range [][]string{
		{"-workspace-preload=false"},
		{"-code-search=true"},
		{"-code-search=true", "-workspace-preload=false"},
		{"-code-search=true", "-workspace-representation=ast-code"},
	} {
		err := Run(append(baseArgs(t), arguments...))
		if err == nil || !strings.Contains(err.Error(), "hash cases") {
			t.Fatalf("arguments=%v error=%v, want flag parsing to reach cases hashing", arguments, err)
		}
	}
}

func TestManifestOmitsDisabledWorkspaceRetrievalIdentity(t *testing.T) {
	payload, err := json.Marshal(manifest{})
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{
		"code_search",
		"code_search_tool_order",
		"code_search_invocation_dedup",
		"workspace_preload",
		"workspace_representation",
		"workspace_representation_schema",
		"workspace_representation_sha256",
		"embedding_config_sha256",
		"embedding_config",
		"embedding_cache",
	} {
		if strings.Contains(string(payload), `"`+field+`"`) {
			t.Fatalf("disabled manifest unexpectedly contains %q: %s", field, payload)
		}
	}

	preload := false
	payload, err = json.Marshal(manifest{
		CodeSearch:                true,
		CodeSearchToolOrder:       tagagent.CodeSearchProviderToolOrder,
		CodeSearchInvocationDedup: tagagent.CodeSearchInvocationDedup,
		WorkspacePreload:          &preload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"workspace_preload":false`) {
		t.Fatalf("enabled retrieval manifest lost explicit preload=false: %s", payload)
	}
	for _, identity := range []string{
		`"code_search_tool_order":"bash,code_search"`,
		`"code_search_invocation_dedup":"disabled"`,
	} {
		if !strings.Contains(string(payload), identity) {
			t.Fatalf("enabled retrieval manifest lost %s: %s", identity, payload)
		}
	}
}

func TestCaseInfoOmitsDisabledWorkspacePreloadAndKeepsExplicitFalseWhenEnabled(t *testing.T) {
	payload, err := json.Marshal(executor.CaseInfo{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"workspace_preload"`) {
		t.Fatalf("disabled case info unexpectedly contains workspace_preload: %s", payload)
	}
	preload := false
	payload, err = json.Marshal(executor.CaseInfo{CodeSearch: true, WorkspacePreload: &preload})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(payload), `"workspace_preload":false`) {
		t.Fatalf("enabled case info lost explicit preload=false: %s", payload)
	}
}

func TestWorkspaceEmbeddingManifestIsRedactedAndHashed(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "private-cache")
	path := filepath.Join(t.TempDir(), "embedding.yaml")
	contents := strings.Join([]string{
		"embedding:",
		"  provider: openai",
		"  api_base: https://internal.example.invalid/v1",
		"  api_key: private-key",
		"  model: embed-model",
		"  dimensions: 4",
		"retrieval:",
		"  mode: hybrid",
		"cache:",
		"  enabled: true",
		"  directory: " + directory,
		"  model_fingerprint: public-model-revision",
	}, "\n") + "\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	hash, err := fileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if !isHexIdentifier(hash, 64) {
		t.Fatalf("embedding config hash = %q", hash)
	}
	cfg, err := embeddingconfig.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(manifest{
		EmbeddingConfigSHA256: hash,
		EmbeddingConfig:       cfg.Redacted(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"https://internal.example.invalid/v1",
		"private-key",
		directory,
	} {
		if strings.Contains(string(payload), secret) {
			t.Fatalf("embedding manifest leaked %q: %s", secret, payload)
		}
	}
	if !strings.Contains(string(payload), `"embedding_config_sha256":"`+hash+`"`) {
		t.Fatalf("embedding manifest omitted config hash: %s", payload)
	}
}

func TestAgentProtocolRecordsToolLoopWarning(t *testing.T) {
	for _, tc := range []struct {
		name            string
		codec           string
		cleanRoom       bool
		toolLoopWarning bool
		want            string
	}{
		{
			name: "default off", codec: "xml",
			want: "mini-swe-agent-v2.1-on-trpc-agent-go",
		},
		{
			name: "warning only", codec: "xml", toolLoopWarning: true,
			want: "mini-swe-agent-v2.1-on-trpc-agent-go+tool-loop-warning-v1",
		},
		{
			name: "clean room warning", codec: "json", cleanRoom: true, toolLoopWarning: true,
			want: "mini-swe-agent-v2.1-on-trpc-agent-go+codec-json+clean-room-v1+tool-loop-warning-v1",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentProtocol(
				observation.ObservationCodec(tc.codec),
				tc.cleanRoom,
				tc.toolLoopWarning,
			); got != tc.want {
				t.Fatalf("agentProtocol() = %q, want %q", got, tc.want)
			}
		})
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

func TestPreservePredictionsForRedoKeepsExactImmutableBoundary(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "preds.json")
	original := []byte("{\n  \"case-a\": {\"instance_id\": \"case-a\", \"model_patch\": \"patch\"}\n}\n")
	if err := os.WriteFile(path, original, 0o644); err != nil {
		t.Fatal(err)
	}

	backup, err := preservePredictionsForRedo(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if backup == "" || backup == path {
		t.Fatalf("backup path = %q", backup)
	}
	got, err := os.ReadFile(backup)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup changed exact predictions boundary:\ngot=%q\nwant=%q", got, original)
	}

	again, err := preservePredictionsForRedo(path, true)
	if err != nil {
		t.Fatal(err)
	}
	if again != backup {
		t.Fatalf("idempotent backup = %q, want %q", again, backup)
	}
	if disabled, err := preservePredictionsForRedo(path, false); err != nil || disabled != "" {
		t.Fatalf("disabled backup = %q, %v", disabled, err)
	}
}

func TestDispatchCasesStopsAtCancellation(t *testing.T) {
	pending := []contract.Case{{InstanceID: "case-a"}, {InstanceID: "case-b"}}

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	canceledJobs := make(chan contract.Case, len(pending))
	if got := dispatchCases(canceled, canceledJobs, pending); got != 0 || len(canceledJobs) != 0 {
		t.Fatalf("canceled dispatch = %d jobs, buffered=%d", got, len(canceledJobs))
	}

	jobs := make(chan contract.Case, len(pending))
	if got := dispatchCases(context.Background(), jobs, pending); got != len(pending) || len(jobs) != len(pending) {
		t.Fatalf("complete dispatch = %d jobs, buffered=%d", got, len(jobs))
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
		{
			name: "warning telemetry is not pre-start",
			change: func(result *executor.CaseResult) {
				first := 1
				result.ToolLoopWarningCount = 1
				result.FirstToolLoopWarningLLMCall = &first
				result.ToolLoopWarningLLMCalls = []int{1}
			},
			wantError: "tool_loop_warning=false",
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
	t.Run("tool loop warning mismatch", func(t *testing.T) {
		output := t.TempDir()
		path := filepath.Join(output, "preds.json")
		if err := artifact.WriteJSON(path, map[string]contract.Prediction{
			"case-a": {InstanceID: "case-a", ModelPatch: "patch"},
		}); err != nil {
			t.Fatal(err)
		}
		writeResumeBundle(t, output, "case-a", "patch", identity)
		changed := identity
		changed.ToolLoopWarning = true
		if _, _, _, err := prepareResume(output, path, selected, false, changed); err == nil ||
			!strings.Contains(err.Error(), "tool_loop_warning") {
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

func TestValidateRunIdentityWorkspaceRetrievalRequirements(t *testing.T) {
	identity := testWorkspaceIdentity(t, "case-a")
	if err := validateRunIdentity(identity); err != nil {
		t.Fatal(err)
	}

	t.Run("representation hash", func(t *testing.T) {
		changed := identity
		changed.RepresentationSHA256 = strings.Repeat("1", 64)
		if err := validateRunIdentity(changed); err == nil ||
			!strings.Contains(err.Error(), "workspace representation hash") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("embedding hash", func(t *testing.T) {
		changed := identity
		changed.EmbeddingConfigSHA256 = "not-a-hash"
		if err := validateRunIdentity(changed); err == nil ||
			!strings.Contains(err.Error(), "embedding config hash") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("disabled retrieval rejects residue", func(t *testing.T) {
		changed := identity
		changed.CodeSearch = false
		if err := validateRunIdentity(changed); err == nil ||
			!strings.Contains(err.Error(), "code_search=false") {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestValidateResumeResultRequiresExactWorkspaceRetrievalIdentity(t *testing.T) {
	selectedCase := contract.Case{InstanceID: "case-a"}
	identity := testWorkspaceIdentity(t, selectedCase.InstanceID)
	valid := resumeCaseResult(t, selectedCase, "patch", identity)
	if err := validateResumeResult(selectedCase, valid, identity); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		name      string
		change    func(*executor.CaseResult)
		wantError string
	}{
		{
			name:      "code search",
			change:    func(result *executor.CaseResult) { result.Info.CodeSearch = false },
			wantError: "code_search=false, want true",
		},
		{
			name: "preload",
			change: func(result *executor.CaseResult) {
				preload := false
				result.Info.WorkspacePreload = &preload
			},
			wantError: "workspace_preload=false, want true",
		},
		{
			name:      "representation",
			change:    func(result *executor.CaseResult) { result.Info.WorkspaceRepresentation = "fixed-raw" },
			wantError: "workspace representation",
		},
		{
			name:      "representation hash",
			change:    func(result *executor.CaseResult) { result.Info.RepresentationSHA256 = strings.Repeat("1", 64) },
			wantError: "workspace representation hash",
		},
		{
			name:      "embedding config hash",
			change:    func(result *executor.CaseResult) { result.Info.EmbeddingConfigSHA256 = strings.Repeat("2", 64) },
			wantError: "embedding config hash",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := valid
			tc.change(&result)
			if err := validateResumeResult(selectedCase, result, identity); err == nil ||
				!strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestValidateResumeResultRequiresCompleteWorkspaceRetrievalEvidence(t *testing.T) {
	selectedCase := contract.Case{InstanceID: "case-a"}
	identity := testWorkspaceIdentity(t, selectedCase.InstanceID)
	newResult := func() executor.CaseResult {
		result := resumeCaseResult(t, selectedCase, "patch", identity)
		raw := json.RawMessage("{\n  \"documents\": []\n}")
		compact := []byte(`{"documents":[]}`)
		observation := "<code_search_result></code_search_result>"
		result.CodeSearchCalls = 1
		result.CodeSearchResultBytes = len(compact)
		result.CodeSearchObservationBytes = len(observation)
		result.CodeSearchRawResults = []json.RawMessage{raw}
		result.RetrievalTrace = []tagagent.RetrievalTraceEntry{{
			Call:              1,
			ToolCallID:        "call-1",
			Query:             "needle",
			Status:            "success",
			ArgumentsSHA256:   strings.Repeat("a", 64),
			ResultSHA256:      tagagent.DigestBytes(compact),
			ObservationSHA256: tagagent.DigestBytes([]byte(observation)),
			ResultBytes:       len(compact),
			ObservationBytes:  len(observation),
			Documents:         []tagagent.RetrievalTraceDocument{},
		}}
		return result
	}

	if err := validateResumeResult(selectedCase, newResult(), identity); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		change    func(*executor.CaseResult)
		wantError string
	}{
		{
			name:      "missing workspace index",
			change:    func(result *executor.CaseResult) { result.WorkspaceIndex = nil },
			wantError: "missing workspace_index",
		},
		{
			name:      "trace count",
			change:    func(result *executor.CaseResult) { result.RetrievalTrace = nil },
			wantError: "evidence counts",
		},
		{
			name: "raw result digest",
			change: func(result *executor.CaseResult) {
				result.CodeSearchRawResults[0] = json.RawMessage(`{"documents":[{"id":"changed"}]}`)
			},
			wantError: "does not match raw result",
		},
		{
			name:      "error count",
			change:    func(result *executor.CaseResult) { result.CodeSearchErrors = 1 },
			wantError: "retrieval errors",
		},
		{
			name: "workspace identity",
			change: func(result *executor.CaseResult) {
				result.WorkspaceIndex.RepresentationSHA256 = strings.Repeat("f", 64)
			},
			wantError: "representation identity",
		},
		{
			name:      "embedding telemetry",
			change:    func(result *executor.CaseResult) { result.Embedding = nil },
			wantError: "embedding telemetry",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := newResult()
			tc.change(&result)
			if err := validateResumeResult(selectedCase, result, identity); err == nil ||
				!strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestValidateResumeResultRejectsDisabledWorkspaceRetrievalResidue(t *testing.T) {
	selectedCase := contract.Case{InstanceID: "case-a"}
	identity := testRunIdentity(t, selectedCase.InstanceID)
	result := resumeCaseResult(t, selectedCase, "patch", identity)
	result.CodeSearchCalls = 1
	if err := validateResumeResult(selectedCase, result, identity); err == nil ||
		!strings.Contains(err.Error(), "code_search=false") {
		t.Fatalf("error = %v", err)
	}
}

func TestValidateToolLoopWarningTelemetry(t *testing.T) {
	first := 2
	base := executor.CaseResult{
		LLMCalls:                    4,
		ToolLoopWarningCount:        2,
		FirstToolLoopWarningLLMCall: &first,
		ToolLoopWarningLLMCalls:     []int{2, 4},
		Info:                        executor.CaseInfo{ToolLoopWarning: true},
	}
	if err := validateToolLoopWarningTelemetry("case-a", base); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(*executor.CaseResult)
		want   string
	}{
		{name: "disabled with telemetry", change: func(result *executor.CaseResult) {
			result.Info.ToolLoopWarning = false
		}, want: "tool_loop_warning=false"},
		{name: "count mismatch", change: func(result *executor.CaseResult) {
			result.ToolLoopWarningCount = 1
		}, want: "want count"},
		{name: "first mismatch", change: func(result *executor.CaseResult) {
			value := 3
			result.FirstToolLoopWarningLLMCall = &value
		}, want: "inconsistent first"},
		{name: "unsorted", change: func(result *executor.CaseResult) {
			result.ToolLoopWarningLLMCalls = []int{2, 2}
		}, want: "strictly increasing"},
		{name: "beyond calls", change: func(result *executor.CaseResult) {
			result.ToolLoopWarningLLMCalls = []int{2, 5}
		}, want: "beyond llm_calls"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := base
			result.ToolLoopWarningLLMCalls = append([]int(nil), base.ToolLoopWarningLLMCalls...)
			tc.change(&result)
			if err := validateToolLoopWarningTelemetry("case-a", result); err == nil ||
				!strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want %q", err, tc.want)
			}
		})
	}

	if err := validateToolLoopWarningTelemetry("legacy", executor.CaseResult{}); err != nil {
		t.Fatalf("legacy missing=false/count=0 telemetry rejected: %v", err)
	}
}

func TestProgressAndAggregateIncludeSkippedWarningTelemetry(t *testing.T) {
	first := 3
	results := map[string]executor.CaseResult{
		"fresh": {
			Info:     executor.CaseInfo{ExitStatus: "Submitted"},
			LLMCalls: 2, ToolCalls: 1,
		},
		"skipped": {
			Info:       executor.CaseInfo{ExitStatus: "Submitted", ToolLoopWarning: true},
			ModelPatch: "patch", DurationMS: 10, LLMCalls: 5, ToolCalls: 4,
			ToolLoopWarningCount: 2, FirstToolLoopWarningLLMCall: &first,
			ToolLoopWarningLLMCalls: []int{3, 5},
		},
	}
	progress := progressCaseFromResult(results["skipped"])
	if progress.ToolLoopWarningCount != 2 || progress.LLMCalls != 5 || progress.PatchBytes != 5 {
		t.Fatalf("progress = %+v", progress)
	}
	aggregate := aggregateResults(results)
	if aggregate.ToolLoopWarningCount != 2 || aggregate.ToolLoopWarningCaseCount != 1 ||
		aggregate.LLMCalls != 7 || aggregate.ToolCalls != 5 || aggregate.ExitStatusCounts["Submitted"] != 2 {
		t.Fatalf("aggregate = %+v", aggregate)
	}
}

func TestAggregateResultsIncludesEmbeddingAndCacheMetrics(t *testing.T) {
	results := map[string]executor.CaseResult{
		"case-a": {
			Info: executor.CaseInfo{ExitStatus: "Submitted"},
			Embedding: &embeddingconfig.Metrics{
				Requests: 1, BatchRequests: 1, Inputs: 3,
				PromptTokens: 10, TotalTokens: 10, DurationMS: 20,
			},
			EmbeddingCache: &embeddingcache.Metrics{
				Requests: 3, Hits: 2, Misses: 1, BytesRead: 40,
			},
		},
		"case-b": {
			Info: executor.CaseInfo{ExitStatus: "Submitted"},
			Embedding: &embeddingconfig.Metrics{
				Requests: 2, Inputs: 2, Errors: 1,
				PromptTokens: 7, TotalTokens: 7, DurationMS: 9,
			},
			EmbeddingCache: &embeddingcache.Metrics{
				Requests: 2, Hits: 1, Misses: 1, BytesWritten: 64,
			},
		},
	}
	aggregate := aggregateResults(results)
	if !aggregate.HasEmbedding || aggregate.Embedding.Requests != 3 ||
		aggregate.Embedding.Inputs != 5 || aggregate.Embedding.Errors != 1 ||
		aggregate.Embedding.PromptTokens != 17 || aggregate.Embedding.DurationMS != 29 {
		t.Fatalf("embedding aggregate = %#v", aggregate.Embedding)
	}
	if !aggregate.HasEmbeddingCache || aggregate.EmbeddingCache.Requests != 5 ||
		aggregate.EmbeddingCache.Hits != 3 || aggregate.EmbeddingCache.Misses != 2 ||
		aggregate.EmbeddingCache.BytesRead != 40 || aggregate.EmbeddingCache.BytesWritten != 64 {
		t.Fatalf("embedding cache aggregate = %#v", aggregate.EmbeddingCache)
	}
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

func testWorkspaceIdentity(t *testing.T, instanceIDs ...string) runIdentity {
	t.Helper()
	identity := testRunIdentity(t, instanceIDs...)
	representation := tagagent.WorkspaceRepresentationASTStructured
	identity.CodeSearch = true
	identity.CodeSearchToolOrder = tagagent.CodeSearchProviderToolOrder
	identity.CodeSearchInvocationDedup = tagagent.CodeSearchInvocationDedup
	identity.WorkspacePreload = true
	identity.WorkspaceRepresentation = string(representation)
	identity.RepresentationSHA256 = tagagent.WorkspaceRepresentationSHA256(representation)
	identity.EmbeddingConfigSHA256 = strings.Repeat("0", 64)
	return identity
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
	var workspacePreload *bool
	if identity.CodeSearch {
		preload := identity.WorkspacePreload
		workspacePreload = &preload
	}
	result := executor.CaseResult{
		InstanceID: selectedCase.InstanceID,
		ModelPatch: patch,
		Info: executor.CaseInfo{
			RunID: identity.RunID, ObservationCodec: identity.ObservationCodec,
			SourceRevision: identity.SourceRevision, SourceModified: identity.SourceModified,
			BinarySHA256: identity.BinarySHA256, ModelConfigSHA256: identity.ModelConfigSHA256,
			EnvironmentConfigSHA256: identity.EnvironmentConfigSHA256,
			CasesSHA256:             identity.CasesSHA256, CommandTimeout: identity.CommandTimeout,
			CaseTimeout:               identity.CaseTimeout,
			SelectedInstancesSHA256:   identity.SelectedInstancesSHA256,
			CleanRoom:                 identity.CleanRoom,
			ToolLoopWarning:           identity.ToolLoopWarning,
			CodeSearch:                identity.CodeSearch,
			CodeSearchToolOrder:       identity.CodeSearchToolOrder,
			CodeSearchInvocationDedup: identity.CodeSearchInvocationDedup,
			WorkspacePreload:          workspacePreload,
			WorkspaceRepresentation:   identity.WorkspaceRepresentation,
			RepresentationSHA256:      identity.RepresentationSHA256,
			EmbeddingConfigSHA256:     identity.EmbeddingConfigSHA256,
			CleanRoomPolicySHA256:     identity.CleanRoomPolicySHA256,
			OfflineAssetsSHA256:       identity.OfflineAssetsSHA256,
			ImageSetSHA256:            identity.ImageSetSHA256,
			Repo:                      selectedCase.Repo,
			BaseCommit:                selectedCase.BaseCommit,
			Workers:                   identity.Workers,
			ExitStatus:                "Submitted",
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
	if identity.CodeSearch {
		representation := tagagent.WorkspaceRepresentation(identity.WorkspaceRepresentation)
		result.WorkspaceIndex = &tagagent.WorkspaceIndexStats{
			Representation:        identity.WorkspaceRepresentation,
			RepresentationSchema:  tagagent.WorkspaceRepresentationSchema(representation),
			RepresentationSHA256:  identity.RepresentationSHA256,
			EligibleFileSetSHA256: strings.Repeat("1", 64),
			EligibleContentSHA256: strings.Repeat("2", 64),
			IndexedFileSetSHA256:  strings.Repeat("3", 64),
			DocumentSetSHA256:     strings.Repeat("4", 64),
			PreloadInjected:       identity.WorkspacePreload,
			RetrievalMode:         "hybrid",
			InvocationDedup:       identity.CodeSearchInvocationDedup,
		}
		if identity.EmbeddingConfigSHA256 != "" {
			result.Embedding = &embeddingconfig.Metrics{}
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

func TestLoadExistingCaseBundleRequiresWarningTelemetryFields(t *testing.T) {
	for _, field := range []string{
		"tool_loop_warning_count",
		"first_tool_loop_warning_llm_call",
		"tool_loop_warning_llm_calls",
	} {
		t.Run("missing "+field, func(t *testing.T) {
			output := t.TempDir()
			result := executor.CaseResult{
				InstanceID: "case-a", ModelPatch: "patch",
				Info:                    executor.CaseInfo{ExitStatus: "Submitted", ToolLoopWarning: true},
				ToolLoopWarningLLMCalls: []int{},
			}
			if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
				t.Fatal(err)
			}
			resultPath := filepath.Join(output, "case-a", "case-a.native.json")
			var fields map[string]json.RawMessage
			if err := artifact.ReadJSONFile(resultPath, &fields); err != nil {
				t.Fatal(err)
			}
			delete(fields, field)
			if err := artifact.WriteJSON(resultPath, fields); err != nil {
				t.Fatal(err)
			}
			prediction := contract.Prediction{InstanceID: "case-a", ModelPatch: "patch"}
			if _, err := loadExistingCaseBundle(output, "case-a", prediction); err == nil ||
				!strings.Contains(err.Error(), field) {
				t.Fatalf("missing %s error = %v", field, err)
			}
		})
	}
	for _, field := range []string{"tool_loop_warning_count", "tool_loop_warning_llm_calls"} {
		t.Run("null "+field, func(t *testing.T) {
			output := t.TempDir()
			result := executor.CaseResult{
				InstanceID: "case-a", ModelPatch: "patch",
				Info:                    executor.CaseInfo{ExitStatus: "Submitted", ToolLoopWarning: true},
				ToolLoopWarningLLMCalls: []int{},
			}
			if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
				t.Fatal(err)
			}
			resultPath := filepath.Join(output, "case-a", "case-a.native.json")
			var fields map[string]json.RawMessage
			if err := artifact.ReadJSONFile(resultPath, &fields); err != nil {
				t.Fatal(err)
			}
			fields[field] = json.RawMessage("null")
			if err := artifact.WriteJSON(resultPath, fields); err != nil {
				t.Fatal(err)
			}
			prediction := contract.Prediction{InstanceID: "case-a", ModelPatch: "patch"}
			if _, err := loadExistingCaseBundle(output, "case-a", prediction); err == nil ||
				!strings.Contains(err.Error(), field) {
				t.Fatalf("null %s error = %v", field, err)
			}
		})
	}
}

func TestLoadExistingCaseBundleAcceptsLegacyWarningOffTelemetryOmission(t *testing.T) {
	output := t.TempDir()
	result := executor.CaseResult{
		InstanceID: "case-a", ModelPatch: "patch",
		Info: executor.CaseInfo{ExitStatus: "Submitted"},
	}
	if err := writeCaseBundle(output, &result, artifact.WriteJSON); err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(output, "case-a", "case-a.native.json")
	var fields map[string]json.RawMessage
	if err := artifact.ReadJSONFile(resultPath, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "tool_loop_warning_count")
	delete(fields, "first_tool_loop_warning_llm_call")
	delete(fields, "tool_loop_warning_llm_calls")
	if err := artifact.WriteJSON(resultPath, fields); err != nil {
		t.Fatal(err)
	}
	prediction := contract.Prediction{InstanceID: "case-a", ModelPatch: "patch"}
	if _, err := loadExistingCaseBundle(output, "case-a", prediction); err != nil {
		t.Fatal(err)
	}
}

func TestReadExistingCaseResultRequiresExplicitWorkspaceIdentityFields(t *testing.T) {
	identity := testWorkspaceIdentity(t, "case-a")
	result := resumeCaseResult(t, contract.Case{InstanceID: "case-a"}, "patch", identity)
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var envelope map[string]any
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatal(err)
	}
	info, ok := envelope["info"].(map[string]any)
	if !ok {
		t.Fatalf("info = %#v", envelope["info"])
	}
	delete(info, "workspace_preload")
	payload, err = json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "case-a.native.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readExistingCaseResult(path, "case-a"); err == nil ||
		!strings.Contains(err.Error(), `missing required info field "workspace_preload"`) {
		t.Fatalf("error = %v", err)
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
