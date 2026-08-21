//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
)

// Load validates a portable bundle and its exact Native provenance. It reads
// no gold labels and ignores the candidate model_patch field in Native case
// JSON. All referenced paths are relative to the bundle directory.
func Load(bundlePath, runDir string) (*LoadedBundle, error) {
	bundlePath = strings.TrimSpace(bundlePath)
	if bundlePath == "" {
		return nil, errors.New("bundle path is required")
	}
	absBundle, err := filepath.Abs(bundlePath)
	if err != nil {
		return nil, fmt.Errorf("resolve bundle path: %w", err)
	}
	absBundle = filepath.Clean(absBundle)
	bundleRoot, err := resolveRealDirectory(filepath.Dir(absBundle), "bundle directory")
	if err != nil {
		return nil, err
	}
	bundleData, err := readRegularFile(absBundle, "retrieval replay bundle", maxBundleBytes)
	if err != nil {
		return nil, err
	}
	var bundle Bundle
	if err := decodeStrictJSON(bundleData, &bundle); err != nil {
		return nil, fmt.Errorf("parse retrieval replay bundle: %w", err)
	}
	if err := validateBundle(bundle); err != nil {
		return nil, err
	}
	runRoot, err := resolveRealDirectory(runDir, "Native run directory")
	if err != nil {
		return nil, err
	}
	manifest, manifestSHA, err := loadNativeManifest(runRoot, bundle.NativeRun)
	if err != nil {
		return nil, err
	}
	if manifest.CaseCount < len(bundle.Cases) {
		return nil, fmt.Errorf(
			"Native manifest case_count=%d is smaller than replay selection %d",
			manifest.CaseCount,
			len(bundle.Cases),
		)
	}
	if err := verifyArtifact(bundleRoot, bundle.EngineConfig); err != nil {
		return nil, fmt.Errorf("verify engine config: %w", err)
	}

	loaded := &LoadedBundle{
		Spec: bundle, BundleSHA256: digestBytes(bundleData),
		NativeManifestSHA256: manifestSHA,
		EngineConfig:         artifactHandle(bundleRoot, bundle.EngineConfig),
		Cases:                make([]LoadedCase, 0, len(bundle.Cases)),
		bundleRoot:           bundleRoot, runRoot: runRoot,
	}
	for _, spec := range bundle.Cases {
		one, err := loadBundleCase(bundleRoot, runRoot, manifest, bundle, spec)
		if err != nil {
			return nil, err
		}
		loaded.Cases = append(loaded.Cases, one)
	}
	return loaded, nil
}

func verifyArtifact(root string, ref ArtifactRef) error {
	reader, err := openVerifiedArtifact(root, ref)
	if err != nil {
		return err
	}
	return reader.Close()
}

func validateBundle(bundle Bundle) error {
	if bundle.SchemaVersion != BundleSchemaVersion || bundle.Kind != BundleKind {
		return fmt.Errorf(
			"unsupported retrieval replay bundle schema=%d kind=%q",
			bundle.SchemaVersion,
			bundle.Kind,
		)
	}
	if strings.TrimSpace(bundle.NativeRun.RunID) == "" ||
		!isHexSHA256(bundle.NativeRun.ManifestSHA256) {
		return errors.New("bundle has invalid Native run binding")
	}
	if err := validateEngineDescriptor(bundle.Engine); err != nil {
		return fmt.Errorf("bundle engine: %w", err)
	}
	if err := validateArtifactRef(bundle.EngineConfig); err != nil {
		return fmt.Errorf("bundle engine_config: %w", err)
	}
	if len(bundle.Cases) == 0 {
		return errors.New("bundle contains no cases")
	}
	caseIDs := make([]string, len(bundle.Cases))
	seen := make(map[string]struct{}, len(bundle.Cases))
	previous := ""
	for index, spec := range bundle.Cases {
		if err := validateCaseSpec(spec); err != nil {
			return fmt.Errorf("bundle case %d: %w", index, err)
		}
		if _, exists := seen[spec.InstanceID]; exists {
			return fmt.Errorf("bundle has duplicate instance_id %q", spec.InstanceID)
		}
		if previous != "" && strings.Compare(previous, spec.InstanceID) >= 0 {
			return errors.New("bundle cases must be strictly sorted by instance_id")
		}
		seen[spec.InstanceID] = struct{}{}
		caseIDs[index] = spec.InstanceID
		previous = spec.InstanceID
	}
	expectedCaseSet, err := CaseSetSHA256(caseIDs)
	if err != nil {
		return err
	}
	if bundle.CaseSetSHA256 != expectedCaseSet {
		return fmt.Errorf(
			"bundle case_set_sha256=%s, want %s",
			bundle.CaseSetSHA256,
			expectedCaseSet,
		)
	}
	return nil
}

func validateEngineDescriptor(descriptor EngineDescriptor) error {
	if strings.TrimSpace(descriptor.Name) == "" || strings.TrimSpace(descriptor.Version) == "" {
		return errors.New("engine name and version are required")
	}
	if !isHexSHA256(descriptor.ImplementationSHA256) {
		return errors.New("engine implementation_sha256 is invalid")
	}
	if !descriptor.OfflineOnly {
		return errors.New("engine must declare offline_only=true")
	}
	return nil
}

func validateCaseSpec(spec CaseSpec) error {
	if err := validateInstanceID(spec.InstanceID); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"native_artifact_sha256":    spec.NativeArtifactSHA256,
		"responses_artifact_sha256": spec.ResponsesArtifactSHA256,
		"input_sha256":              spec.InputSHA256,
	} {
		if !isHexSHA256(value) {
			return fmt.Errorf("case %s has invalid %s", spec.InstanceID, name)
		}
	}
	for name, ref := range map[string]ArtifactRef{
		"corpus": spec.Corpus, "queries": spec.Queries, "expected_results": spec.ExpectedResults,
	} {
		if err := validateArtifactRef(ref); err != nil {
			return fmt.Errorf("case %s %s: %w", spec.InstanceID, name, err)
		}
	}
	if spec.EmbeddingCache != nil {
		if err := validateArtifactRef(*spec.EmbeddingCache); err != nil {
			return fmt.Errorf("case %s embedding_cache: %w", spec.InstanceID, err)
		}
	}
	if err := validateRetrievalProvenance(spec.Retrieval, spec); err != nil {
		return fmt.Errorf("case %s retrieval provenance: %w", spec.InstanceID, err)
	}
	return nil
}

func validateRetrievalProvenance(provenance RetrievalProvenance, spec CaseSpec) error {
	if provenance.CorpusFormat != "workspace-tar-v1" &&
		provenance.CorpusFormat != "eligible-corpus-jsonl-v1" {
		return fmt.Errorf("unsupported corpus_format %q", provenance.CorpusFormat)
	}
	for name, value := range map[string]string{
		"corpus_sha256":                   provenance.CorpusSHA256,
		"eligible_file_set_sha256":        provenance.EligibleFileSetSHA256,
		"eligible_content_sha256":         provenance.EligibleContentSHA256,
		"document_set_sha256":             provenance.DocumentSetSHA256,
		"workspace_representation_sha256": provenance.WorkspaceRepresentationSHA256,
	} {
		if !isHexSHA256(value) {
			return fmt.Errorf("invalid %s", name)
		}
	}
	if provenance.CorpusSHA256 != spec.Corpus.SHA256 {
		return errors.New("corpus_sha256 does not match corpus artifact")
	}
	switch provenance.WorkspaceRepresentation {
	case "current-fixed", "fixed-raw", "ast-code", "ast-structured":
	default:
		return fmt.Errorf("unsupported workspace_representation %q", provenance.WorkspaceRepresentation)
	}
	if strings.TrimSpace(provenance.WorkspaceRepresentationSchema) == "" {
		return errors.New("workspace_representation_schema is required")
	}
	if strings.HasPrefix(provenance.WorkspaceRepresentation, "ast-") {
		if strings.TrimSpace(provenance.ParserDependency) == "" ||
			!isHexSHA256(provenance.ParserRuntimeSHA256) {
			return errors.New("AST retrieval requires parser_dependency and parser_runtime_sha256")
		}
	} else if provenance.ParserDependency != "" || provenance.ParserRuntimeSHA256 != "" {
		return errors.New("non-AST retrieval must not declare parser provenance")
	}
	if provenance.MaxResults <= 0 || provenance.MaxResults > 100 {
		return errors.New("max_results must be between 1 and 100")
	}
	if provenance.InvocationDedup {
		return errors.New(
			"invocation_dedup=true is unsupported by the public query-only replay engine; historical private-tool runs are not silently equivalent",
		)
	}
	switch provenance.RetrievalMode {
	case "keyword":
		if provenance.Embedding != nil || spec.EmbeddingCache != nil {
			return errors.New("keyword retrieval must not declare embedding inputs")
		}
	case "vector", "hybrid":
		if provenance.Embedding == nil || spec.EmbeddingCache == nil {
			return errors.New("vector/hybrid retrieval requires embedding identity and cache")
		}
		if err := validateEmbeddingIdentity(*provenance.Embedding); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported retrieval_mode %q", provenance.RetrievalMode)
	}
	return nil
}

func validateEmbeddingIdentity(identity EmbeddingIdentity) error {
	if strings.TrimSpace(identity.Provider) == "" || strings.TrimSpace(identity.Model) == "" ||
		strings.TrimSpace(identity.ModelFingerprint) == "" || identity.Dimensions <= 0 ||
		!isHexSHA256(identity.IdentitySHA256) {
		return errors.New("embedding identity is incomplete")
	}
	return nil
}

func loadBundleCase(
	bundleRoot, runRoot string,
	manifest nativeManifest,
	bundle Bundle,
	spec CaseSpec,
) (LoadedCase, error) {
	if err := validateCaseSpecAgainstManifest(spec, manifest); err != nil {
		return LoadedCase{}, fmt.Errorf("case %s Native manifest binding: %w", spec.InstanceID, err)
	}
	if err := verifyArtifact(bundleRoot, spec.Corpus); err != nil {
		return LoadedCase{}, fmt.Errorf("case %s corpus: %w", spec.InstanceID, err)
	}
	var embeddingCache *VerifiedArtifact
	if spec.EmbeddingCache != nil {
		if err := verifyArtifact(bundleRoot, *spec.EmbeddingCache); err != nil {
			return LoadedCase{}, fmt.Errorf("case %s embedding cache: %w", spec.InstanceID, err)
		}
		handle := artifactHandle(bundleRoot, *spec.EmbeddingCache)
		embeddingCache = &handle
	}
	queryData, err := readVerifiedArtifact(bundleRoot, spec.Queries, maxQueryBytes)
	if err != nil {
		return LoadedCase{}, fmt.Errorf("case %s queries: %w", spec.InstanceID, err)
	}
	var queries QuerySet
	if err := decodeStrictJSON(queryData, &queries); err != nil {
		return LoadedCase{}, fmt.Errorf("case %s queries: %w", spec.InstanceID, err)
	}
	if err := validateQuerySet(&queries, spec.InstanceID); err != nil {
		return LoadedCase{}, fmt.Errorf("case %s queries: %w", spec.InstanceID, err)
	}
	expectedData, err := readVerifiedArtifact(bundleRoot, spec.ExpectedResults, maxExpectedBytes)
	if err != nil {
		return LoadedCase{}, fmt.Errorf("case %s expected results: %w", spec.InstanceID, err)
	}
	var expected ExpectedResultSet
	if err := decodeStrictJSON(expectedData, &expected); err != nil {
		return LoadedCase{}, fmt.Errorf("case %s expected results: %w", spec.InstanceID, err)
	}
	if err := validateExpectedResultSet(expected, queries, spec); err != nil {
		return LoadedCase{}, fmt.Errorf("case %s expected results: %w", spec.InstanceID, err)
	}
	evidence, err := loadNativeCase(
		runRoot,
		manifest,
		spec,
		queries,
		expected,
	)
	if err != nil {
		return LoadedCase{}, err
	}
	inputSHA, err := InputSHA256(bundle.NativeRun.ManifestSHA256, bundle.Engine, bundle.EngineConfig, spec)
	if err != nil {
		return LoadedCase{}, fmt.Errorf("case %s input fingerprint: %w", spec.InstanceID, err)
	}
	if inputSHA != spec.InputSHA256 {
		return LoadedCase{}, fmt.Errorf(
			"case %s input_sha256=%s, want %s",
			spec.InstanceID,
			spec.InputSHA256,
			inputSHA,
		)
	}
	if expected.InputSHA256 != inputSHA {
		return LoadedCase{}, fmt.Errorf("case %s expected results bind a different input_sha256", spec.InstanceID)
	}
	return LoadedCase{
		Spec: spec, Native: evidence.Identity, Corpus: artifactHandle(bundleRoot, spec.Corpus),
		EmbeddingCache: embeddingCache, Queries: queries, Expected: expected,
		NativeSHA256: evidence.NativeSHA256, ResponsesSHA256: evidence.ResponsesSHA256,
		QuerySetSHA256: digestBytes(queryData), ExpectedSHA256: digestBytes(expectedData),
	}, nil
}

func validateCaseSpecAgainstManifest(spec CaseSpec, manifest nativeManifest) error {
	if spec.Retrieval.WorkspaceRepresentation != manifest.WorkspaceRepresentation ||
		spec.Retrieval.WorkspaceRepresentationSchema != manifest.RepresentationSchema ||
		spec.Retrieval.WorkspaceRepresentationSHA256 != manifest.RepresentationSHA256 {
		return errors.New("workspace representation does not match Native manifest")
	}
	if spec.Retrieval.InvocationDedup {
		return errors.New("current query-only Native code_search has invocation_dedup=false")
	}
	if manifest.EmbeddingConfig == nil {
		if manifest.EmbeddingConfigSHA256 != "" || spec.Retrieval.RetrievalMode != "keyword" ||
			spec.Retrieval.MaxResults != 6 || spec.Retrieval.Embedding != nil || spec.EmbeddingCache != nil {
			return errors.New("keyword Native manifest does not match bundle retrieval/embedding provenance")
		}
		return nil
	}
	config := manifest.EmbeddingConfig
	if manifest.EmbeddingConfigSHA256 == "" || config.RetrievalMode != spec.Retrieval.RetrievalMode ||
		config.MaxResults != spec.Retrieval.MaxResults || spec.Retrieval.Embedding == nil ||
		spec.EmbeddingCache == nil || !config.Cache.Enabled {
		return errors.New("embedding Native manifest does not match bundle retrieval provenance")
	}
	embedding := spec.Retrieval.Embedding
	if config.Provider != embedding.Provider || config.Model != embedding.Model ||
		config.Dimensions != embedding.Dimensions ||
		config.Cache.ModelFingerprint != embedding.ModelFingerprint {
		return errors.New("embedding identity does not match Native manifest")
	}
	return nil
}

func validateQuerySet(queries *QuerySet, instanceID string) error {
	if queries.SchemaVersion != QuerySetSchemaVersion || queries.Kind != QuerySetKind {
		return fmt.Errorf("unsupported schema=%d kind=%q", queries.SchemaVersion, queries.Kind)
	}
	if queries.InstanceID != instanceID {
		return fmt.Errorf("instance_id=%q, want %q", queries.InstanceID, instanceID)
	}
	if len(queries.Queries) == 0 {
		return errors.New("query set is empty")
	}
	if queries.EmittedCodeSearchCalls < len(queries.Queries) ||
		queries.SkippedEmittedCodeSearchCalls != queries.EmittedCodeSearchCalls-len(queries.Queries) {
		return errors.New("query set has inconsistent emitted/executed/skipped counts")
	}
	seenCalls := make(map[string]struct{}, len(queries.Queries))
	for index := range queries.Queries {
		query := &queries.Queries[index]
		if query.Ordinal != index+1 || query.ToolName != "code_search" || strings.TrimSpace(query.ToolCallID) == "" {
			return fmt.Errorf("query %d has invalid ordinal/tool identity", index+1)
		}
		if _, duplicate := seenCalls[query.ToolCallID]; duplicate {
			return fmt.Errorf("query %d repeats tool_call_id %q", index+1, query.ToolCallID)
		}
		seenCalls[query.ToolCallID] = struct{}{}
		if strings.TrimSpace(query.Query) == "" || query.QuerySHA256 != digestBytes([]byte(query.Query)) {
			return fmt.Errorf("query %d has invalid query fingerprint", index+1)
		}
		canonical, err := canonicalJSONObject(query.Arguments)
		if err != nil {
			return fmt.Errorf("query %d arguments: %w", index+1, err)
		}
		if query.ArgumentsSHA256 != digestBytes(canonical) {
			return fmt.Errorf("query %d has invalid arguments fingerprint", index+1)
		}
		if !isHexSHA256(query.NativeArgumentsSHA256) {
			return fmt.Errorf("query %d has invalid Native arguments fingerprint", index+1)
		}
		if query.NativeArgumentsSHA256 != digestBytes([]byte(query.NativeArguments)) {
			return fmt.Errorf("query %d has mismatched raw Native arguments fingerprint", index+1)
		}
		nativeCanonical, err := canonicalJSONObject([]byte(query.NativeArguments))
		if err != nil || !slices.Equal(nativeCanonical, canonical) {
			return fmt.Errorf("query %d raw Native arguments differ semantically", index+1)
		}
		var arguments struct {
			Query string `json:"query"`
		}
		if err := json.Unmarshal(canonical, &arguments); err != nil || arguments.Query != query.Query {
			return fmt.Errorf("query %d arguments do not encode the recorded query", index+1)
		}
		query.Arguments = append(json.RawMessage(nil), canonical...)
	}
	return nil
}

func validateExpectedResultSet(expected ExpectedResultSet, queries QuerySet, spec CaseSpec) error {
	if expected.SchemaVersion != ResultSetSchemaVersion || expected.Kind != ResultSetKind {
		return fmt.Errorf("unsupported schema=%d kind=%q", expected.SchemaVersion, expected.Kind)
	}
	if expected.InstanceID != spec.InstanceID || !isHexSHA256(expected.InputSHA256) {
		return errors.New("expected result identity is invalid")
	}
	if len(expected.Results) != len(queries.Queries) {
		return fmt.Errorf("result count=%d, want %d", len(expected.Results), len(queries.Queries))
	}
	for index, result := range expected.Results {
		query := queries.Queries[index]
		if result.Ordinal != index+1 || result.QuerySHA256 != query.QuerySHA256 {
			return fmt.Errorf("result %d does not match query identity", index+1)
		}
		if result.Documents == nil {
			return fmt.Errorf("result %d documents must be an array, not null", index+1)
		}
		switch result.Status {
		case "success":
			if result.ErrorSHA256 != "" {
				return fmt.Errorf("result %d success carries error_sha256", index+1)
			}
			if len(result.Documents) == 0 {
				return fmt.Errorf("result %d records zero-document success", index+1)
			}
		case "no_hit", "error":
			if !isHexSHA256(result.ErrorSHA256) || len(result.Documents) != 0 {
				return fmt.Errorf("result %d has invalid error outcome", index+1)
			}
		default:
			return fmt.Errorf("result %d has unsupported status %q", index+1, result.Status)
		}
		if len(result.Documents) > spec.Retrieval.MaxResults {
			return fmt.Errorf("result %d exceeds max_results", index+1)
		}
		if err := validateRankedDocuments(result.Documents); err != nil {
			return fmt.Errorf("result %d: %w", index+1, err)
		}
		fingerprint, err := OutcomeSHA256(
			result.QuerySHA256,
			result.Status,
			result.ErrorSHA256,
			result.Documents,
		)
		if err != nil {
			return err
		}
		if result.ResultSHA256 != fingerprint {
			return fmt.Errorf("result %d has invalid result_sha256", index+1)
		}
	}
	return nil
}

func validateRankedDocuments(documents []RankedDocument) error {
	seen := make(map[string]struct{}, len(documents))
	for index, document := range documents {
		if document.Rank != index+1 || strings.TrimSpace(document.DocumentID) == "" {
			return fmt.Errorf("document %d has invalid rank or id", index+1)
		}
		if _, duplicate := seen[document.DocumentID]; duplicate {
			return fmt.Errorf("document %d repeats id %q", index+1, document.DocumentID)
		}
		seen[document.DocumentID] = struct{}{}
		if document.SourcePath != "" {
			if err := validatePortablePath(document.SourcePath); err != nil {
				return fmt.Errorf("document %d source_path: %w", index+1, err)
			}
		}
		if !isHexSHA256(document.ContentSHA256) || !isHexSHA256(document.MetadataSHA256) {
			return fmt.Errorf("document %d has invalid content/metadata fingerprint", index+1)
		}
		if len(document.ScoreBits) != 16 || strings.ToLower(document.ScoreBits) != document.ScoreBits {
			return fmt.Errorf("document %d has invalid score_bits", index+1)
		}
		bits, err := strconv.ParseUint(document.ScoreBits, 16, 64)
		if err != nil || math.IsNaN(math.Float64frombits(bits)) || math.IsInf(math.Float64frombits(bits), 0) {
			return fmt.Errorf("document %d has non-finite score_bits", index+1)
		}
	}
	return nil
}

// CaseSetSHA256 returns the deterministic identity of an ordered case list.
func CaseSetSHA256(instanceIDs []string) (string, error) {
	if len(instanceIDs) == 0 {
		return "", errors.New("case set is empty")
	}
	copyIDs := append([]string(nil), instanceIDs...)
	for _, instanceID := range copyIDs {
		if err := validateInstanceID(instanceID); err != nil {
			return "", err
		}
	}
	if !slices.IsSorted(copyIDs) {
		return "", errors.New("case set must be sorted")
	}
	for index := 1; index < len(copyIDs); index++ {
		if copyIDs[index-1] == copyIDs[index] {
			return "", errors.New("case set contains duplicates")
		}
	}
	payload := struct {
		Domain      string   `json:"domain"`
		InstanceIDs []string `json:"instance_ids"`
	}{Domain: "retrieval-replay-case-set-v1", InstanceIDs: copyIDs}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// InputSHA256 returns the deterministic identity of one replay input. The
// expected result artifact and InputSHA256 field itself are intentionally
// excluded.
func InputSHA256(
	nativeManifestSHA string,
	engine EngineDescriptor,
	engineConfig ArtifactRef,
	spec CaseSpec,
) (string, error) {
	if !isHexSHA256(nativeManifestSHA) {
		return "", errors.New("invalid Native manifest SHA-256")
	}
	if err := validateEngineDescriptor(engine); err != nil {
		return "", err
	}
	if err := validateArtifactRef(engineConfig); err != nil {
		return "", err
	}
	cacheSHA := ""
	if spec.EmbeddingCache != nil {
		cacheSHA = spec.EmbeddingCache.SHA256
	}
	payload := struct {
		Domain                  string              `json:"domain"`
		NativeManifestSHA256    string              `json:"native_manifest_sha256"`
		Engine                  EngineDescriptor    `json:"engine"`
		EngineConfigSHA256      string              `json:"engine_config_sha256"`
		InstanceID              string              `json:"instance_id"`
		NativeArtifactSHA256    string              `json:"native_artifact_sha256"`
		ResponsesArtifactSHA256 string              `json:"responses_artifact_sha256"`
		CorpusSHA256            string              `json:"corpus_sha256"`
		QueriesSHA256           string              `json:"queries_sha256"`
		EmbeddingCacheSHA256    string              `json:"embedding_cache_sha256,omitempty"`
		Retrieval               RetrievalProvenance `json:"retrieval"`
	}{
		Domain: "retrieval-replay-input-v1", NativeManifestSHA256: nativeManifestSHA,
		Engine: engine, EngineConfigSHA256: engineConfig.SHA256,
		InstanceID: spec.InstanceID, NativeArtifactSHA256: spec.NativeArtifactSHA256,
		ResponsesArtifactSHA256: spec.ResponsesArtifactSHA256,
		CorpusSHA256:            spec.Corpus.SHA256, QueriesSHA256: spec.Queries.SHA256,
		EmbeddingCacheSHA256: cacheSHA, Retrieval: spec.Retrieval,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// ResultSHA256 fingerprints a successful exact ranked result for one query.
// It is retained as a convenience for callers constructing success fixtures.
func ResultSHA256(querySHA256 string, documents []RankedDocument) (string, error) {
	return OutcomeSHA256(querySHA256, "success", "", documents)
}

// OutcomeSHA256 fingerprints the exact search outcome. Error outcomes bind
// the recorded error text by digest and carry no documents; successful
// outcomes bind exact ranked documents. The no_hit status is a deterministic
// classification of the current knowledge tool's exact no-document errors;
// its error SHA-256 is still compared exactly.
func OutcomeSHA256(
	querySHA256, status, errorSHA256 string,
	documents []RankedDocument,
) (string, error) {
	if !isHexSHA256(querySHA256) {
		return "", errors.New("invalid query SHA-256")
	}
	if documents == nil {
		return "", errors.New("documents must be an array, not null")
	}
	if err := validateRankedDocuments(documents); err != nil {
		return "", err
	}
	switch status {
	case "success":
		if errorSHA256 != "" {
			return "", errors.New("successful result cannot carry an error SHA-256")
		}
		if len(documents) == 0 {
			return "", errors.New("successful result must contain at least one document")
		}
	case "no_hit", "error":
		if !isHexSHA256(errorSHA256) {
			return "", errors.New("error result requires a valid error SHA-256")
		}
		if len(documents) != 0 {
			return "", errors.New("error result cannot carry documents")
		}
	default:
		return "", fmt.Errorf("unsupported result status %q", status)
	}
	payload := struct {
		Domain      string           `json:"domain"`
		QuerySHA256 string           `json:"query_sha256"`
		Status      string           `json:"status"`
		ErrorSHA256 string           `json:"error_sha256,omitempty"`
		Documents   []RankedDocument `json:"documents"`
	}{
		Domain: "retrieval-replay-search-outcome-v1", QuerySHA256: querySHA256,
		Status: status, ErrorSHA256: errorSHA256, Documents: documents,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return digestBytes(data), nil
}

// RetrievalMetadataSHA256 fingerprints the portable metadata fields captured
// by the current Native retrieval_trace schema.
func RetrievalMetadataSHA256(sourcePath, lines, symbol string) string {
	payload := struct {
		Domain     string `json:"domain"`
		SourcePath string `json:"source_path"`
		Lines      string `json:"lines"`
		Symbol     string `json:"symbol"`
	}{
		Domain:     "retrieval-replay-document-metadata-v1",
		SourcePath: sourcePath,
		Lines:      lines,
		Symbol:     symbol,
	}
	data, _ := json.Marshal(payload)
	return digestBytes(data)
}
