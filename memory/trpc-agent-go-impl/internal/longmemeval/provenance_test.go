//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
)

const (
	testBenchmarkRevision = "0123456789abcdef0123456789abcdef01234567"
	testAgentRevision     = "a4e36132659f"
)

func buildLMERunManifest(
	ctx context.Context,
	request lmeRunManifestRequest,
	deps lmeProvenanceDependencies,
) (*lmeRunManifest, error) {
	return buildLMERunManifestAt(ctx, request, "", deps)
}

func TestEnsureLMERunManifestCreateAndResume(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	outputDir := fixture.outputDir
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	deps := testLMEProvenanceDependencies(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC))
	created, err := ensureLMERunManifestWithDependencies(
		context.Background(), outputDir, fixture.request, false, deps,
	)
	if err != nil {
		t.Fatalf("ensureLMERunManifestWithDependencies(create) error = %v", err)
	}
	manifestPath := filepath.Join(outputDir, lmeRunManifestFileName)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if created.SchemaVersion != lmeRunManifestSchemaVersion {
		t.Fatalf("SchemaVersion = %d, want %d", created.SchemaVersion, lmeRunManifestSchemaVersion)
	}
	if created.CompatibilityDigest == "" {
		t.Fatal("CompatibilityDigest is empty")
	}
	if created.Run.EffectiveTopK != lmeRetrievalTopK {
		t.Fatalf("EffectiveTopK = %d, want %d", created.Run.EffectiveTopK, lmeRetrievalTopK)
	}
	if !created.Reproducible || created.OfficialStatus != lmeOfficialStatusEligible {
		t.Fatalf("official state = (%v, %q), blockers = %v", created.Reproducible, created.OfficialStatus, created.OfficialBlockers)
	}
	if created.Artifacts.Dataset.Path != "" || created.Artifacts.CaseManifest.Path != "" {
		t.Fatalf("external input paths must not be persisted: %+v", created.Artifacts)
	}
	if created.Artifacts.CanonicalReplay.Path != "replay" {
		t.Fatalf("canonical replay path = %q, want %q", created.Artifacts.CanonicalReplay.Path, "replay")
	}
	if created.Artifacts.BuildPlan.Path != "build_plan.json" {
		t.Fatalf("build plan path = %q, want %q", created.Artifacts.BuildPlan.Path, "build_plan.json")
	}
	resolved, blockers := resolveAndDigestLMEInputs(manifestPath, created)
	if len(blockers) != 0 {
		t.Fatalf("resolveAndDigestLMEInputs() blockers = %v", blockers)
	}
	if resolved.replay != fixture.request.Config.ReplayRoot {
		t.Fatalf("resolved replay = %q, want %q", resolved.replay, fixture.request.Config.ReplayRoot)
	}
	if resolved.buildPlan != fixture.request.Config.BuildPlanRoot {
		t.Fatalf("resolved build plan = %q, want %q", resolved.buildPlan, fixture.request.Config.BuildPlanRoot)
	}

	_, err = ensureLMERunManifestWithDependencies(
		context.Background(), outputDir, fixture.request, false, deps,
	)
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second create error = %v, want already exists", err)
	}
	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("ReadFile(after) error = %v", err)
	}
	if string(before) != string(after) {
		t.Fatal("immutable run manifest changed after a second create")
	}

	resumeDeps := testLMEProvenanceDependencies(time.Date(2026, 7, 17, 10, 0, 0, 0, time.UTC))
	resumed, err := ensureLMERunManifestWithDependencies(
		context.Background(), outputDir, fixture.request, true, resumeDeps,
	)
	if err != nil {
		t.Fatalf("ensureLMERunManifestWithDependencies(resume) error = %v", err)
	}
	if resumed.CompatibilityDigest != created.CompatibilityDigest {
		t.Fatalf("resume digest = %q, want %q", resumed.CompatibilityDigest, created.CompatibilityDigest)
	}
	if !resumed.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("resume CreatedAt = %v, want immutable %v", resumed.CreatedAt, created.CreatedAt)
	}
}

func TestEnsureLMERunManifestRejectsDirtyResume(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	if err := os.MkdirAll(fixture.outputDir, 0755); err != nil {
		t.Fatal(err)
	}
	deps := lmeProvenanceDependencies{
		now: func() time.Time { return time.Now() },
		readBuildInfo: func() (*debug.BuildInfo, bool) {
			return testLMEBuildInfo(true), true
		},
	}
	if _, err := ensureLMERunManifestWithDependencies(
		context.Background(), fixture.outputDir, fixture.request, false, deps,
	); err != nil {
		t.Fatalf("create dirty run manifest: %v", err)
	}
	_, err := ensureLMERunManifestWithDependencies(
		context.Background(), fixture.outputDir, fixture.request, true, deps,
	)
	if err == nil || !strings.Contains(err.Error(), "clean benchmark worktree") {
		t.Fatalf("dirty resume error = %v", err)
	}
}

func TestDeriveLMERunManifestBlockersRejectsBiasedCaseSelection(t *testing.T) {
	tests := []struct {
		name   string
		method dataset.LongMemEvalManifestMethod
		split  string
		want   string
	}{
		{
			name:   "full category with split",
			method: dataset.LongMemEvalManifestMethodFullCategory,
			split:  dataset.LongMemEvalManifestSplitDev,
			want:   "full-category case manifest must not declare a split",
		},
		{
			name:   "sample without split",
			method: dataset.LongMemEvalManifestMethodStratifiedSHA256,
			want:   "sampled case manifest must declare a dev or holdout split",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := &lmeRunManifest{Run: lmeRunIdentity{
				Scenario:                  "auto",
				EffectiveTopK:             lmeRetrievalTopK,
				CaseManifestSchemaVersion: dataset.LongMemEvalManifestSchemaVersion,
				CaseManifestMethod:        string(test.method),
				CaseManifestSplit:         test.split,
			}}
			blockers := strings.Join(deriveLMERunManifestBlockers(manifest), "\n")
			if !strings.Contains(blockers, test.want) {
				t.Fatalf("blockers = %q, want %q", blockers, test.want)
			}
		})
	}
}

func TestAutoUpdatePolicyIsExplicitTreatmentOutsideComparisonControls(t *testing.T) {
	mergeSimilar := &lmeRunManifest{
		Run: lmeRunIdentity{
			Scenario:         "auto",
			AutoUpdatePolicy: string(lmeAutoUpdatePolicyMergeSimilar),
		},
		Config: map[string]any{
			"auto_update_policy": string(lmeAutoUpdatePolicyMergeSimilar),
		},
	}
	preserveHistory := &lmeRunManifest{
		Run: lmeRunIdentity{
			Scenario:         "auto",
			AutoUpdatePolicy: string(lmeAutoUpdatePolicyPreserveHistory),
		},
		Config: map[string]any{
			"auto_update_policy": string(lmeAutoUpdatePolicyPreserveHistory),
		},
	}
	mergeSimilarDigest, err := calculateLMERunCompatibilityDigest(mergeSimilar)
	if err != nil {
		t.Fatalf("calculate mergeSimilar run digest: %v", err)
	}
	preserveHistoryDigest, err := calculateLMERunCompatibilityDigest(preserveHistory)
	if err != nil {
		t.Fatalf("calculate preserveHistory run digest: %v", err)
	}
	if mergeSimilarDigest == preserveHistoryDigest {
		t.Fatal("run compatibility digest does not distinguish update policies")
	}
	mergeSimilarComparison, err := calculateLMEComparisonDigest(mergeSimilar)
	if err != nil {
		t.Fatalf("calculate mergeSimilar comparison digest: %v", err)
	}
	preserveHistoryComparison, err := calculateLMEComparisonDigest(preserveHistory)
	if err != nil {
		t.Fatalf("calculate preserveHistory comparison digest: %v", err)
	}
	if mergeSimilarComparison != preserveHistoryComparison {
		t.Fatal("comparison control digest distinguishes the update-policy treatment under test")
	}
	if mergeSimilar.Run.AutoUpdatePolicy == preserveHistory.Run.AutoUpdatePolicy {
		t.Fatal("run identity does not distinguish update-policy treatments")
	}
}

func TestConversationExtractionIsExplicitTreatmentOutsideComparisonControls(t *testing.T) {
	disabled := &lmeRunManifest{
		Run: lmeRunIdentity{
			Scenario:               "auto",
			ConversationExtraction: string(lmeConversationExtractionDisabled),
		},
		Config: map[string]any{
			"conversation_extraction": string(lmeConversationExtractionDisabled),
		},
	}
	assistantEpisode := &lmeRunManifest{
		Run: lmeRunIdentity{
			Scenario: "auto",
			ConversationExtraction: string(
				lmeConversationExtractionAssistantEpisode,
			),
		},
		Config: map[string]any{
			"conversation_extraction": string(
				lmeConversationExtractionAssistantEpisode,
			),
		},
	}
	disabledDigest, err := calculateLMERunCompatibilityDigest(disabled)
	if err != nil {
		t.Fatalf("calculate disabled run digest: %v", err)
	}
	assistantEpisodeDigest, err := calculateLMERunCompatibilityDigest(assistantEpisode)
	if err != nil {
		t.Fatalf("calculate assistant episode run digest: %v", err)
	}
	if disabledDigest == assistantEpisodeDigest {
		t.Fatal("run compatibility digest does not distinguish conversation extraction")
	}
	disabledComparison, err := calculateLMEComparisonDigest(disabled)
	if err != nil {
		t.Fatalf("calculate disabled comparison digest: %v", err)
	}
	assistantEpisodeComparison, err := calculateLMEComparisonDigest(assistantEpisode)
	if err != nil {
		t.Fatalf("calculate assistant episode comparison digest: %v", err)
	}
	if disabledComparison != assistantEpisodeComparison {
		t.Fatal("comparison control digest distinguishes conversation extraction")
	}
}

func TestCalculateLMEComparisonDigestIgnoresCollectionSource(t *testing.T) {
	manifest := &lmeRunManifest{
		Code: lmeCodeProvenance{
			Benchmark: lmeBenchmarkProvenance{
				Revision: testBenchmarkRevision,
				Source:   "git",
			},
		},
	}
	gitDigest, err := calculateLMEComparisonDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Code.Benchmark.Source = "build_info"
	buildInfoDigest, err := calculateLMEComparisonDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if gitDigest != buildInfoDigest {
		t.Fatalf("comparison digests differ by collection source: %q != %q", gitDigest, buildInfoDigest)
	}
}

func TestDeriveLMERunManifestBlockersRequiresImmutableMem0Revision(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	fixture.request.Scenario = "mem0_oss"
	fixture.request.Backend = "mem0"
	manifest, err := buildLMERunManifestAt(
		context.Background(),
		fixture.request,
		fixture.outputDir,
		testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Run.BackendRevision = "main"
	blockers := deriveLMERunManifestBlockers(manifest)
	if !strings.Contains(strings.Join(blockers, "\n"), "full Git commit or image digest") {
		t.Fatalf("blockers = %v, want mutable revision rejection", blockers)
	}
	manifest.Run.BackendRevision = strings.Repeat("a", 40)
	blockers = deriveLMERunManifestBlockers(manifest)
	if strings.Contains(strings.Join(blockers, "\n"), "Mem0 OSS revision") {
		t.Fatalf("blockers = %v, immutable revision was rejected", blockers)
	}
}

func TestDeriveLMERunManifestBlockersRequiresAlignedTemporalContext(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	manifest, err := buildLMERunManifestAt(
		context.Background(),
		fixture.request,
		fixture.outputDir,
		testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatal(err)
	}

	manifest.Run.TemporalContext = "storage_metadata_only"
	blockers := deriveLMERunManifestBlockers(manifest)
	if !strings.Contains(strings.Join(blockers, "\n"), "Auto temporal_context") {
		t.Fatalf("blockers = %v, want Auto temporal-context rejection", blockers)
	}

	manifest.Run.TemporalContext = lmeAutoTemporalContext
	delete(manifest.Config, "temporal_reference_source")
	delete(manifest.Config, "temporal_reference_format")
	blockers = deriveLMERunManifestBlockers(manifest)
	joined := strings.Join(blockers, "\n")
	if !strings.Contains(joined, "temporal_reference_source") ||
		!strings.Contains(joined, "temporal_reference_format") {
		t.Fatalf("blockers = %v, want temporal-reference config rejection", blockers)
	}
}

func TestEnsureLMERunManifestConcurrentCreate(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	outputDir := fixture.outputDir
	if err := os.MkdirAll(outputDir, 0755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	deps := testLMEProvenanceDependencies(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC))
	const workers = 8
	errorsByWorker := make([]error, workers)
	var wait sync.WaitGroup
	wait.Add(workers)
	for i := 0; i < workers; i++ {
		go func(index int) {
			defer wait.Done()
			_, errorsByWorker[index] = ensureLMERunManifestWithDependencies(
				context.Background(), outputDir, fixture.request, false, deps,
			)
		}(i)
	}
	wait.Wait()
	successes := 0
	for _, err := range errorsByWorker {
		if err == nil {
			successes++
			continue
		}
		if !strings.Contains(err.Error(), "already exists") {
			t.Errorf("concurrent create error = %v, want already exists", err)
		}
	}
	if successes != 1 {
		t.Fatalf("successful creates = %d, want 1", successes)
	}
	if _, err := readLMERunManifest(filepath.Join(outputDir, lmeRunManifestFileName)); err != nil {
		t.Fatalf("readLMERunManifest() error = %v", err)
	}
}

func TestEnsureLMERunManifestResumeMismatch(t *testing.T) {
	tests := []struct {
		name   string
		want   string
		mutate func(*testing.T, *lmeProvenanceFixture)
	}{
		{
			name: "scenario",
			want: "scenario changed",
			mutate: func(_ *testing.T, fixture *lmeProvenanceFixture) {
				fixture.request.Scenario = "mem0_oss"
			},
		},
		{
			name: "model",
			want: "model changed",
			mutate: func(_ *testing.T, fixture *lmeProvenanceFixture) {
				fixture.request.Config.ModelName = "different-model"
			},
		},
		{
			name: "dataset",
			want: "dataset artifact changed",
			mutate: func(t *testing.T, fixture *lmeProvenanceFixture) {
				writeLMEProvenanceTestFile(t, fixture.request.Config.DatasetPath, `[{"question_id":"changed"}]`)
			},
		},
		{
			name: "case manifest",
			want: "case manifest artifact changed",
			mutate: func(t *testing.T, fixture *lmeProvenanceFixture) {
				data, err := os.ReadFile(fixture.request.Config.ManifestPath)
				if err != nil {
					t.Fatalf("ReadFile(case manifest) error = %v", err)
				}
				writeLMEProvenanceTestFile(t, fixture.request.Config.ManifestPath, string(data)+"\n")
			},
		},
		{
			name: "canonical replay",
			want: "effective configuration changed",
			mutate: func(_ *testing.T, fixture *lmeProvenanceFixture) {
				fixture.request.Config.ReplayDigest = strings.Repeat("a", 64)
			},
		},
		{
			name: "build plan",
			want: "effective configuration changed",
			mutate: func(_ *testing.T, fixture *lmeProvenanceFixture) {
				fixture.request.Config.BuildPlanDigest = strings.Repeat("b", 64)
			},
		},
		{
			name: "case order",
			want: "case IDs or order changed",
			mutate: func(_ *testing.T, fixture *lmeProvenanceFixture) {
				fixture.request.CaseIDs = []string{"case-2", "case-1"}
			},
		},
		{
			name: "trace content mode",
			want: "effective configuration changed",
			mutate: func(_ *testing.T, fixture *lmeProvenanceFixture) {
				fixture.request.Config.TraceContentMode = lmeTraceContentNone
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newLMEProvenanceFixture(t)
			outputDir := fixture.outputDir
			if err := os.MkdirAll(outputDir, 0755); err != nil {
				t.Fatalf("MkdirAll() error = %v", err)
			}
			deps := testLMEProvenanceDependencies(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC))
			if _, err := ensureLMERunManifestWithDependencies(
				context.Background(), outputDir, fixture.request, false, deps,
			); err != nil {
				t.Fatalf("create manifest error = %v", err)
			}
			test.mutate(t, fixture)
			_, err := ensureLMERunManifestWithDependencies(
				context.Background(), outputDir, fixture.request, true, deps,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("resume error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestBuildLMERunManifestDirtyAndUnresolved(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	info := testLMEBuildInfo(true)
	info.Deps = append(info.Deps,
		&debug.Module{
			Path:    "trpc.group/trpc-go/trpc-agent-go/memory/pgvector",
			Version: "v1.7.0",
			Replace: &debug.Module{Path: "../../memory/pgvector"},
		},
		&debug.Module{
			Path:    "trpc.group/trpc-go/trpc-agent-go/memory/sqlite",
			Version: "(devel)",
		},
	)
	deps := lmeProvenanceDependencies{
		now: func() time.Time { return time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC) },
		readBuildInfo: func() (*debug.BuildInfo, bool) {
			return info, true
		},
	}
	manifest, err := buildLMERunManifest(context.Background(), fixture.request, deps)
	if err != nil {
		t.Fatalf("buildLMERunManifest() error = %v", err)
	}
	if manifest.Reproducible || manifest.OfficialStatus != lmeOfficialStatusBlocked {
		t.Fatalf("official state = (%v, %q), want blocked", manifest.Reproducible, manifest.OfficialStatus)
	}
	joined := strings.Join(manifest.OfficialBlockers, "\n")
	for _, want := range []string{"worktree is dirty", "uses a local replacement", "is unresolved"} {
		if !strings.Contains(joined, want) {
			t.Errorf("OfficialBlockers = %v, want %q", manifest.OfficialBlockers, want)
		}
	}
}

func TestLMERunCompatibilityDigestStable(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	first, err := buildLMERunManifest(
		context.Background(),
		fixture.request,
		testLMEProvenanceDependencies(time.Date(2026, 7, 16, 10, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest(first) error = %v", err)
	}
	second, err := buildLMERunManifest(
		context.Background(),
		fixture.request,
		testLMEProvenanceDependencies(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest(second) error = %v", err)
	}
	if first.CreatedAt.Equal(second.CreatedAt) {
		t.Fatal("test setup produced equal creation timestamps")
	}
	if first.CompatibilityDigest != second.CompatibilityDigest {
		t.Fatalf("compatibility digest changed with creation time: %q != %q", first.CompatibilityDigest, second.CompatibilityDigest)
	}

	data, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded lmeRunManifest
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	decodedDigest, err := calculateLMERunCompatibilityDigest(&decoded)
	if err != nil {
		t.Fatalf("calculateLMERunCompatibilityDigest(decoded) error = %v", err)
	}
	if decodedDigest != first.CompatibilityDigest {
		t.Fatalf("decoded digest = %q, want %q", decodedDigest, first.CompatibilityDigest)
	}
}

func TestBuildLMERunManifestMissingOptionalArtifacts(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	fixture.request.Config.ManifestPath = ""
	fixture.request.Config.ReplayRoot = filepath.Join(t.TempDir(), "missing-replay")
	fixture.request.Config.BuildPlanRoot = ""
	fixture.request.Config.BuildPlanDigest = ""
	manifest, err := buildLMERunManifest(
		context.Background(), fixture.request, testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest() error = %v", err)
	}
	if manifest.Artifacts.CaseManifest.Configured || manifest.Artifacts.CaseManifest.Available {
		t.Fatalf("CaseManifest = %+v, want not configured", manifest.Artifacts.CaseManifest)
	}
	if !manifest.Artifacts.CanonicalReplay.Configured || manifest.Artifacts.CanonicalReplay.Available {
		t.Fatalf("CanonicalReplay = %+v, want configured but unavailable", manifest.Artifacts.CanonicalReplay)
	}
	if manifest.Reproducible {
		t.Fatal("manifest with missing configured replay is reproducible")
	}
	if !strings.Contains(strings.Join(manifest.OfficialBlockers, "\n"), "canonical_replay") {
		t.Fatalf("OfficialBlockers = %v, want canonical_replay blocker", manifest.OfficialBlockers)
	}
	if manifest.Unavailable["artifacts.case_manifest"] != "not configured" {
		t.Fatalf("case manifest unavailable reason = %q", manifest.Unavailable["artifacts.case_manifest"])
	}
}

func TestLMEProvenanceSecretRedaction(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	fixture.request.Config.Mem0Host =
		"https://alice:super-secret@example.com/v1?api_key=query-secret&region=us#fragment-secret"
	fixture.request.Config.Mem0APIKeySet = true
	manifest, err := buildLMERunManifest(
		context.Background(), fixture.request, testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest() error = %v", err)
	}
	redacted := sanitizeProvenanceMap(map[string]any{
		"OPENAI_API_KEY": "openai-secret",
		"Authorization":  "Bearer auth-secret",
		"PGVECTOR_DSN":   "postgres://bob:dsn-secret@db.example/test",
		"proxy_url":      "https://proxy-user:proxy-secret@proxy.example/path?token=proxy-token",
		"max_tokens":     20,
		"tokenizer_name": "o200k_base",
	})
	payload, err := json.Marshal(map[string]any{
		"manifest": manifest,
		"redacted": redacted,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	text := string(payload)
	for _, secret := range []string{
		"super-secret", "query-secret", "openai-secret", "auth-secret",
		"dsn-secret", "proxy-secret", "proxy-token", "fragment-secret",
		"region=us", "alice", "proxy-user",
	} {
		if strings.Contains(text, secret) {
			t.Errorf("serialized provenance contains secret %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, "o200k_base") || !strings.Contains(text, "max_tokens") {
		t.Fatalf("non-secret token settings were incorrectly redacted: %s", text)
	}
	if manifest.Config["mem0_auth_configured"] != true {
		t.Fatalf("mem0_auth_configured = %v, want true", manifest.Config["mem0_auth_configured"])
	}
}

func TestFingerprintLMEEndpointExcludesCredentialsQueryAndFragment(t *testing.T) {
	withSecrets := fingerprintLMEEndpoint(
		"https://alice:secret@example.com/v1?api_key=secret#region-secret",
	)
	withoutSecrets := fingerprintLMEEndpoint("https://example.com/v1")
	if withSecrets != withoutSecrets {
		t.Fatalf("endpoint fingerprints differ: %q != %q", withSecrets, withoutSecrets)
	}
	for _, secret := range []string{"alice", "secret", "region-secret", "example.com"} {
		if strings.Contains(withSecrets, secret) {
			t.Fatalf("endpoint fingerprint contains %q: %s", secret, withSecrets)
		}
	}
	if got := fingerprintLMEEndpoint(""); got != "provider-default" {
		t.Fatalf("default endpoint fingerprint = %q", got)
	}
	if got := sanitizeEndpoint("not a valid endpoint?token=secret"); got != lmeRedactedValue {
		t.Fatalf("invalid endpoint sanitizer = %q, want redacted", got)
	}
}

func TestReadLMERunManifestRejectsTampering(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	manifest, err := buildLMERunManifest(
		context.Background(), fixture.request, testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest() error = %v", err)
	}
	manifest.Run.EffectiveTopK++
	path := filepath.Join(t.TempDir(), lmeRunManifestFileName)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	_, err = readLMERunManifest(path)
	if err == nil || !strings.Contains(err.Error(), "digest is invalid") {
		t.Fatalf("readLMERunManifest() error = %v, want invalid digest", err)
	}
}

func TestReadLMERunManifestRejectsUnknownAndTrailingJSON(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	manifest, err := buildLMERunManifest(
		context.Background(), fixture.request, testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest() error = %v", err)
	}
	canonical, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	var unknown map[string]any
	if err := json.Unmarshal(canonical, &unknown); err != nil {
		t.Fatal(err)
	}
	unknown["unexpected"] = true
	unknownData, err := json.Marshal(unknown)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: unknownData},
		{name: "trailing value", data: append(append([]byte{}, canonical...), []byte("\n{}\n")...)},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), lmeRunManifestFileName)
			if err := os.WriteFile(path, test.data, 0644); err != nil {
				t.Fatal(err)
			}
			if _, err := readLMERunManifest(path); err == nil ||
				!strings.Contains(err.Error(), "invalid or non-canonical JSON") {
				t.Fatalf("readLMERunManifest() error = %v", err)
			}
		})
	}
}

func TestReadLMERunManifestSanitizesReadErrors(t *testing.T) {
	path := filepath.Join(t.TempDir(), "secret", lmeRunManifestFileName)
	_, err := readLMERunManifest(path)
	if err == nil {
		t.Fatal("readLMERunManifest() expected an error")
	}
	if strings.Contains(err.Error(), filepath.Dir(path)) {
		t.Fatalf("read error leaked an absolute path: %v", err)
	}
}

func TestLMECheckpointManifestBinding(t *testing.T) {
	fixture := newLMEProvenanceFixture(t)
	manifest, err := buildLMERunManifest(
		context.Background(), fixture.request, testLMEProvenanceDependencies(time.Now()),
	)
	if err != nil {
		t.Fatalf("buildLMERunManifest() error = %v", err)
	}
	result := newLMERunResult("auto", "pgvector", fixture.request.Config, 2)
	bindLMERunManifest(result, manifest)
	if err := verifyLMECheckpointManifest(result, manifest); err != nil {
		t.Fatalf("verifyLMECheckpointManifest() error = %v", err)
	}
	result.Metadata.RunCompatibility = "sha256:different"
	if err := verifyLMECheckpointManifest(result, manifest); err == nil {
		t.Fatal("verifyLMECheckpointManifest() accepted a foreign checkpoint")
	}
}

func TestCollectLMECodeProvenanceGitFallback(t *testing.T) {
	deps := lmeProvenanceDependencies{
		readBuildInfo: func() (*debug.BuildInfo, bool) { return nil, false },
		runGit: func(_ context.Context, args ...string) ([]byte, error) {
			switch strings.Join(args, " ") {
			case "rev-parse HEAD":
				return []byte(testBenchmarkRevision + "\n"), nil
			case "status --porcelain --untracked-files=normal":
				return nil, nil
			default:
				return nil, errors.New("unexpected Git command")
			}
		},
	}
	code, blockers, unavailable := collectLMECodeProvenance(context.Background(), deps)
	if code.Benchmark.Revision != testBenchmarkRevision || code.Benchmark.DirtyState != lmeDirtyStateClean {
		t.Fatalf("Benchmark = %+v", code.Benchmark)
	}
	if !strings.Contains(strings.Join(blockers, "\n"), "module provenance is unavailable") {
		t.Fatalf("blockers = %v, want missing module provenance", blockers)
	}
	if unavailable["code.go_build_info"] == "" {
		t.Fatalf("unavailable = %v, want Go build info reason", unavailable)
	}
}

type lmeProvenanceFixture struct {
	root      string
	outputDir string
	request   lmeRunManifestRequest
}

func newLMEProvenanceFixture(t *testing.T) *lmeProvenanceFixture {
	t.Helper()
	root := t.TempDir()
	datasetPath := filepath.Join(root, "dataset.json")
	manifestPath := filepath.Join(root, "manifest.json")
	replayPath := filepath.Join(root, "replay")
	buildPlanPath := filepath.Join(root, "build_plan.json")
	if err := os.MkdirAll(replayPath, 0755); err != nil {
		t.Fatalf("MkdirAll(replay) error = %v", err)
	}
	writeLMEProvenanceTestFile(t, datasetPath, `[{"question_id":"case-1"},{"question_id":"case-2"}]`)
	manifest, err := dataset.BuildLongMemEvalManifest(
		[]*dataset.LongMemEvalInstance{
			{QuestionID: "case-1", QuestionType: "single-session-user"},
			{QuestionID: "case-2", QuestionType: "single-session-user"},
		},
		dataset.LongMemEvalManifestSelection{
			Method:        dataset.LongMemEvalManifestMethodFullCategory,
			QuestionTypes: []string{"single-session-user"},
		},
	)
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifest() error = %v", err)
	}
	if err := dataset.WriteLongMemEvalManifest(manifestPath, manifest); err != nil {
		t.Fatalf("WriteLongMemEvalManifest() error = %v", err)
	}
	writeLMEProvenanceTestFile(t, filepath.Join(replayPath, "case-1.json"), `{"case_id":"case-1"}`)
	writeLMEProvenanceTestFile(t, buildPlanPath, `{"protocol":"turn-pair-fragment","chunks":1}`)
	cfg := lmeRunConfig{
		ModelName:                    "gpt-4o-mini",
		EmbedModelName:               "text-embedding-3-small",
		LLMEndpointFingerprint:       "sha256:" + strings.Repeat("a", 64),
		EmbeddingEndpointFingerprint: "sha256:" + strings.Repeat("b", 64),
		DatasetPath:                  datasetPath,
		ManifestPath:                 manifestPath,
		ReplayRoot:                   replayPath,
		BuildPlanRoot:                buildPlanPath,
		ReplayDigest:                 strings.Repeat("1", 64),
		BuildPlanDigest:              strings.Repeat("2", 64),
		BuildTokenizer:               "tiktoken:o200k_base",
		QuestionTypes:                []string{"single-session-user", "multi-session"},
		MaxTasks:                     2,
		RetrievalTopK:                lmeRetrievalTopK,
		MaxRetries:                   2,
		AnswerMaxTokens:              500,
		JudgeMaxTokens:               10240,
		AutoExtractionWait:           10 * time.Minute,
		AutoMemoryTable:              "memory_eval_auto_test",
		AutoUpdatePolicy:             lmeAutoUpdatePolicyMergeSimilar,
		ConversationExtraction:       string(lmeConversationExtractionDisabled),
		EmbeddingCacheEnabled:        true,
		EmbeddingCachePath:           filepath.Join(root, "cache"),
		TransportRetryEnabled:        true,
		TransportRetryStrategy:       "transport only",
		FullQALog:                    true,
		Mem0Host:                     "http://localhost:8888",
		Mem0IngestTimeout:            10 * time.Minute,
		TraceContentMode:             lmeTraceContentHash,
	}
	return &lmeProvenanceFixture{
		root:      root,
		outputDir: filepath.Join(root, "run"),
		request: lmeRunManifestRequest{
			Scenario: "auto",
			Backend:  "pgvector",
			Table:    cfg.AutoMemoryTable,
			Config:   cfg,
			CaseIDs:  []string{"case-1", "case-2"},
		},
	}
}

func testLMEProvenanceDependencies(now time.Time) lmeProvenanceDependencies {
	return lmeProvenanceDependencies{
		now: func() time.Time { return now },
		readBuildInfo: func() (*debug.BuildInfo, bool) {
			return testLMEBuildInfo(false), true
		},
	}
}

func testLMEBuildInfo(dirty bool) *debug.BuildInfo {
	dirtyValue := "false"
	if dirty {
		dirtyValue = "true"
	}
	return &debug.BuildInfo{
		GoVersion: "go1.24.0",
		Path:      "trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl",
		Main: debug.Module{
			Path:    "trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl",
			Version: "(devel)",
		},
		Deps: []*debug.Module{
			{
				Path:    "trpc.group/trpc-go/trpc-agent-go",
				Version: "v1.7.0",
				Replace: &debug.Module{
					Path:    "github.com/trpc-group/trpc-agent-go",
					Version: "v1.7.1-0.20260402032440-" + testAgentRevision,
					Sum:     "h1:test-checksum",
				},
			},
		},
		Settings: []debug.BuildSetting{
			{Key: "vcs.revision", Value: testBenchmarkRevision},
			{Key: "vcs.modified", Value: dirtyValue},
		},
	}
}

func writeLMEProvenanceTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("WriteFile(%s) error = %v", path, err)
	}
}
