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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	lmeResultArtifactMaxFileBytes  int64 = 64 << 20
	lmeResultArtifactMaxTotalBytes int64 = 512 << 20
	lmeResultArtifactMaxFiles            = 100_000
)

type lmeResultGovernanceDependencies struct {
	now         func() time.Time
	writeAtomic func(string, []byte, fs.FileMode) error
}

func defaultLMEResultGovernanceDependencies() lmeResultGovernanceDependencies {
	return lmeResultGovernanceDependencies{
		now:         time.Now,
		writeAtomic: writeLMEAtomicFile,
	}
}

func saveLMECheckpoint(outputDir string, result *lmeRunResult) error {
	data, err := marshalLMEJSON(result)
	if err != nil {
		return fmt.Errorf("marshal LongMemEval checkpoint: %w", err)
	}
	path := filepath.Join(outputDir, lmeCheckpointFileName)
	if err := writeLMEAtomicFile(path, data, 0644); err != nil {
		return fmt.Errorf("write LongMemEval checkpoint: %w", err)
	}
	return nil
}

func loadLMERunResult(outputDir string) (*lmeRunResult, error) {
	data, err := readLMEBoundedResultFile(
		filepath.Join(outputDir, lmeCheckpointFileName),
	)
	if err != nil {
		return nil, err
	}
	var result lmeRunResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("parse LongMemEval checkpoint: %w", err)
	}
	return &result, nil
}

func readLMEBoundedResultFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > lmeResultArtifactMaxFileBytes {
		return nil, fmt.Errorf("LongMemEval result must be a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("LongMemEval result changed while opening")
	}
	data, err := io.ReadAll(io.LimitReader(file, lmeResultArtifactMaxFileBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > lmeResultArtifactMaxFileBytes {
		return nil, fmt.Errorf("LongMemEval result exceeds the size limit")
	}
	return data, nil
}

// finalizeLMERunResult validates the fixed denominator and build traces before
// atomically publishing the maintained result artifacts.
func finalizeLMERunResult(
	outputDir string,
	result *lmeRunResult,
	expectedCaseIDs []string,
) error {
	return finalizeLMERunResultWithDependencies(
		outputDir,
		result,
		expectedCaseIDs,
		defaultLMEResultGovernanceDependencies(),
	)
}

func finalizeLMERunResultWithDependencies(
	outputDir string,
	result *lmeRunResult,
	expectedCaseIDs []string,
	deps lmeResultGovernanceDependencies,
) error {
	if deps.now == nil {
		deps.now = time.Now
	}
	if deps.writeAtomic == nil {
		deps.writeAtomic = writeLMEAtomicFile
	}
	markLMEMissingCases(result)
	manifest, traceArtifact, blockers := validateLMEMaintainedResult(
		outputDir,
		result,
		expectedCaseIDs,
	)
	denominator := newLMEFixedDenominator(expectedCaseIDs)
	publication := &lmePublication{
		SchemaVersion:    lmeResultSchemaVersion,
		Classification:   lmeResultClassMaintained,
		Origin:           lmeResultOriginNativeRunner,
		Eligible:         len(blockers) == 0,
		Blockers:         uniqueLMEBlockers(blockers),
		FixedDenominator: denominator,
	}
	if manifest != nil {
		publication.RunManifest = lmePublishedRunManifest{
			SchemaVersion:       manifest.SchemaVersion,
			CompatibilityDigest: manifest.CompatibilityDigest,
			ComparisonDigest:    manifest.ComparisonDigest,
		}
	}
	result.Publication = publication
	if len(publication.Blockers) > 0 {
		return &lmeEligibilityError{Blockers: publication.Blockers}
	}

	generated, err := buildLMEGeneratedResultArtifacts(result, publication)
	if err != nil {
		return err
	}
	publication.Artifacts = make(map[string]lmeResultArtifact, len(generated)+1)
	for i := range generated {
		name := versionedLMEArtifactName(generated[i].baseName, generated[i].data)
		publication.Artifacts[generated[i].key] = newLMEResultArtifact(name, generated[i].data)
	}
	if traceArtifact.Path != "" {
		publication.Artifacts["build_trace"] = traceArtifact
	}
	publication.FinalizedAt = deps.now().UTC()

	for _, artifact := range generated {
		published := publication.Artifacts[artifact.key]
		if err := deps.writeAtomic(filepath.Join(outputDir, published.Path), artifact.data, 0644); err != nil {
			return fmt.Errorf("publish LongMemEval artifact %s: %w", published.Path, err)
		}
	}
	resultData, err := marshalLMEJSON(result)
	if err != nil {
		return fmt.Errorf("marshal maintained LongMemEval result: %w", err)
	}
	if err := deps.writeAtomic(
		filepath.Join(outputDir, lmeResultsFileName),
		resultData,
		0644,
	); err != nil {
		return fmt.Errorf("publish maintained LongMemEval result: %w", err)
	}
	return nil
}

func markLMEMissingCases(result *lmeRunResult) {
	if result == nil {
		return
	}
	for _, record := range result.Cases {
		if record != nil && record.Status == lmeCaseStatusPending {
			record.Status = lmeCaseStatusMissing
		}
	}
}

func validateLMEMaintainedResult(
	outputDir string,
	result *lmeRunResult,
	expectedCaseIDs []string,
) (*lmeRunManifest, lmeResultArtifact, []string) {
	var blockers []string
	blockers = append(blockers, validateLMEFixedDenominator(result, expectedCaseIDs)...)
	blockers = append(blockers, validateLMECost(result)...)
	manifest, manifestBlockers := readAndValidateLMEEligibilityManifest(
		filepath.Join(outputDir, lmeRunManifestResultFileName),
		result,
		expectedCaseIDs,
	)
	blockers = append(blockers, manifestBlockers...)
	blockers = append(blockers, validateLMEInputArtifacts(
		filepath.Join(outputDir, lmeRunManifestResultFileName),
		result,
		manifest,
		expectedCaseIDs,
	)...)
	traceArtifact, traceBlockers := validateLMEBuildTrace(outputDir, result, expectedCaseIDs)
	blockers = append(blockers, traceBlockers...)
	return manifest, traceArtifact, uniqueLMEBlockers(blockers)
}

func validateLMEFixedDenominator(
	result *lmeRunResult,
	expectedCaseIDs []string,
) []string {
	if result == nil {
		return []string{"result is nil"}
	}
	if len(expectedCaseIDs) == 0 {
		return []string{"fixed denominator is empty"}
	}
	var blockers []string
	seen := make(map[string]int, len(result.Cases))
	actual := make([]string, 0, len(result.Cases))
	for _, record := range result.Cases {
		if record == nil {
			blockers = append(blockers, "case records contain a nil entry")
			continue
		}
		normalizeLMECaseStatus(record)
		seen[record.QuestionID]++
		actual = append(actual, record.QuestionID)
		if !isLMETerminalCaseStatus(record.Status) {
			blockers = append(blockers, fmt.Sprintf(
				"case %s has non-terminal status %q",
				record.QuestionID,
				record.Status,
			))
		}
	}
	for id, count := range seen {
		if count > 1 {
			blockers = append(blockers, fmt.Sprintf("case %s appears %d times", id, count))
		}
	}
	if !equalLMEStrings(actual, expectedCaseIDs) {
		missing, extra := differenceLMECaseIDs(expectedCaseIDs, actual)
		if len(missing) > 0 {
			blockers = append(blockers, "missing case IDs: "+strings.Join(missing, ", "))
		}
		if len(extra) > 0 {
			blockers = append(blockers, "extra case IDs: "+strings.Join(extra, ", "))
		}
		if len(missing) == 0 && len(extra) == 0 {
			blockers = append(blockers, "case ID order does not match the fixed manifest")
		}
	}
	if result.Summary == nil {
		blockers = append(blockers, "summary is missing")
	} else {
		if result.Summary.TotalCases != len(expectedCaseIDs) {
			blockers = append(blockers, fmt.Sprintf(
				"summary total_cases is %d, want %d",
				result.Summary.TotalCases,
				len(expectedCaseIDs),
			))
		}
		if result.Summary.CompletedCases != len(expectedCaseIDs) {
			blockers = append(blockers, fmt.Sprintf(
				"summary completed_cases is %d, want %d",
				result.Summary.CompletedCases,
				len(expectedCaseIDs),
			))
		}
		blockers = append(blockers, validateLMESummaryConsistency(
			result,
			len(expectedCaseIDs),
		)...)
	}
	return blockers
}

func readAndValidateLMEEligibilityManifest(
	path string,
	result *lmeRunResult,
	expectedCaseIDs []string,
) (*lmeRunManifest, []string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, []string{"immutable run manifest is unavailable"}
	}
	var manifest lmeRunManifest
	if err := lmeDecodeStrict(data, &manifest); err != nil {
		return nil, []string{"immutable run manifest is invalid or non-canonical JSON"}
	}
	blockers := validateLMEManifestIntegrity(&manifest)
	blockers = append(blockers, validateLMEManifestEligibility(
		&manifest,
		expectedCaseIDs,
	)...)
	blockers = append(blockers, validateLMEManifestResultIdentity(&manifest, result)...)
	return &manifest, uniqueLMEBlockers(blockers)
}

func validateLMEManifestIntegrity(manifest *lmeRunManifest) []string {
	var blockers []string
	if manifest.SchemaVersion != lmeRunManifestSchemaVersion {
		blockers = append(blockers, fmt.Sprintf(
			"run manifest schema version is %d, want %d",
			manifest.SchemaVersion,
			lmeRunManifestSchemaVersion,
		))
	}
	if !strings.HasPrefix(manifest.CompatibilityDigest, "sha256:") {
		blockers = append(blockers, "run compatibility digest is missing or invalid")
	} else if digest, err := calculateLMERunCompatibilityDigest(manifest); err != nil {
		blockers = append(blockers, "calculate run compatibility digest: "+err.Error())
	} else if digest != manifest.CompatibilityDigest {
		blockers = append(blockers, "run compatibility digest is invalid")
	}
	if !strings.HasPrefix(manifest.ComparisonDigest, "sha256:") {
		blockers = append(blockers, "run comparison digest is missing or invalid")
	} else if digest, err := calculateLMEComparisonDigest(manifest); err != nil {
		blockers = append(blockers, "calculate run comparison digest: "+err.Error())
	} else if digest != manifest.ComparisonDigest {
		blockers = append(blockers, "run comparison digest is invalid")
	}
	if err := validateLMERunManifestDeclaration(manifest); err != nil {
		blockers = append(blockers, err.Error())
	}
	return blockers
}

func validateLMEManifestEligibility(
	manifest *lmeRunManifest,
	expectedCaseIDs []string,
) []string {
	var blockers []string
	if !manifest.Reproducible || manifest.OfficialStatus != lmeOfficialStatusEligible {
		blockers = append(blockers, "run provenance is not clean and reproducible")
	}
	blockers = append(blockers, manifest.OfficialBlockers...)
	if !equalLMEStrings(manifest.CaseIDs, expectedCaseIDs) {
		blockers = append(blockers, "run manifest case IDs/order do not match the fixed denominator")
	}
	blockers = append(blockers, validateLMEManifestArtifacts(manifest.Artifacts)...)
	if manifest.Run.EffectiveTopK != lmeRetrievalTopK {
		blockers = append(blockers, fmt.Sprintf(
			"effective retrieval top-k is %d, want %d",
			manifest.Run.EffectiveTopK,
			lmeRetrievalTopK,
		))
	}
	if manifest.Run.BuildProtocol != lmeBuildProtocol {
		blockers = append(blockers, fmt.Sprintf(
			"unsupported build protocol %q",
			manifest.Run.BuildProtocol,
		))
	}
	return blockers
}

func validateLMEManifestArtifacts(artifacts lmeArtifactSet) []string {
	var blockers []string
	for _, artifact := range []struct {
		name         string
		value        lmeArtifactProvenance
		pathRequired bool
	}{
		{name: "dataset", value: artifacts.Dataset},
		{name: "case manifest", value: artifacts.CaseManifest},
		{name: "canonical replay", value: artifacts.CanonicalReplay, pathRequired: true},
		{name: "build plan", value: artifacts.BuildPlan, pathRequired: true},
	} {
		if !artifact.value.Configured || !artifact.value.Available ||
			!strings.HasPrefix(artifact.value.Digest, "sha256:") ||
			(artifact.pathRequired &&
				(artifact.value.Path == "" || filepath.IsAbs(artifact.value.Path))) {
			blockers = append(blockers, artifact.name+" digest is unavailable")
		}
	}
	return blockers
}

func validateLMEManifestResultIdentity(
	manifest *lmeRunManifest,
	result *lmeRunResult,
) []string {
	if result == nil || result.Metadata == nil {
		return nil
	}
	metadata := result.Metadata
	var blockers []string
	if manifest.Run.Scenario != metadata.Scenario {
		blockers = append(blockers, "result scenario does not match run manifest")
	}
	if manifest.Run.Backend != metadata.MemoryBackend {
		blockers = append(blockers, "result backend does not match run manifest")
	}
	if manifest.Run.ModelName != metadata.Config.ModelName {
		blockers = append(blockers, "result model does not match run manifest")
	}
	if manifest.Run.EmbedModelName != metadata.Config.EmbedModelName {
		blockers = append(blockers, "result embedding model does not match run manifest")
	}
	if metadata.Config.RetrievalTopK > 0 &&
		manifest.Run.EffectiveTopK != metadata.Config.RetrievalTopK {
		blockers = append(blockers, "result retrieval top-k does not match run manifest")
	}
	if metadata.RunManifestVersion != manifest.SchemaVersion {
		blockers = append(blockers, "result run manifest schema does not match provenance")
	}
	if metadata.RunCompatibility != manifest.CompatibilityDigest {
		blockers = append(blockers, "result metadata does not match run compatibility digest")
	}
	if metadata.RunComparison != manifest.ComparisonDigest {
		blockers = append(blockers, "result metadata does not match run comparison digest")
	}
	if manifest.Run.BackendVersion !=
		sanitizeProvenanceIdentifier(metadata.Config.Mem0Version) {
		blockers = append(blockers, "result backend version does not match run manifest")
	}
	if manifest.Run.BackendRevision !=
		sanitizeProvenanceIdentifier(metadata.Config.Mem0Revision) {
		blockers = append(blockers, "result backend revision does not match run manifest")
	}
	if lmeConfigString(manifest.Config, "trace_content_mode") !=
		string(metadata.Config.TraceContentMode) {
		blockers = append(blockers, "result trace content mode does not match run manifest")
	}
	blockers = append(blockers, validateLMEMemoryBuildIdentity(manifest, metadata)...)
	return blockers
}

func validateLMEMemoryBuildIdentity(
	manifest *lmeRunManifest,
	metadata *lmeMetadata,
) []string {
	if metadata.MemoryBuild == nil {
		return []string{"result memory-build metadata is missing"}
	}
	memoryBuild := metadata.MemoryBuild
	var blockers []string
	if lmeConfigString(memoryBuild, "protocol") != manifest.Run.BuildProtocol {
		blockers = append(blockers, "result memory-build protocol does not match run manifest")
	}
	if lmeConfigString(memoryBuild, "temporal_context") != manifest.Run.TemporalContext {
		blockers = append(blockers, "result temporal context does not match run manifest")
	}
	for _, key := range []string{
		"temporal_reference_source",
		"temporal_reference_format",
	} {
		if lmeConfigString(memoryBuild, key) != lmeConfigString(manifest.Config, key) {
			blockers = append(blockers, "result "+key+" does not match run manifest")
		}
	}
	if manifest.Run.Scenario != "mem0_oss" {
		return blockers
	}
	if !lmeConfigBool(memoryBuild, "custom_extraction_prompt") {
		blockers = append(blockers, "result does not confirm the Mem0 OSS custom extraction prompt")
	}
	if !lmeConfigBool(memoryBuild, "observation_prompt_verified") {
		blockers = append(blockers, "result does not confirm the Mem0 OSS observation-prompt capability")
	}
	for _, pair := range [][2]string{
		{"preflight_digest", "mem0_preflight_digest"},
		{"environment_lock_digest", "mem0_environment_lock_digest"},
	} {
		if lmeConfigString(memoryBuild, pair[0]) != lmeConfigString(manifest.Config, pair[1]) {
			blockers = append(blockers, "result "+pair[0]+" does not match run manifest")
		}
	}
	return blockers
}

func validateLMECost(result *lmeRunResult) []string {
	if result == nil || result.Cost == nil {
		return []string{"phase-level cost artifact is missing"}
	}
	if result.Cost.Partial {
		return []string{"phase-level cost artifact is partial: " + result.Cost.PartialReason}
	}
	var blockers []string
	buckets := []struct {
		name   string
		bucket lmeCostBucket
	}{
		{name: "llm.total", bucket: result.Cost.LLM.Total},
		{name: "llm.memory_build", bucket: result.Cost.LLM.MemoryBuild},
		{name: "llm.qa", bucket: result.Cost.LLM.QA},
		{name: "llm.judge", bucket: result.Cost.LLM.Judge},
		{name: "embedding.total", bucket: result.Cost.Embedding.Total},
		{name: "embedding.memory_build", bucket: result.Cost.Embedding.MemoryBuild},
		{name: "embedding.qa_retrieval", bucket: result.Cost.Embedding.QARetrieval},
	}
	for _, item := range buckets {
		if item.bucket.Calls < 0 || item.bucket.Requests < 0 ||
			item.bucket.CacheHits < 0 || item.bucket.PromptTokens < 0 ||
			item.bucket.CompletionTokens < 0 || item.bucket.TotalTokens < 0 ||
			item.bucket.CachedTokens < 0 {
			blockers = append(blockers, item.name+" contains negative usage")
		}
		if item.bucket.Calls == 0 &&
			(item.bucket.PromptTokens != 0 || item.bucket.CompletionTokens != 0 ||
				item.bucket.TotalTokens != 0 || item.bucket.CachedTokens != 0) {
			blockers = append(blockers, item.name+" has tokens without provider calls")
		}
	}
	return blockers
}

func validateRawLMECostSchema(data []byte) []string {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return []string{"result JSON is invalid: " + err.Error()}
	}
	cost, ok := root["cost"].(map[string]any)
	if !ok {
		return []string{"phase-level cost artifact is missing"}
	}
	paths := [][2]string{
		{"llm", "total"},
		{"llm", "memory_build"},
		{"llm", "qa"},
		{"llm", "judge"},
		{"embedding", "total"},
		{"embedding", "memory_build"},
		{"embedding", "qa_retrieval"},
	}
	var blockers []string
	for _, path := range paths {
		modality, ok := cost[path[0]].(map[string]any)
		if !ok {
			blockers = append(blockers, "cost modality is missing: "+path[0])
			continue
		}
		bucket, ok := modality[path[1]].(map[string]any)
		if !ok {
			blockers = append(blockers, "cost bucket is missing: "+path[0]+"."+path[1])
			continue
		}
		if _, ok := bucket["tokens_known"].(bool); !ok {
			blockers = append(blockers, "cost bucket lacks explicit tokens_known: "+path[0]+"."+path[1])
		}
	}
	return uniqueLMEBlockers(blockers)
}

func validateLMEResultArtifact(root string, artifact lmeResultArtifact) error {
	if artifact.Path == "" || artifact.SHA256 == "" {
		return fmt.Errorf("path or digest is empty")
	}
	artifactPath, err := resolveLMEInputLocator(root, artifact.Path)
	if err != nil {
		return fmt.Errorf("resolve result artifact: %w", err)
	}
	digest, err := digestLMEPath(artifactPath)
	if err != nil {
		return err
	}
	if digest != artifact.SHA256 {
		return fmt.Errorf("digest mismatch")
	}
	return nil
}

func newLMEFixedDenominator(caseIDs []string) lmeFixedDenominator {
	copyIDs := append([]string(nil), caseIDs...)
	data, _ := json.Marshal(copyIDs)
	digest := sha256.Sum256(data)
	return lmeFixedDenominator{
		TotalCases: len(copyIDs),
		CaseIDs:    copyIDs,
		Digest:     "sha256:" + hex.EncodeToString(digest[:]),
	}
}

func newLMEResultArtifact(path string, data []byte) lmeResultArtifact {
	digest := sha256.Sum256(data)
	return lmeResultArtifact{
		Path:   path,
		SHA256: "sha256:" + hex.EncodeToString(digest[:]),
	}
}

func versionedLMEArtifactName(base string, data []byte) string {
	digest := sha256.Sum256(data)
	extension := filepath.Ext(base)
	name := strings.TrimSuffix(base, extension)
	return fmt.Sprintf("%s.%s%s", name, hex.EncodeToString(digest[:6]), extension)
}

func digestLMEPath(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("symbolic links are not valid result artifacts")
	}
	hash := sha256.New()
	if info.Mode().IsRegular() {
		if _, err := hashBoundedLMEFile(hash, path, lmeResultArtifactMaxFileBytes); err != nil {
			return "", err
		}
		return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
	}
	if !info.IsDir() {
		return "", fmt.Errorf("result artifact must be a regular file or directory")
	}
	var files []string
	if err := filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic link %s is not a valid result artifact", current)
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported result artifact file type: %s", current)
		}
		files = append(files, current)
		if len(files) > lmeResultArtifactMaxFiles {
			return fmt.Errorf("result artifact exceeds the file-count limit")
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(files)
	var totalBytes int64
	for _, current := range files {
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return "", err
		}
		if _, err := io.WriteString(hash, filepath.ToSlash(relative)+"\x00"); err != nil {
			return "", err
		}
		remaining := lmeResultArtifactMaxTotalBytes - totalBytes
		if remaining <= 0 {
			return "", fmt.Errorf("result artifact exceeds the total-size limit")
		}
		limit := min(lmeResultArtifactMaxFileBytes, remaining)
		written, err := hashBoundedLMEFile(hash, current, limit)
		if err != nil {
			return "", err
		}
		totalBytes += written
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), nil
}

func hashBoundedLMEFile(writer io.Writer, path string, limit int64) (int64, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if !before.Mode().IsRegular() {
		return 0, fmt.Errorf("result artifact file is not regular: %s", path)
	}
	if before.Size() > limit {
		return 0, fmt.Errorf("result artifact file exceeds the size limit: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	after, statErr := file.Stat()
	if statErr != nil {
		_ = file.Close()
		return 0, statErr
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) {
		_ = file.Close()
		return 0, fmt.Errorf("result artifact file changed while opening: %s", path)
	}
	written, copyErr := io.CopyN(writer, file, limit+1)
	closeErr := file.Close()
	if copyErr != nil && copyErr != io.EOF {
		return written, copyErr
	}
	if written > limit {
		return written, fmt.Errorf("result artifact file exceeds the size limit: %s", path)
	}
	if closeErr != nil {
		return written, closeErr
	}
	return written, nil
}

func marshalLMEJSON(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func differenceLMECaseIDs(expected, actual []string) ([]string, []string) {
	expectedCount := make(map[string]int, len(expected))
	actualCount := make(map[string]int, len(actual))
	for _, id := range expected {
		expectedCount[id]++
	}
	for _, id := range actual {
		actualCount[id]++
	}
	var missing, extra []string
	for id, count := range expectedCount {
		if actualCount[id] < count {
			missing = append(missing, id)
		}
	}
	for id, count := range actualCount {
		if expectedCount[id] < count {
			extra = append(extra, id)
		}
	}
	sort.Strings(missing)
	sort.Strings(extra)
	return missing, extra
}

func equalLMEStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func uniqueLMEBlockers(blockers []string) []string {
	seen := make(map[string]struct{}, len(blockers))
	out := make([]string, 0, len(blockers))
	for _, blocker := range blockers {
		blocker = strings.TrimSpace(blocker)
		if blocker == "" {
			continue
		}
		if _, ok := seen[blocker]; ok {
			continue
		}
		seen[blocker] = struct{}{}
		out = append(out, blocker)
	}
	sort.Strings(out)
	return out
}

func joinLMEBlockers(blockers []string) string {
	return strings.Join(uniqueLMEBlockers(blockers), "; ")
}

var lmeResultSecretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+@`),
	regexp.MustCompile(`(?i)(authorization\s*[:=]\s*(?:bearer\s+)?)[^\s,;"']+`),
	regexp.MustCompile(`(?i)((?:api[_ -]?key|access[_ -]?token|secret|password)\s*[:=]\s*)[^\s,;"']+`),
	regexp.MustCompile(`(?i)(bearer\s+)[a-z0-9._~+/=-]+`),
}

func sanitizeLMEResultText(value string, limit int) string {
	value = strings.TrimSpace(value)
	for _, pattern := range lmeResultSecretPatterns {
		value = pattern.ReplaceAllString(value, "${1}[REDACTED]")
	}
	if limit > 0 && len(value) > limit {
		value = value[:limit]
	}
	return value
}

// Publication artifact validation.

func validatePublishedLMEResult(path string, result *lmeRunResult) error {
	if result == nil || result.Publication == nil {
		return &lmeEligibilityError{Blockers: []string{
			"publication metadata is missing; result is diagnostic only",
		}}
	}
	root := filepath.Dir(path)
	publication := result.Publication
	blockers := validateLMEPublicationDeclaration(result, publication)
	blockers = append(blockers, validateLMEPublicationArtifacts(root, result, publication)...)
	blockers = append(blockers, validateLMEPublicationManifest(root, result, publication)...)
	blockers = uniqueLMEBlockers(blockers)
	if len(blockers) > 0 {
		return &lmeEligibilityError{Blockers: blockers}
	}
	return nil
}

func validateLMEPublicationDeclaration(
	result *lmeRunResult,
	publication *lmePublication,
) []string {
	var blockers []string
	if publication.SchemaVersion != lmeResultSchemaVersion {
		blockers = append(blockers, fmt.Sprintf(
			"unsupported publication schema %d",
			publication.SchemaVersion,
		))
	}
	if publication.Classification != lmeResultClassMaintained {
		blockers = append(blockers, "result classification is diagnostic, not maintained")
	}
	if publication.Origin != lmeResultOriginNativeRunner {
		blockers = append(blockers, "result origin is not the native runner")
	}
	if !publication.Eligible || len(publication.Blockers) > 0 {
		blockers = append(blockers, publication.Blockers...)
		if !publication.Eligible && len(publication.Blockers) == 0 {
			blockers = append(blockers, "result publication is marked ineligible")
		}
	}
	blockers = append(blockers, validateLMEFixedDenominator(
		result,
		publication.FixedDenominator.CaseIDs,
	)...)
	if publication.FixedDenominator.TotalCases != len(publication.FixedDenominator.CaseIDs) {
		blockers = append(blockers, "fixed denominator total does not match case IDs")
	}
	if publication.FixedDenominator.Digest !=
		newLMEFixedDenominator(publication.FixedDenominator.CaseIDs).Digest {
		blockers = append(blockers, "fixed denominator digest is invalid")
	}
	return append(blockers, validateLMECost(result)...)
}

func validateLMEPublicationArtifacts(
	root string,
	result *lmeRunResult,
	publication *lmePublication,
) []string {
	blockers := validateLMEResultArtifacts(root, publication.Artifacts)
	blockers = append(blockers, validateLMEGeneratedArtifacts(
		result,
		publication.Artifacts,
	)...)
	if result.Metadata == nil ||
		(result.Metadata.Scenario != "auto" && result.Metadata.Scenario != "mem0_oss") {
		return blockers
	}
	traceArtifact, ok := publication.Artifacts["build_trace"]
	if !ok {
		return append(blockers, "required build_trace artifact is missing")
	}
	return append(blockers, validateLMEPublishedBuildTrace(
		root,
		result,
		publication.FixedDenominator.CaseIDs,
		traceArtifact,
	)...)
}

func validateLMEPublicationManifest(
	root string,
	result *lmeRunResult,
	publication *lmePublication,
) []string {
	manifestPath := filepath.Join(root, lmeRunManifestResultFileName)
	manifest, blockers := readAndValidateLMEEligibilityManifest(
		manifestPath,
		result,
		publication.FixedDenominator.CaseIDs,
	)
	blockers = append(blockers, validateLMEInputArtifacts(
		manifestPath,
		result,
		manifest,
		publication.FixedDenominator.CaseIDs,
	)...)
	if manifest == nil {
		return blockers
	}
	if publication.RunManifest.CompatibilityDigest != manifest.CompatibilityDigest {
		blockers = append(blockers, "published result does not match run manifest compatibility digest")
	}
	if publication.RunManifest.SchemaVersion != manifest.SchemaVersion {
		blockers = append(blockers, "published result does not match run manifest schema version")
	}
	if publication.RunManifest.ComparisonDigest != manifest.ComparisonDigest {
		blockers = append(blockers, "published result does not match run manifest comparison digest")
	}
	return blockers
}

func validateLMEGeneratedArtifacts(
	result *lmeRunResult,
	artifacts map[string]lmeResultArtifact,
) []string {
	if result == nil || result.Publication == nil {
		return []string{"publication metadata is missing"}
	}
	expected, err := buildLMEGeneratedResultArtifacts(result, result.Publication)
	if err != nil {
		return []string{"rebuild generated result artifacts: " + err.Error()}
	}
	var blockers []string
	for _, generated := range expected {
		artifact, ok := artifacts[generated.key]
		if !ok {
			continue
		}
		if artifact.SHA256 != newLMEResultArtifact("", generated.data).SHA256 {
			blockers = append(blockers, fmt.Sprintf(
				"%s artifact does not match the published result",
				generated.key,
			))
		}
	}
	return blockers
}

func validateLMEResultArtifacts(
	root string,
	artifacts map[string]lmeResultArtifact,
) []string {
	required := []string{"aggregate", "bad_cases", "bad_cases_en", "bad_cases_zh_cn"}
	var blockers []string
	for _, name := range required {
		artifact, ok := artifacts[name]
		if !ok {
			blockers = append(blockers, "required result artifact is missing: "+name)
			continue
		}
		if err := validateLMEResultArtifact(root, artifact); err != nil {
			blockers = append(blockers, fmt.Sprintf("invalid %s artifact: %v", name, err))
		}
	}
	if artifact, ok := artifacts["build_trace"]; ok {
		if err := validateLMEResultArtifact(root, artifact); err != nil {
			blockers = append(blockers, "invalid build_trace artifact: "+err.Error())
		}
	}
	return blockers
}

type lmeResolvedInputArtifacts struct {
	replay    string
	buildPlan string
}

func validateLMEInputArtifacts(
	manifestPath string,
	result *lmeRunResult,
	manifest *lmeRunManifest,
	expectedCaseIDs []string,
) []string {
	if result == nil || result.Metadata == nil {
		return []string{"result metadata is unavailable for input validation"}
	}
	if result.Metadata.Scenario != "auto" && result.Metadata.Scenario != "mem0_oss" {
		return nil
	}
	if manifest == nil {
		return []string{"run manifest is unavailable for input validation"}
	}
	resolved, blockers := resolveAndDigestLMEInputs(manifestPath, manifest)
	if len(blockers) > 0 {
		return blockers
	}
	replay, err := loadLMEReplayCorpus(resolved.replay)
	if err != nil {
		return []string{"canonical replay content validation failed"}
	}
	plan, err := loadLMEBuildPlan(resolved.buildPlan)
	if err != nil {
		return []string{"build plan content validation failed"}
	}
	blockers = append(blockers, validateLMEInputPlanMetadata(
		result.Metadata.Config,
		manifest,
		replay,
		plan,
	)...)
	blockers = append(blockers, validateLMEInputLineage(
		manifest,
		replay,
		plan,
		expectedCaseIDs,
	)...)
	return uniqueLMEBlockers(blockers)
}

func validateLMEInputPlanMetadata(
	cfg lmeRunConfig,
	manifest *lmeRunManifest,
	replay *lmeReplayCorpus,
	plan *lmeBuildPlanCorpus,
) []string {
	var blockers []string
	if err := verifyLMEBuildPlanSource(plan, replay); err != nil {
		blockers = append(blockers, "build plan does not match canonical replay")
	}
	if replay.Index.ReplayDigest != cfg.ReplayDigest {
		blockers = append(blockers, "canonical replay logical digest changed before validation")
	}
	if plan.Index.BuildPlanDigest != cfg.BuildPlanDigest {
		blockers = append(blockers, "build plan logical digest changed before validation")
	}
	if plan.Index.Config.ReplayDigest != replay.Index.ReplayDigest {
		blockers = append(blockers, "build plan replay digest does not match canonical replay")
	}
	if plan.Index.Protocol != lmeBuildProtocol {
		blockers = append(blockers, fmt.Sprintf(
			"build plan protocol is %q, want %q",
			plan.Index.Protocol,
			lmeBuildProtocol,
		))
	}
	if plan.Index.Config.MaxTokens != cfg.BuildMaxTokens {
		blockers = append(blockers, fmt.Sprintf(
			"build plan max tokens is %d, want %d",
			plan.Index.Config.MaxTokens,
			cfg.BuildMaxTokens,
		))
	}
	if plan.Index.Config.Tokenizer != cfg.BuildTokenizer ||
		plan.Index.Config.Model != cfg.BuildTokenizerModel ||
		plan.Index.Config.Encoding != cfg.BuildTokenizerEncoding {
		blockers = append(blockers, "build plan tokenizer configuration does not match result metadata")
	}
	if plan.Index.Stats != cfg.BuildStats {
		blockers = append(blockers, "build plan chunking statistics do not match result metadata")
	}
	if !equalLMEJSON(manifest.Config["build_stats"], plan.Index.Stats) {
		blockers = append(blockers, "build plan chunking statistics do not match run provenance")
	}
	if manifest.Run.BuildProtocol != lmeBuildProtocol {
		blockers = append(blockers, "run provenance build protocol is not turn-pair")
	}
	return blockers
}

func validateLMEInputLineage(
	manifest *lmeRunManifest,
	replay *lmeReplayCorpus,
	plan *lmeBuildPlanCorpus,
	expectedCaseIDs []string,
) []string {
	var blockers []string
	if !equalLMEStrings(lmeArtifactCaseIDs(replay.Index.Cases), expectedCaseIDs) {
		blockers = append(blockers, "canonical replay case IDs/order do not match the fixed denominator")
	}
	if !equalLMEStrings(lmeArtifactCaseIDs(plan.Index.Cases), expectedCaseIDs) {
		blockers = append(blockers, "build plan case IDs/order do not match the fixed denominator")
	}
	if manifest.Artifacts.Dataset.Digest != lmeDigestAlgorithm+":"+replay.Index.DatasetDigest {
		blockers = append(blockers, "canonical replay dataset digest does not match run provenance")
	}
	if manifest.Artifacts.CaseManifest.Digest != lmeDigestAlgorithm+":"+replay.Index.ManifestDigest {
		blockers = append(blockers, "canonical replay manifest digest does not match run provenance")
	}
	return blockers
}

func resolveAndDigestLMEInputs(
	manifestPath string,
	manifest *lmeRunManifest,
) (lmeResolvedInputArtifacts, []string) {
	if manifest == nil {
		return lmeResolvedInputArtifacts{}, []string{"run manifest is unavailable"}
	}
	base := filepath.Dir(filepath.Dir(manifestPath))
	artifacts := []struct {
		name  string
		value lmeArtifactProvenance
		set   func(*lmeResolvedInputArtifacts, string)
	}{
		{name: "canonical replay", value: manifest.Artifacts.CanonicalReplay, set: func(paths *lmeResolvedInputArtifacts, path string) {
			paths.replay = path
		}},
		{name: "build plan", value: manifest.Artifacts.BuildPlan, set: func(paths *lmeResolvedInputArtifacts, path string) {
			paths.buildPlan = path
		}},
	}
	var resolved lmeResolvedInputArtifacts
	var blockers []string
	for _, item := range artifacts {
		path, err := resolveLMEInputLocator(base, item.value.Path)
		if err != nil {
			blockers = append(blockers, item.name+" artifact locator is invalid")
			continue
		}
		item.set(&resolved, path)
		digest, err := digestLMEArtifact(path)
		if err != nil {
			blockers = append(blockers, item.name+" artifact cannot be rehashed")
			continue
		}
		actual := lmeDigestAlgorithm + ":" + digest
		if actual != item.value.Digest {
			blockers = append(blockers, item.name+" artifact digest does not match the actual content")
		}
	}
	return resolved, uniqueLMEBlockers(blockers)
}

func resolveLMEInputLocator(base, locator string) (string, error) {
	locator = strings.TrimSpace(locator)
	if locator == "" || filepath.IsAbs(locator) || filepath.VolumeName(locator) != "" ||
		hasLMEForeignVolume(locator) {
		return "", fmt.Errorf("input artifact locator must be a non-empty relative path")
	}
	absBase, err := filepath.Abs(base)
	if err != nil {
		return "", fmt.Errorf("resolve input artifact base: %w", err)
	}
	absBase = filepath.Clean(absBase)
	clean := filepath.Clean(filepath.FromSlash(locator))
	if clean == "." {
		return "", fmt.Errorf("input artifact locator must identify a child artifact")
	}
	candidate := filepath.Clean(filepath.Join(absBase, clean))
	relative, err := filepath.Rel(absBase, candidate)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("input artifact locator escapes its base")
	}
	if err := rejectLMESymlinkPath(absBase, candidate); err != nil {
		return "", err
	}
	return candidate, nil
}

func hasLMEForeignVolume(path string) bool {
	if strings.HasPrefix(path, `\\`) || strings.HasPrefix(path, `//`) {
		return true
	}
	return len(path) >= 2 && unicode.IsLetter(rune(path[0])) && path[1] == ':'
}

func rejectLMESymlinkPath(base, candidate string) error {
	baseInfo, err := os.Lstat(base)
	if err == nil && baseInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("input artifact base must not be a symbolic link")
	}
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("inspect input artifact base: %w", err)
	}
	relative, err := filepath.Rel(base, candidate)
	if err != nil {
		return fmt.Errorf("inspect input artifact locator: %w", err)
	}
	current := base
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect input artifact locator: %w", err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("input artifact locator must not traverse symbolic links")
		}
	}
	return nil
}

func lmeArtifactCaseIDs(entries []lmeArtifactEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.CaseID)
	}
	return ids
}

// Atomic artifact persistence.

type lmeAtomicFileOperations struct {
	createTemp func(string, string) (*os.File, error)
	rename     func(string, string) error
	remove     func(string) error
	syncDir    func(string) error
}

func defaultLMEAtomicFileOperations() lmeAtomicFileOperations {
	return lmeAtomicFileOperations{
		createTemp: os.CreateTemp,
		rename:     os.Rename,
		remove:     os.Remove,
		syncDir:    syncLMEOutputDirectory,
	}
}

func writeLMEAtomicFile(path string, data []byte, perm fs.FileMode) error {
	return writeLMEAtomicFileWithOperations(
		path,
		data,
		perm,
		defaultLMEAtomicFileOperations(),
	)
}

func writeLMEAtomicFileWithOperations(
	path string,
	data []byte,
	perm fs.FileMode,
	ops lmeAtomicFileOperations,
) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("create LongMemEval output directory: %w", err)
	}
	temp, err := ops.createTemp(dir, ".lme-output-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary output for %s: %w", path, err)
	}
	tempPath := temp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = ops.remove(tempPath)
		}
	}()
	if err := temp.Chmod(perm); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set temporary output permissions for %s: %w", path, err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write temporary output for %s: %w", path, err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync temporary output for %s: %w", path, err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary output for %s: %w", path, err)
	}
	if err := ops.rename(tempPath, path); err != nil {
		return fmt.Errorf("publish LongMemEval output %s: %w", path, err)
	}
	removeTemp = false
	if err := ops.syncDir(dir); err != nil {
		return fmt.Errorf("sync LongMemEval output directory %s: %w", dir, err)
	}
	return nil
}

func syncLMEOutputDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
