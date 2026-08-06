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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

const (
	preparedBundleFilename = "replay-bundle.json"
	preparedReportFilename = "prepare-validation-report.json"
	engineConfigFilename   = "engine-config.json"
)

type prepareOptions struct {
	RunDir     string
	CorpusRoot string
	CaseList   string
	OutputDir  string
}

func runPrepare(args []string) error {
	if len(args) == 0 {
		return errors.New("retrieval-replay prepare argv is empty")
	}
	flags := flag.NewFlagSet(filepath.Base(args[0]), flag.ContinueOnError)
	options := prepareOptions{}
	flags.StringVar(&options.RunDir, "run-dir", "", "framework-Native run output directory")
	flags.StringVar(&options.CorpusRoot, "corpus-root", "", "root containing one task-start snapshot directory per selected instance")
	flags.StringVar(&options.CaseList, "case-list", "", "sorted selected instance IDs, one per line")
	flags.StringVar(&options.OutputDir, "output-dir", "", "new portable replay bundle directory")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments: %s", strings.Join(flags.Args(), " "))
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "--run-dir", value: options.RunDir},
		{name: "--corpus-root", value: options.CorpusRoot},
		{name: "--case-list", value: options.CaseList},
		{name: "--output-dir", value: options.OutputDir},
	} {
		if strings.TrimSpace(required.value) == "" {
			return fmt.Errorf("%s is required", required.name)
		}
	}
	return prepareBundle(context.Background(), options)
}

// prepareBundle builds and validates a portable replay bundle from explicit frozen
// task-start snapshots plus immutable Native artifacts. A Native run directory
// alone is intentionally insufficient because it does not contain workspace
// bytes. The output directory is published only after a full offline replay.
func prepareBundle(ctx context.Context, options prepareOptions) error {
	runRoot, err := resolveRealDirectory(options.RunDir, "Native run directory")
	if err != nil {
		return err
	}
	corpusRoot, err := resolveRealDirectory(options.CorpusRoot, "frozen corpus root")
	if err != nil {
		return err
	}
	caseIDs, err := readPrepareCaseList(options.CaseList)
	if err != nil {
		return err
	}
	caseDirectories, err := validateCorpusRootSelection(corpusRoot, caseIDs)
	if err != nil {
		return err
	}
	output, parent, err := validateNewOutputDirectory(options.OutputDir, runRoot, corpusRoot)
	if err != nil {
		return err
	}
	manifest, manifestSHA, err := inspectNativeManifest(runRoot)
	if err != nil {
		return err
	}
	if manifest.EmbeddingConfig != nil || manifest.EmbeddingConfigSHA256 != "" {
		return errors.New("prepare currently supports exact keyword/BM25 replay only; dense runs require a portable all-hit embedding cache")
	}
	if len(caseIDs) > manifest.CaseCount {
		return fmt.Errorf("case list has %d cases but Native manifest has %d", len(caseIDs), manifest.CaseCount)
	}
	staging, err := os.MkdirTemp(parent, ".retrieval-replay-prepare-*")
	if err != nil {
		return fmt.Errorf("create bundle staging directory: %w", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(staging)
		}
	}()

	engineConfigData, err := defaultEngineConfigJSON()
	if err != nil {
		return err
	}
	engineConfigData = append(engineConfigData, '\n')
	engineConfigPath := filepath.Join(staging, engineConfigFilename)
	if err := os.WriteFile(engineConfigPath, engineConfigData, 0o600); err != nil {
		return err
	}
	engineConfigRef := ArtifactRef{Path: engineConfigFilename, SHA256: digestBytes(engineConfigData)}
	bundle := Bundle{
		SchemaVersion: BundleSchemaVersion, Kind: BundleKind,
		NativeRun: NativeRunBinding{RunID: manifest.RunID, ManifestSHA256: manifestSHA},
		Engine:    DefaultEngineDescriptor(), EngineConfig: engineConfigRef,
		Cases: make([]CaseSpec, 0, len(caseIDs)),
	}
	for _, instanceID := range caseIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		spec, err := prepareCase(
			ctx,
			staging,
			runRoot,
			caseDirectories[instanceID],
			manifest,
			bundle,
			instanceID,
		)
		if err != nil {
			return fmt.Errorf("prepare case %s: %w", instanceID, err)
		}
		bundle.Cases = append(bundle.Cases, spec)
	}
	bundle.CaseSetSHA256, err = CaseSetSHA256(caseIDs)
	if err != nil {
		return err
	}
	bundlePath := filepath.Join(staging, preparedBundleFilename)
	if _, err := writePreparedJSON(bundlePath, bundle); err != nil {
		return err
	}
	loaded, err := Load(bundlePath, runRoot)
	if err != nil {
		return fmt.Errorf("validate prepared bundle: %w", err)
	}
	report, err := Replay(ctx, loaded, NewOfflineEngine())
	if err != nil {
		return fmt.Errorf("validate prepared replay: %w", err)
	}
	if report.Status != "completed" || report.Summary.Mismatches != 0 {
		return errors.New("prepared replay differs from Native retrieval trace")
	}
	if err := writeReportAtomic(filepath.Join(staging, preparedReportFilename), report); err != nil {
		return err
	}
	if err := os.Rename(staging, output); err != nil {
		return fmt.Errorf("publish prepared bundle: %w", err)
	}
	published = true
	return nil
}

func prepareCase(
	ctx context.Context,
	staging, runRoot, corpusDirectory string,
	manifest nativeManifest,
	bundle Bundle,
	instanceID string,
) (CaseSpec, error) {
	evidence, err := inspectNativeCase(runRoot, manifest, instanceID)
	if err != nil {
		return CaseSpec{}, err
	}
	if evidence.WorkspaceIndex.Representation != manifest.WorkspaceRepresentation ||
		evidence.WorkspaceIndex.RepresentationSchema != manifest.RepresentationSchema ||
		evidence.WorkspaceIndex.RepresentationSHA256 != manifest.RepresentationSHA256 ||
		evidence.WorkspaceIndex.RetrievalMode != "keyword" {
		return CaseSpec{}, errors.New("Native case workspace index does not match keyword run manifest")
	}
	files, err := scanFrozenCorpus(corpusDirectory)
	if err != nil {
		return CaseSpec{}, err
	}
	stats, err := inspectPreparedCorpus(ctx, corpusDirectory, evidence.Identity.Repo, manifest)
	if err != nil {
		return CaseSpec{}, err
	}
	if stats.EligibleFileSetSHA256 != evidence.WorkspaceIndex.EligibleFileSetSHA256 ||
		stats.EligibleContentSHA256 != evidence.WorkspaceIndex.EligibleContentSHA256 ||
		stats.DocumentSetSHA256 != evidence.WorkspaceIndex.DocumentSetSHA256 ||
		stats.ParserDependency != evidence.WorkspaceIndex.ParserDependency ||
		stats.ParserRuntimeSHA256 != evidence.WorkspaceIndex.ParserRuntimeSHA256 ||
		stats.Representation != evidence.WorkspaceIndex.Representation ||
		stats.RepresentationSchema != evidence.WorkspaceIndex.RepresentationSchema ||
		stats.RepresentationSHA256 != evidence.WorkspaceIndex.RepresentationSHA256 ||
		stats.RetrievalMode != evidence.WorkspaceIndex.RetrievalMode {
		return CaseSpec{}, errors.New("frozen corpus index hashes do not match Native workspace_index")
	}
	relativeDirectory := filepath.ToSlash(filepath.Join("artifacts", instanceID))
	caseOutput := filepath.Join(staging, filepath.FromSlash(relativeDirectory))
	if err := os.MkdirAll(caseOutput, 0o700); err != nil {
		return CaseSpec{}, err
	}
	corpusRelative := relativeDirectory + "/workspace.tar"
	corpusPath := filepath.Join(staging, filepath.FromSlash(corpusRelative))
	if err := writeDeterministicCorpusTar(corpusPath, files); err != nil {
		return CaseSpec{}, err
	}
	corpusSHA, err := digestRegularFile(
		corpusPath,
		"prepared corpus",
		maxCorpusTotal+int64(maxCorpusFiles)*4096,
	)
	if err != nil {
		return CaseSpec{}, err
	}
	querySet := QuerySet{
		SchemaVersion: QuerySetSchemaVersion, Kind: QuerySetKind,
		InstanceID:                    instanceID,
		EmittedCodeSearchCalls:        evidence.EmittedQueries,
		SkippedEmittedCodeSearchCalls: evidence.SkippedQueries,
		Queries:                       evidence.Queries,
	}
	queryRelative := relativeDirectory + "/queries.json"
	queryData, err := writePreparedJSON(filepath.Join(staging, filepath.FromSlash(queryRelative)), querySet)
	if err != nil {
		return CaseSpec{}, err
	}
	expectedRelative := relativeDirectory + "/expected.json"
	spec := CaseSpec{
		InstanceID: instanceID, NativeArtifactSHA256: evidence.NativeSHA256,
		ResponsesArtifactSHA256: evidence.ResponsesSHA256,
		Corpus:                  ArtifactRef{Path: corpusRelative, SHA256: corpusSHA},
		Queries:                 ArtifactRef{Path: queryRelative, SHA256: digestBytes(queryData)},
		ExpectedResults:         ArtifactRef{Path: expectedRelative, SHA256: strings.Repeat("0", 64)},
		Retrieval: RetrievalProvenance{
			CorpusFormat: "workspace-tar-v1", CorpusSHA256: corpusSHA,
			EligibleFileSetSHA256:         evidence.WorkspaceIndex.EligibleFileSetSHA256,
			EligibleContentSHA256:         evidence.WorkspaceIndex.EligibleContentSHA256,
			DocumentSetSHA256:             evidence.WorkspaceIndex.DocumentSetSHA256,
			WorkspaceRepresentation:       evidence.WorkspaceIndex.Representation,
			WorkspaceRepresentationSchema: evidence.WorkspaceIndex.RepresentationSchema,
			WorkspaceRepresentationSHA256: evidence.WorkspaceIndex.RepresentationSHA256,
			ParserDependency:              evidence.WorkspaceIndex.ParserDependency,
			ParserRuntimeSHA256:           evidence.WorkspaceIndex.ParserRuntimeSHA256,
			RetrievalMode:                 "keyword", MaxResults: 6, InvocationDedup: false,
		},
	}
	if err := validateCaseSpecAgainstManifest(spec, manifest); err != nil {
		return CaseSpec{}, err
	}
	spec.InputSHA256, err = InputSHA256(
		bundle.NativeRun.ManifestSHA256,
		bundle.Engine,
		bundle.EngineConfig,
		spec,
	)
	if err != nil {
		return CaseSpec{}, err
	}
	expected := ExpectedResultSet{
		SchemaVersion: ResultSetSchemaVersion, Kind: ResultSetKind,
		InstanceID: instanceID, InputSHA256: spec.InputSHA256,
		Results: make([]ExpectedQueryResult, len(evidence.Queries)),
	}
	for index, result := range evidence.Results {
		expected.Results[index] = ExpectedQueryResult{
			Ordinal: result.Ordinal, QuerySHA256: result.QuerySHA256,
			Status: result.Status, ErrorSHA256: result.ErrorSHA256,
			ResultSHA256: result.ResultSHA256,
			Documents:    append([]RankedDocument(nil), result.Documents...),
		}
	}
	expectedData, err := writePreparedJSON(
		filepath.Join(staging, filepath.FromSlash(expectedRelative)),
		expected,
	)
	if err != nil {
		return CaseSpec{}, err
	}
	spec.ExpectedResults.SHA256 = digestBytes(expectedData)
	return spec, nil
}

func inspectPreparedCorpus(
	ctx context.Context,
	directory, repository string,
	manifest nativeManifest,
) (tagagent.WorkspaceIndexStats, error) {
	representation, err := tagagent.ParseWorkspaceRepresentation(manifest.WorkspaceRepresentation)
	if err != nil {
		return tagagent.WorkspaceIndexStats{}, err
	}
	index, stats, err := tagagent.NewWorkspaceIndexFromDirectory(
		ctx,
		directory,
		repository,
		&tagagent.WorkspaceSearchConfig{
			SearchMode: vectorstore.SearchModeKeyword, MaxResults: 6,
			Representation: representation, RepositoryName: repository,
			BatchSize: 1, DocConcurrency: 1,
		},
	)
	if err != nil {
		return tagagent.WorkspaceIndexStats{}, err
	}
	if err := index.Close(); err != nil {
		return tagagent.WorkspaceIndexStats{}, err
	}
	return stats, nil
}

func readPrepareCaseList(filePath string) ([]string, error) {
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, fmt.Errorf("inspect case list: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, errors.New("case list must be a real regular file")
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var result []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		instanceID := strings.TrimSpace(scanner.Text())
		if instanceID == "" {
			continue
		}
		if err := validateInstanceID(instanceID); err != nil {
			return nil, err
		}
		if len(result) > 0 && strings.Compare(result[len(result)-1], instanceID) >= 0 {
			return nil, errors.New("case list must be strictly sorted with no duplicates")
		}
		result = append(result, instanceID)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(result) == 0 {
		return nil, errors.New("case list is empty")
	}
	return result, nil
}

func validateCorpusRootSelection(root string, instanceIDs []string) (map[string]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	expected := make(map[string]struct{}, len(instanceIDs))
	for _, instanceID := range instanceIDs {
		expected[instanceID] = struct{}{}
	}
	directories := make(map[string]string, len(instanceIDs))
	for _, entry := range entries {
		if _, ok := expected[entry.Name()]; !ok {
			return nil, fmt.Errorf("corpus root contains unselected entry %q", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.IsDir() {
			return nil, fmt.Errorf("corpus case %q must be a real directory", entry.Name())
		}
		directories[entry.Name()] = filepath.Join(root, entry.Name())
	}
	for _, instanceID := range instanceIDs {
		if _, ok := directories[instanceID]; !ok {
			return nil, fmt.Errorf("corpus root is missing selected case %q", instanceID)
		}
	}
	return directories, nil
}

func validateNewOutputDirectory(output, runRoot, corpusRoot string) (string, string, error) {
	absOutput, err := filepath.Abs(output)
	if err != nil {
		return "", "", err
	}
	absOutput = filepath.Clean(absOutput)
	if _, err := os.Lstat(absOutput); err == nil {
		return "", "", errors.New("output directory already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	for _, input := range []struct {
		label string
		root  string
	}{
		{label: "Native run", root: runRoot},
		{label: "corpus", root: corpusRoot},
	} {
		if pathWithin(input.root, absOutput) || pathWithin(absOutput, input.root) {
			return "", "", fmt.Errorf("output directory must be disjoint from %s input", input.label)
		}
	}
	parent := filepath.Dir(absOutput)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", "", err
	}
	parent, err = resolveRealDirectory(parent, "output parent")
	if err != nil {
		return "", "", err
	}
	return absOutput, parent, nil
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && (relative == "." ||
		(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}

func writePreparedJSON(filePath string, value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if err := os.WriteFile(filePath, data, 0o600); err != nil {
		return nil, err
	}
	return data, nil
}
