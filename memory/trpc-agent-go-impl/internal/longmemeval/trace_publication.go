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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

const lmeTraceSelectionSchemaVersion = "longmemeval.build_trace_selection/v1"

type lmeTraceSelectionIndex struct {
	SchemaVersion string                   `json:"schema_version"`
	Purpose       string                   `json:"purpose"`
	Comparability string                   `json:"comparability"`
	Scenario      string                   `json:"scenario"`
	Backend       string                   `json:"backend,omitempty"`
	ContentMode   lmeTraceContentMode      `json:"content_mode"`
	Cases         []lmeTraceSelectionEntry `json:"cases"`
}

type lmeTraceSelectionEntry struct {
	CaseID string `json:"case_id"`
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
}

type lmeTraceAttempt struct {
	number int
	path   string
	gzip   bool
}

type lmeSelectedTrace struct {
	caseID  string
	path    string
	file    string
	data    []byte
	digest  string
	summary *lmeTraceOutcomeSummary
}

// validateLMEBuildTrace admits only complete, validated attempts into the
// maintained publication artifact.
func validateLMEBuildTrace(
	outputDir string,
	result *lmeRunResult,
	expectedCaseIDs []string,
) (lmeResultArtifact, []string) {
	if result == nil || result.Metadata == nil ||
		(result.Metadata.Scenario != "auto" && result.Metadata.Scenario != "mem0_oss") {
		return lmeResultArtifact{}, nil
	}
	mode := result.Metadata.Config.TraceContentMode
	if mode != lmeTraceContentHash && mode != lmeTraceContentNone {
		return lmeResultArtifact{}, []string{
			"maintained build traces require hash or none content mode",
		}
	}
	plan, err := loadLMETraceBuildPlan(outputDir)
	if err != nil {
		return lmeResultArtifact{}, []string{
			"immutable build plan is unavailable for trace validation",
		}
	}
	attempts, blockers := discoverLMETraceAttempts(outputDir, expectedCaseIDs)
	caseResults := make(map[string]*lmeCaseResult, len(result.Cases))
	for _, record := range result.Cases {
		if record != nil {
			caseResults[record.QuestionID] = record
		}
	}
	selected := make([]lmeSelectedTrace, 0, len(expectedCaseIDs))
	for _, caseID := range expectedCaseIDs {
		casePlan := plan.Cases[caseID]
		if casePlan == nil {
			blockers = append(blockers, "build plan is missing trace case "+caseID)
			continue
		}
		candidate, ok := selectLMETraceAttempt(
			caseID,
			mode,
			caseResults[caseID],
			casePlan,
			attempts[caseID],
		)
		if !ok {
			blockers = append(blockers, "no validated build trace attempt for "+caseID)
			continue
		}
		selected = append(selected, candidate)
	}
	if len(blockers) > 0 {
		return lmeResultArtifact{}, uniqueLMEBlockers(blockers)
	}
	artifact, err := publishLMETraceSelection(outputDir, result.Metadata, selected)
	if err != nil {
		return lmeResultArtifact{}, []string{"publish validated build trace selection failed"}
	}
	return artifact, nil
}

func discoverLMETraceAttempts(
	outputDir string,
	expectedCaseIDs []string,
) (map[string][]lmeTraceAttempt, []string) {
	root := filepath.Join(outputDir, "build_trace")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, []string{"required build trace is unavailable"}
	}
	expectedByBase := make(map[string]string, len(expectedCaseIDs))
	var blockers []string
	for _, caseID := range expectedCaseIDs {
		base := lmeTraceFileName(caseID)
		if previous, ok := expectedByBase[base]; ok && previous != caseID {
			blockers = append(blockers, "case IDs collide after build trace filename sanitization")
			continue
		}
		expectedByBase[base] = caseID
	}
	attempts := make(map[string][]lmeTraceAttempt, len(expectedCaseIDs))
	for _, entry := range entries {
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), "maintained-") ||
				strings.HasPrefix(entry.Name(), ".maintained-") {
				continue
			}
			blockers = append(blockers, "unexpected directory in local build trace workspace")
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			blockers = append(blockers, "build trace attempts must be regular files")
			continue
		}
		base, attempt, ok := parseLMETraceAttemptFile(entry.Name())
		if !ok {
			blockers = append(blockers, "unexpected file in local build trace workspace")
			continue
		}
		caseID, ok := expectedByBase[base]
		if !ok {
			blockers = append(blockers, "build trace contains a case outside the fixed denominator")
			continue
		}
		attempts[caseID] = append(attempts[caseID], lmeTraceAttempt{
			number: attempt,
			path:   filepath.Join(root, entry.Name()),
			gzip:   strings.HasSuffix(entry.Name(), ".gz"),
		})
	}
	for caseID := range attempts {
		sort.Slice(attempts[caseID], func(i, j int) bool {
			return attempts[caseID][i].number > attempts[caseID][j].number
		})
	}
	return attempts, uniqueLMEBlockers(blockers)
}

func selectLMETraceAttempt(
	caseID string,
	mode lmeTraceContentMode,
	record *lmeCaseResult,
	casePlan *lmeBuildCasePlan,
	attempts []lmeTraceAttempt,
) (lmeSelectedTrace, bool) {
	for _, attempt := range attempts {
		summary, err := readLMETraceOutcome(attempt.path)
		if err != nil || summary.CaseID != caseID || summary.ContentMode != mode {
			continue
		}
		if err := validateLMETraceBuildSources(casePlan, summary); err != nil {
			continue
		}
		if err := validateLMETraceOutcome(record, summary); err != nil {
			continue
		}
		data, err := os.ReadFile(attempt.path)
		if err != nil {
			continue
		}
		file := lmeTraceFileName(caseID) + ".jsonl"
		if attempt.gzip {
			file += ".gz"
		}
		return lmeSelectedTrace{
			caseID:  caseID,
			path:    attempt.path,
			file:    file,
			data:    data,
			digest:  lmeDigestAlgorithm + ":" + lmeSHA256(string(data)),
			summary: summary,
		}, true
	}
	return lmeSelectedTrace{}, false
}

func publishLMETraceSelection(
	outputDir string,
	metadata *lmeMetadata,
	selected []lmeSelectedTrace,
) (lmeResultArtifact, error) {
	index := lmeTraceSelectionIndex{
		SchemaVersion: lmeTraceSelectionSchemaVersion,
		Purpose:       lmeTraceArtifactPurpose,
		Comparability: lmeTraceArtifactComparable,
		Scenario:      metadata.Scenario,
		Backend:       metadata.MemoryBackend,
		ContentMode:   metadata.Config.TraceContentMode,
		Cases:         make([]lmeTraceSelectionEntry, 0, len(selected)),
	}
	for _, trace := range selected {
		index.Cases = append(index.Cases, lmeTraceSelectionEntry{
			CaseID: trace.caseID,
			File:   trace.file,
			SHA256: trace.digest,
		})
	}
	indexData, err := marshalLMEJSON(index)
	if err != nil {
		return lmeResultArtifact{}, fmt.Errorf("marshal trace selection index: %w", err)
	}
	name := lmeTraceSelectionDirectoryName(indexData)
	root := filepath.Join(outputDir, "build_trace")
	target := filepath.Join(root, name)
	if err := installLMETraceSelection(root, target, indexData, selected); err != nil {
		return lmeResultArtifact{}, err
	}
	digest, err := digestLMEPath(target)
	if err != nil {
		return lmeResultArtifact{}, fmt.Errorf("digest trace selection: %w", err)
	}
	return lmeResultArtifact{
		Path:          filepath.ToSlash(filepath.Join("build_trace", name)),
		SHA256:        digest,
		Purpose:       lmeTraceArtifactPurpose,
		Comparability: lmeTraceArtifactComparable,
		ContentMode:   string(index.ContentMode),
		SelectedCases: len(index.Cases),
	}, nil
}

func lmeTraceSelectionDirectoryName(indexData []byte) string {
	return "maintained-" + lmeSHA256(string(indexData))[:16]
}

func installLMETraceSelection(
	root string,
	target string,
	indexData []byte,
	selected []lmeSelectedTrace,
) error {
	if err := os.MkdirAll(root, 0755); err != nil {
		return fmt.Errorf("create trace artifact root: %w", err)
	}
	temp, err := os.MkdirTemp(root, ".maintained-selection-")
	if err != nil {
		return fmt.Errorf("create trace selection staging directory: %w", err)
	}
	defer os.RemoveAll(temp)
	if err := writeLMETraceSelectionFile(filepath.Join(temp, "manifest.json"), indexData); err != nil {
		return err
	}
	for _, trace := range selected {
		if err := writeLMETraceSelectionFile(filepath.Join(temp, trace.file), trace.data); err != nil {
			return err
		}
	}
	if err := syncLMEOutputDirectory(temp); err != nil {
		return fmt.Errorf("sync trace selection: %w", err)
	}
	if err := os.Rename(temp, target); err != nil {
		info, statErr := os.Lstat(target)
		if statErr != nil {
			return fmt.Errorf("publish trace selection: %w", err)
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("publish trace selection: target is not a regular directory")
		}
		if err := validateInstalledLMETraceSelection(target, indexData, selected); err != nil {
			return fmt.Errorf("existing trace selection conflicts: %w", err)
		}
		return nil
	}
	if err := syncLMEOutputDirectory(root); err != nil {
		return fmt.Errorf("sync trace artifact root: %w", err)
	}
	return nil
}

func writeLMETraceSelectionFile(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("create trace selection file: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write trace selection file: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync trace selection file: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close trace selection file: %w", err)
	}
	return nil
}

func validateInstalledLMETraceSelection(
	target string,
	indexData []byte,
	selected []lmeSelectedTrace,
) error {
	indexPath := filepath.Join(target, "manifest.json")
	indexInfo, err := os.Lstat(indexPath)
	if err != nil || !indexInfo.Mode().IsRegular() {
		return fmt.Errorf("selection manifest is not a regular file")
	}
	actualIndex, err := os.ReadFile(indexPath)
	if err != nil || string(actualIndex) != string(indexData) {
		return fmt.Errorf("selection manifest differs")
	}
	entries, err := os.ReadDir(target)
	if err != nil || len(entries) != len(selected)+1 {
		return fmt.Errorf("selection file set differs")
	}
	for _, trace := range selected {
		tracePath := filepath.Join(target, trace.file)
		info, err := os.Lstat(tracePath)
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("selected case trace is not a regular file")
		}
		data, err := os.ReadFile(tracePath)
		if err != nil || lmeDigestAlgorithm+":"+lmeSHA256(string(data)) != trace.digest {
			return fmt.Errorf("selected case trace differs")
		}
	}
	return nil
}

func loadLMETraceBuildPlan(outputDir string) (*lmeBuildPlanCorpus, error) {
	manifestPath := filepath.Join(outputDir, lmeRunManifestResultFileName)
	manifest, err := readLMERunManifest(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("validate run manifest: %w", err)
	}
	path, err := resolveLMEInputLocator(
		filepath.Dir(filepath.Dir(manifestPath)),
		manifest.Artifacts.BuildPlan.Path,
	)
	if err != nil {
		return nil, fmt.Errorf("resolve build plan: %w", err)
	}
	return loadLMEBuildPlan(path)
}

func parseLMETraceAttemptFile(name string) (string, int, bool) {
	base := strings.TrimSuffix(name, ".gz")
	if !strings.HasSuffix(base, ".jsonl") {
		return "", 0, false
	}
	base = strings.TrimSuffix(base, ".jsonl")
	const marker = ".attempt-"
	index := strings.LastIndex(base, marker)
	if index <= 0 {
		return "", 0, false
	}
	attempt, err := strconv.Atoi(base[index+len(marker):])
	if err != nil || attempt <= 0 || attempt > lmeTraceMaxAttempts {
		return "", 0, false
	}
	return base[:index], attempt, true
}

func validateLMEPublishedBuildTrace(
	root string,
	result *lmeRunResult,
	expectedCaseIDs []string,
	artifact lmeResultArtifact,
) []string {
	if result == nil || result.Metadata == nil {
		return []string{"result metadata is unavailable for build trace validation"}
	}
	mode := result.Metadata.Config.TraceContentMode
	blockers := validateLMETraceArtifactDeclaration(artifact, mode, len(expectedCaseIDs))
	selectionRoot, clean, pathBlocker := resolveLMETraceSelectionRoot(root, artifact.Path)
	if pathBlocker != "" {
		return append(blockers, pathBlocker)
	}
	index, indexBlocker := readLMETraceSelectionIndex(selectionRoot)
	if indexBlocker != "" {
		return append(blockers, indexBlocker)
	}
	blockers = append(blockers, validateLMETraceSelectionDeclaration(
		index,
		filepath.Base(clean),
		result.Metadata,
		mode,
	)...)
	if len(index.Cases) != len(expectedCaseIDs) {
		return append(blockers, "build trace selection does not match the fixed denominator")
	}
	plan, err := loadLMETraceBuildPlan(root)
	if err != nil {
		return append(blockers, "immutable build plan is unavailable for published trace validation")
	}
	expectedFiles, caseBlockers := validateLMETraceSelectionCases(
		selectionRoot,
		index.Cases,
		expectedCaseIDs,
		mode,
		plan,
		indexLMECaseResults(result.Cases),
	)
	blockers = append(blockers, caseBlockers...)
	blockers = append(blockers, validateLMETraceSelectionDirectory(
		selectionRoot,
		expectedFiles,
	)...)
	return uniqueLMEBlockers(blockers)
}

func validateLMETraceArtifactDeclaration(
	artifact lmeResultArtifact,
	mode lmeTraceContentMode,
	expectedCases int,
) []string {
	var blockers []string
	if artifact.Purpose != lmeTraceArtifactPurpose {
		blockers = append(blockers, "build trace purpose is not best-effort diagnostic")
	}
	if artifact.Comparability != lmeTraceArtifactComparable {
		blockers = append(blockers, "build trace comparability does not prohibit cross-backend ranking")
	}
	if artifact.ContentMode != string(mode) ||
		(mode != lmeTraceContentHash && mode != lmeTraceContentNone) {
		blockers = append(blockers, "build trace content mode is incompatible with the result")
	}
	if artifact.SelectedCases != expectedCases {
		blockers = append(blockers, "build trace selected case count is incompatible with the denominator")
	}
	return blockers
}

func resolveLMETraceSelectionRoot(root, artifactPath string) (string, string, string) {
	clean := filepath.Clean(filepath.FromSlash(artifactPath))
	if filepath.ToSlash(clean) != artifactPath || filepath.IsAbs(clean) ||
		filepath.VolumeName(clean) != "" || filepath.Dir(clean) != "build_trace" ||
		!strings.HasPrefix(filepath.Base(clean), "maintained-") {
		return "", "", "build trace artifact path is not a maintained selection"
	}
	selectionRoot := filepath.Join(root, clean)
	selectionInfo, err := os.Lstat(selectionRoot)
	if err != nil || !selectionInfo.IsDir() || selectionInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", "build trace selection directory is unavailable or invalid"
	}
	return selectionRoot, clean, ""
}

func readLMETraceSelectionIndex(
	selectionRoot string,
) (lmeTraceSelectionIndex, string) {
	indexPath := filepath.Join(selectionRoot, "manifest.json")
	indexInfo, err := os.Lstat(indexPath)
	if err != nil || !indexInfo.Mode().IsRegular() || indexInfo.Size() > 1<<20 {
		return lmeTraceSelectionIndex{},
			"build trace selection manifest is unavailable or invalid"
	}
	indexData, err := os.ReadFile(indexPath)
	if err != nil {
		return lmeTraceSelectionIndex{}, "build trace selection manifest is unreadable"
	}
	var index lmeTraceSelectionIndex
	if err := lmeDecodeStrict(indexData, &index); err != nil {
		return lmeTraceSelectionIndex{},
			"build trace selection manifest is not strict JSON"
	}
	return index, ""
}

func validateLMETraceSelectionDeclaration(
	index lmeTraceSelectionIndex,
	directoryName string,
	metadata *lmeMetadata,
	mode lmeTraceContentMode,
) []string {
	var blockers []string
	if index.SchemaVersion != lmeTraceSelectionSchemaVersion ||
		index.Purpose != lmeTraceArtifactPurpose ||
		index.Comparability != lmeTraceArtifactComparable {
		blockers = append(blockers, "build trace selection declaration is invalid")
	}
	if index.Scenario != metadata.Scenario ||
		index.Backend != metadata.MemoryBackend || index.ContentMode != mode {
		blockers = append(blockers, "build trace selection identity is incompatible with the result")
	}
	canonicalIndex, err := marshalLMEJSON(index)
	if err != nil || directoryName != lmeTraceSelectionDirectoryName(canonicalIndex) {
		blockers = append(blockers, "build trace selection identity digest is invalid")
	}
	return blockers
}

func indexLMECaseResults(records []*lmeCaseResult) map[string]*lmeCaseResult {
	results := make(map[string]*lmeCaseResult, len(records))
	for _, record := range records {
		if record != nil {
			results[record.QuestionID] = record
		}
	}
	return results
}

func validateLMETraceSelectionCases(
	selectionRoot string,
	selected []lmeTraceSelectionEntry,
	expectedCaseIDs []string,
	mode lmeTraceContentMode,
	plan *lmeBuildPlanCorpus,
	caseResults map[string]*lmeCaseResult,
) (map[string]struct{}, []string) {
	expectedFiles := map[string]struct{}{"manifest.json": {}}
	var blockers []string
	for i, entry := range selected {
		caseID := expectedCaseIDs[i]
		if entry.CaseID != caseID {
			blockers = append(blockers, "build trace selection case order differs from the denominator")
			continue
		}
		expectedFile := lmeTraceFileName(caseID) + ".jsonl"
		if strings.HasSuffix(entry.File, ".gz") {
			expectedFile += ".gz"
		}
		if entry.File != expectedFile || strings.Contains(entry.File, ".attempt-") ||
			filepath.Base(entry.File) != entry.File {
			blockers = append(blockers, "build trace selection contains a non-canonical case file")
			continue
		}
		if _, duplicate := expectedFiles[entry.File]; duplicate {
			blockers = append(blockers, "build trace selection contains duplicate case files")
			continue
		}
		expectedFiles[entry.File] = struct{}{}
		blockers = append(blockers, validateLMETraceSelectionCase(
			selectionRoot,
			entry,
			caseID,
			mode,
			plan.Cases[caseID],
			caseResults[caseID],
		)...)
	}
	return expectedFiles, blockers
}

func validateLMETraceSelectionCase(
	selectionRoot string,
	entry lmeTraceSelectionEntry,
	caseID string,
	mode lmeTraceContentMode,
	casePlan *lmeBuildCasePlan,
	caseResult *lmeCaseResult,
) []string {
	tracePath := filepath.Join(selectionRoot, entry.File)
	traceInfo, err := os.Lstat(tracePath)
	if err != nil || !traceInfo.Mode().IsRegular() || traceInfo.Size() > lmeTraceMaxFileBytes {
		return []string{"selected build trace is not a bounded regular file for " + caseID}
	}
	digest, err := digestLMEFile(tracePath)
	if err != nil || entry.SHA256 != lmeDigestAlgorithm+":"+digest {
		return []string{"selected build trace digest is invalid for " + caseID}
	}
	summary, err := readLMETraceOutcome(tracePath)
	if err != nil || summary.CaseID != caseID || summary.ContentMode != mode {
		return []string{"selected build trace is invalid for " + caseID}
	}
	var blockers []string
	if err := validateLMETraceBuildSources(casePlan, summary); err != nil {
		blockers = append(blockers, "selected build trace does not match the build plan for "+caseID)
	}
	if err := validateLMETraceOutcome(caseResult, summary); err != nil {
		blockers = append(blockers, "selected build trace does not match the result for "+caseID)
	}
	return blockers
}

func validateLMETraceSelectionDirectory(
	selectionRoot string,
	expectedFiles map[string]struct{},
) []string {
	entries, err := os.ReadDir(selectionRoot)
	if err != nil {
		return []string{"build trace selection directory is unavailable"}
	}
	var blockers []string
	if len(entries) != len(expectedFiles) {
		blockers = append(blockers, "build trace selection contains unexpected artifacts")
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			blockers = append(blockers, "build trace selection contains a non-regular artifact")
			continue
		}
		if _, ok := expectedFiles[entry.Name()]; !ok {
			blockers = append(blockers, "build trace selection contains an unexpected artifact")
		}
	}
	return blockers
}
