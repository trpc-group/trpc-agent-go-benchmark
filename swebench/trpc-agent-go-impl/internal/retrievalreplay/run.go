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
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingcache"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/embeddingconfig"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/tagagent"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/embedder"
	"trpc.group/trpc-go/trpc-agent-go/knowledge/vectorstore"
)

// Run executes the offline workspace retrieval replay CLI.
func Run(args []string) error {
	fs := flag.NewFlagSet("retrieval-replay", flag.ContinueOnError)
	runID := fs.String("run-id", "", "replay run id")
	casesPath := fs.String("cases", "data/generated/cases.jsonl", "safe SWE-Bench cases JSONL")
	caseListPath := fs.String("case-list", "", "optional exact instance-id list")
	labelsPath := fs.String("labels", "", "external, uncommitted JSON/JSONL with instance_id and patch")
	environmentConfigPath := fs.String(
		"environment-config",
		"config/environments/swebench-testbed.yaml",
		"environment YAML path",
	)
	embeddingConfigPath := fs.String("embedding-config", "", "optional workspace embedding YAML path")
	outputPath := fs.String("output", "", "report JSON path")
	filterValue := fs.String("filter", "", "optional instance id regexp")
	representationsValue := fs.String(
		"representations",
		"current-fixed,fixed-raw,ast-code,ast-structured",
		"comma-separated workspace representations",
	)
	maxResults := fs.Int("max-results", 6, "retrieval results retained per representation")
	caseWorkers := fs.Int("case-workers", 1, "parallel case snapshots")
	commandTimeout := fs.Duration("command-timeout", time.Minute, "timeout for Docker commands")
	caseTimeout := fs.Duration("case-timeout", 2*time.Hour, "timeout for one replay case")
	dockerHost := fs.String("docker-host", "", "optional Docker daemon endpoint")
	frameworkRevision := fs.String("framework-revision", "", "framework commit used for this binary")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if strings.TrimSpace(*runID) == "" {
		return fmt.Errorf("-run-id is required")
	}
	if strings.TrimSpace(*labelsPath) == "" {
		return fmt.Errorf("-labels is required and must remain outside committed benchmark data")
	}
	if strings.TrimSpace(*frameworkRevision) == "" {
		return fmt.Errorf("-framework-revision is required")
	}
	if *maxResults < 6 {
		return fmt.Errorf("-max-results must be at least 6 for Recall@6")
	}
	if *caseWorkers <= 0 {
		return fmt.Errorf("-case-workers must be positive")
	}
	if *commandTimeout <= 0 {
		return fmt.Errorf("-command-timeout must be positive")
	}
	if *caseTimeout <= 0 {
		return fmt.Errorf("-case-timeout must be positive")
	}
	representations, err := parseRepresentations(*representationsValue)
	if err != nil {
		return err
	}
	if *outputPath == "" {
		*outputPath = filepath.Join(
			"results",
			"retrieval-replay",
			*runID,
			"report.json",
		)
	}

	cases, err := artifact.ReadCasesJSONL(*casesPath)
	if err != nil {
		return fmt.Errorf("read cases: %w", err)
	}
	var caseIDs map[string]struct{}
	if strings.TrimSpace(*caseListPath) != "" {
		caseIDs, err = loadCaseIDs(*caseListPath)
		if err != nil {
			return fmt.Errorf("read case list: %w", err)
		}
	}
	selected, err := selectReplayCases(cases, *filterValue, caseIDs)
	if err != nil {
		return err
	}
	labels, err := loadGoldLabels(*labelsPath)
	if err != nil {
		return fmt.Errorf("read labels: %w", err)
	}
	targetsByCase := make(map[string]patchTargets, len(selected))
	for _, replayCase := range selected {
		label, ok := labels[replayCase.InstanceID]
		if !ok {
			return fmt.Errorf("labels missing selected instance %s", replayCase.InstanceID)
		}
		targets, parseErr := parsePatchTargets(label.Patch)
		if parseErr != nil {
			return fmt.Errorf("parse gold patch for %s: %w", replayCase.InstanceID, parseErr)
		}
		if len(targets.TargetFiles) == 0 {
			return fmt.Errorf(
				"instance %s gold patch has no base-side target files",
				replayCase.InstanceID,
			)
		}
		targetsByCase[replayCase.InstanceID] = targets
	}

	envConfig, err := sweenv.LoadConfig(*environmentConfigPath)
	if err != nil {
		return fmt.Errorf("load environment config: %w", err)
	}
	environmentHash, err := hashFile(*environmentConfigPath)
	if err != nil {
		return fmt.Errorf("hash environment config: %w", err)
	}
	var embeddingConfig *embeddingconfig.Config
	var embeddingConfigHash string
	if strings.TrimSpace(*embeddingConfigPath) != "" {
		embeddingConfig, err = embeddingconfig.Load(*embeddingConfigPath)
		if err != nil {
			return fmt.Errorf("load embedding config: %w", err)
		}
		embeddingConfigHash, err = hashFile(*embeddingConfigPath)
		if err != nil {
			return fmt.Errorf("hash embedding config: %w", err)
		}
	}
	var cacheStore *embeddingcache.Store
	if embeddingConfig != nil && embeddingConfig.Cache.Enabled {
		cacheStore, err = embeddingcache.Open(
			context.Background(),
			embeddingConfig.Cache.Directory,
			embeddingConfig.CacheIdentity(),
		)
		if err != nil {
			return fmt.Errorf("open embedding cache: %w", err)
		}
		defer func() { _ = cacheStore.Close() }()
	}

	casesHash, err := hashFile(*casesPath)
	if err != nil {
		return fmt.Errorf("hash cases: %w", err)
	}
	labelsHash, err := hashFile(*labelsPath)
	if err != nil {
		return fmt.Errorf("hash labels: %w", err)
	}
	build, err := currentBuildMetadata()
	if err != nil {
		return err
	}
	started := time.Now()
	report := Report{
		SchemaVersion:     reportSchemaVersion,
		RunID:             strings.TrimSpace(*runID),
		Status:            "running",
		SourceRevision:    build.SourceRevision,
		SourceModified:    build.SourceModified,
		BinarySHA256:      build.BinarySHA256,
		FrameworkRevision: strings.TrimSpace(*frameworkRevision),
		StartedAt:         started.UTC(),
		CasesPath:         artifact.AbsPath(*casesPath),
		CasesSHA256:       casesHash,
		LabelsPath:        artifact.AbsPath(*labelsPath),
		LabelsSHA256:      labelsHash,
		EnvironmentConfig: artifact.AbsPath(*environmentConfigPath),
		EnvironmentSHA256: environmentHash,
		QueryMode:         "problem_statement",
		MaxResults:        *maxResults,
		SelectedCases:     len(selected),
		CaseWorkers:       *caseWorkers,
		Aggregate:         map[string]Aggregate{},
	}
	if strings.TrimSpace(*caseListPath) != "" {
		report.CaseListPath = artifact.AbsPath(*caseListPath)
		report.CaseListSHA256, err = hashFile(*caseListPath)
		if err != nil {
			return fmt.Errorf("hash case list: %w", err)
		}
	}
	for _, representation := range representations {
		report.Representations = append(report.Representations, string(representation))
	}
	if embeddingConfig != nil {
		report.EmbeddingConfigPath = artifact.AbsPath(*embeddingConfigPath)
		report.EmbeddingConfigSHA = embeddingConfigHash
		report.EmbeddingConfig = embeddingConfig.Redacted()
	}
	if cacheStore != nil {
		report.EmbeddingCacheDB = artifact.AbsPath(cacheStore.Path())
	}
	if err := artifact.WriteJSONAtomic(*outputPath, report); err != nil {
		return fmt.Errorf("write initial report: %w", err)
	}

	factory := sweenv.DockerFactory{
		Config:         envConfig,
		DockerHost:     *dockerHost,
		CommandTimeout: *commandTimeout,
		CaseTimeout:    *caseTimeout,
		Labels: map[string]string{
			"tag-swebench.retrieval_replay": strings.TrimSpace(*runID),
		},
	}
	results := make([]CaseResult, len(selected))
	completed := make([]bool, len(selected))
	jobs := make(chan int)
	var workers sync.WaitGroup
	var checkpointMu sync.Mutex
	var checkpointErrors []error
	for worker := 0; worker < *caseWorkers; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				replayCase := selected[index]
				result := runReplayCase(
					context.Background(),
					factory,
					replayCase,
					targetsByCase[replayCase.InstanceID],
					representations,
					embeddingConfig,
					cacheStore,
					*maxResults,
					*caseTimeout,
				)
				checkpointMu.Lock()
				results[index] = result
				completed[index] = true
				report.Cases = completedResults(results, completed)
				report.CompletedCases = len(report.Cases)
				report.Aggregate = aggregateResults(report.Cases, representations)
				report.DurationMS = time.Since(started).Milliseconds()
				completedCount := report.CompletedCases
				if writeErr := artifact.WriteJSONAtomic(*outputPath, report); writeErr != nil {
					checkpointErrors = append(checkpointErrors, writeErr)
				}
				checkpointMu.Unlock()
				fmt.Printf(
					"instance=%s arms=%d error=%q completed=%d/%d\n",
					result.InstanceID,
					len(result.Arms),
					result.Error,
					completedCount,
					len(selected),
				)
			}
		}()
	}
	for index := range selected {
		jobs <- index
	}
	close(jobs)
	workers.Wait()

	report.Cases = completedResults(results, completed)
	report.CompletedCases = len(report.Cases)
	report.Aggregate = aggregateResults(report.Cases, representations)
	report.FinishedAt = time.Now().UTC()
	report.DurationMS = time.Since(started).Milliseconds()
	failed := failedCaseCount(report.Cases)
	if failed > 0 {
		report.Status = "completed_with_errors"
	} else {
		report.Status = "completed"
	}
	if err := artifact.WriteJSONAtomic(*outputPath, report); err != nil {
		return fmt.Errorf("write final report: %w", err)
	}
	if len(checkpointErrors) > 0 {
		return fmt.Errorf("write replay checkpoints: %w", errors.Join(checkpointErrors...))
	}
	fmt.Printf(
		"status=%s cases=%d failed=%d report=%s\n",
		report.Status,
		report.CompletedCases,
		failed,
		artifact.AbsPath(*outputPath),
	)
	if failed > 0 {
		return fmt.Errorf("%d replay case(s) failed; see %s", failed, artifact.AbsPath(*outputPath))
	}
	return nil
}

func runReplayCase(
	parent context.Context,
	factory sweenv.Factory,
	replayCase contract.Case,
	targets patchTargets,
	representations []tagagent.WorkspaceRepresentation,
	embeddingConfig *embeddingconfig.Config,
	cacheStore *embeddingcache.Store,
	maxResults int,
	caseTimeout time.Duration,
) (result CaseResult) {
	result.InstanceID = replayCase.InstanceID
	result.Repo = replayCase.Repo
	result.QuerySHA256 = hashText(replayCase.ProblemStatement)
	result.TargetFiles = len(targets.TargetFiles)
	result.NewFiles = len(targets.NewFiles)
	result.HunkAnchors = len(targets.Anchors)
	if strings.TrimSpace(replayCase.ProblemStatement) == "" {
		result.Error = "problem statement is empty"
		return result
	}

	ctx, cancel := context.WithTimeout(parent, caseTimeout)
	defer cancel()
	environment, err := factory.Start(ctx, replayCase.InstanceID)
	if err != nil {
		result.Error = "start environment: " + err.Error()
		return result
	}
	environmentClosed := false
	defer func() {
		if environmentClosed {
			return
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer closeCancel()
		if closeErr := environment.Close(closeCtx); closeErr != nil && result.Error == "" {
			result.Error = "close environment: " + closeErr.Error()
		}
	}()
	snapshotter, ok := environment.(sweenv.WorkspaceSnapshotter)
	if !ok {
		result.Error = "environment does not support workspace snapshots"
		return result
	}
	snapshotDir, err := os.MkdirTemp("", "tag-retrieval-replay-*")
	if err != nil {
		result.Error = "create snapshot directory: " + err.Error()
		return result
	}
	defer func() { _ = os.RemoveAll(snapshotDir) }()
	snapshotStarted := time.Now()
	if err := snapshotter.SnapshotWorkspace(ctx, snapshotDir); err != nil {
		result.Error = "snapshot workspace: " + err.Error()
		return result
	}
	result.SnapshotMS = time.Since(snapshotStarted).Milliseconds()
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 30*time.Second)
	closeErr := environment.Close(closeCtx)
	closeCancel()
	environmentClosed = true
	if closeErr != nil {
		result.Error = "close environment after snapshot: " + closeErr.Error()
		return result
	}

	for _, representation := range representations {
		result.Arms = append(result.Arms, runReplayArm(
			ctx,
			snapshotDir,
			replayCase,
			targets,
			representation,
			embeddingConfig,
			cacheStore,
			maxResults,
		))
	}
	return result
}

func runReplayArm(
	ctx context.Context,
	snapshotDir string,
	replayCase contract.Case,
	targets patchTargets,
	representation tagagent.WorkspaceRepresentation,
	embeddingConfig *embeddingconfig.Config,
	cacheStore *embeddingcache.Store,
	maxResults int,
) (arm ArmResult) {
	started := time.Now()
	arm.Representation = string(representation)
	var metered *embeddingconfig.MeteredEmbedder
	var cached *embeddingcache.Embedder
	defer func() {
		arm.DurationMS = time.Since(started).Milliseconds()
		if metered != nil {
			snapshot := metered.Snapshot()
			arm.Embedding = &snapshot
		}
		if cached != nil {
			snapshot := cached.Snapshot()
			arm.EmbeddingCache = &snapshot
		}
	}()

	searchMode := vectorstore.SearchModeKeyword
	var workspaceEmbedder embedder.Embedder
	batchSize := 1
	docConcurrency := 1
	if embeddingConfig != nil {
		var err error
		searchMode, err = embeddingConfig.SearchMode()
		if err != nil {
			arm.Error = err.Error()
			return arm
		}
		metered = embeddingconfig.NewMetered(embeddingConfig.NewEmbedder())
		workspaceEmbedder = metered
		batchSize = embeddingConfig.Embedding.BatchSize
		docConcurrency = embeddingConfig.Embedding.Concurrency
		if embeddingConfig.Cache.Enabled {
			if cacheStore == nil {
				arm.Error = "embedding cache enabled without store"
				return arm
			}
			cached, err = embeddingcache.New(cacheStore, metered)
			if err != nil {
				arm.Error = err.Error()
				return arm
			}
			workspaceEmbedder = cached
		}
	}
	index, stats, err := tagagent.NewWorkspaceIndexFromDirectory(
		ctx,
		snapshotDir,
		replayCase.Repo,
		&tagagent.WorkspaceSearchConfig{
			Embedder:       workspaceEmbedder,
			SearchMode:     searchMode,
			BatchSize:      batchSize,
			DocConcurrency: docConcurrency,
			MaxResults:     maxResults,
			Representation: representation,
			RepositoryName: replayCase.Repo,
		},
	)
	if err != nil {
		arm.Error = err.Error()
		return arm
	}
	arm.Index = stats
	searchResult, searchErr := index.Search(ctx, replayCase.ProblemStatement, maxResults)
	closeErr := index.Close()
	if searchErr != nil {
		arm.Error = "search: " + searchErr.Error()
		return arm
	}
	if closeErr != nil {
		arm.Error = "close index: " + closeErr.Error()
		return arm
	}
	arm.Metrics, arm.Retrieved = evaluateRetrieval(searchResult, targets)
	return arm
}

func parseRepresentations(value string) ([]tagagent.WorkspaceRepresentation, error) {
	var representations []tagagent.WorkspaceRepresentation
	seen := make(map[tagagent.WorkspaceRepresentation]struct{})
	for _, part := range strings.Split(value, ",") {
		if strings.TrimSpace(part) == "" {
			continue
		}
		representation, err := tagagent.ParseWorkspaceRepresentation(part)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[representation]; exists {
			continue
		}
		seen[representation] = struct{}{}
		representations = append(representations, representation)
	}
	if len(representations) == 0 {
		return nil, fmt.Errorf("at least one workspace representation is required")
	}
	return representations, nil
}

func selectReplayCases(
	cases []contract.Case,
	filter string,
	caseIDs map[string]struct{},
) ([]contract.Case, error) {
	var expression *regexp.Regexp
	var err error
	if strings.TrimSpace(filter) != "" {
		expression, err = regexp.Compile(filter)
		if err != nil {
			return nil, fmt.Errorf("compile filter: %w", err)
		}
	}
	var selected []contract.Case
	foundIDs := make(map[string]struct{}, len(caseIDs))
	for _, replayCase := range cases {
		if len(caseIDs) > 0 {
			if _, ok := caseIDs[replayCase.InstanceID]; !ok {
				continue
			}
			foundIDs[replayCase.InstanceID] = struct{}{}
		}
		if expression != nil && !expression.MatchString(replayCase.InstanceID) {
			continue
		}
		selected = append(selected, replayCase)
	}
	for instanceID := range caseIDs {
		if _, ok := foundIDs[instanceID]; !ok {
			return nil, fmt.Errorf("case list instance %s is absent from cases JSONL", instanceID)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("case selection matched zero cases")
	}
	slices.SortFunc(selected, func(a, b contract.Case) int {
		return strings.Compare(a.InstanceID, b.InstanceID)
	})
	return selected, nil
}

func loadCaseIDs(path string) (map[string]struct{}, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	ids := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		instanceID := strings.TrimSpace(scanner.Text())
		if instanceID == "" || strings.HasPrefix(instanceID, "#") {
			continue
		}
		if _, exists := ids[instanceID]; exists {
			return nil, fmt.Errorf("duplicate instance id %s", instanceID)
		}
		ids[instanceID] = struct{}{}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("case list is empty")
	}
	return ids, nil
}

func loadGoldLabels(path string) (map[string]goldLabel, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var labels []goldLabel
	if err := json.Unmarshal(data, &labels); err != nil {
		var single goldLabel
		if singleErr := json.Unmarshal(data, &single); singleErr == nil &&
			single.InstanceID != "" {
			labels = []goldLabel{single}
		} else {
			scanner := bufio.NewScanner(strings.NewReader(string(data)))
			scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				if line == "" {
					continue
				}
				var label goldLabel
				if lineErr := json.Unmarshal([]byte(line), &label); lineErr != nil {
					return nil, fmt.Errorf("parse label JSONL: %w", lineErr)
				}
				labels = append(labels, label)
			}
			if scanErr := scanner.Err(); scanErr != nil {
				return nil, scanErr
			}
		}
	}
	byID := make(map[string]goldLabel, len(labels))
	for _, label := range labels {
		label.InstanceID = strings.TrimSpace(label.InstanceID)
		if label.InstanceID == "" {
			return nil, fmt.Errorf("label has empty instance_id")
		}
		if strings.TrimSpace(label.Patch) == "" {
			return nil, fmt.Errorf("label %s has empty patch", label.InstanceID)
		}
		if _, exists := byID[label.InstanceID]; exists {
			return nil, fmt.Errorf("duplicate label %s", label.InstanceID)
		}
		byID[label.InstanceID] = label
	}
	return byID, nil
}

func completedResults(results []CaseResult, completed []bool) []CaseResult {
	values := make([]CaseResult, 0, len(results))
	for index, result := range results {
		if completed[index] {
			values = append(values, result)
		}
	}
	slices.SortFunc(values, func(a, b CaseResult) int {
		return strings.Compare(a.InstanceID, b.InstanceID)
	})
	return values
}

func aggregateResults(
	results []CaseResult,
	representations []tagagent.WorkspaceRepresentation,
) map[string]Aggregate {
	aggregates := make(map[string]Aggregate, len(representations))
	successes := make(map[string]int, len(representations))
	for _, representation := range representations {
		aggregates[string(representation)] = Aggregate{}
	}
	for _, result := range results {
		arms := make(map[string]ArmResult, len(result.Arms))
		for _, arm := range result.Arms {
			arms[arm.Representation] = arm
		}
		for _, representation := range representations {
			name := string(representation)
			aggregate := aggregates[name]
			aggregate.Cases++
			if result.Error != "" {
				aggregate.Errors++
				aggregates[name] = aggregate
				continue
			}
			arm, ok := arms[name]
			if !ok {
				aggregate.Errors++
				aggregates[name] = aggregate
				continue
			}
			if arm.Embedding != nil {
				aggregate.EmbeddingRequests += arm.Embedding.Requests
				aggregate.EmbeddingInputs += arm.Embedding.Inputs
				aggregate.EmbeddingErrors += arm.Embedding.Errors
				aggregate.EmbeddingDurationMS += arm.Embedding.DurationMS
			}
			if arm.EmbeddingCache != nil {
				aggregate.CacheHits += arm.EmbeddingCache.Hits
				aggregate.CacheMisses += arm.EmbeddingCache.Misses
				aggregate.CacheWrites += arm.EmbeddingCache.Writes
			}
			if arm.Error != "" {
				aggregate.Errors++
				aggregates[name] = aggregate
				continue
			}
			successes[name]++
			aggregate.SuccessfulCases++
			aggregate.MeanTargetFileRecallAt4 += arm.Metrics.TargetFileRecallAt4
			aggregate.MeanTargetFileRecallAt6 += arm.Metrics.TargetFileRecallAt6
			aggregate.MeanTargetFileReciprocalRank += arm.Metrics.TargetFileReciprocalRank
			aggregate.MeanHunkAnchorRecallAt4 += arm.Metrics.HunkAnchorRecallAt4
			aggregate.MeanHunkAnchorRecallAt6 += arm.Metrics.HunkAnchorRecallAt6
			aggregate.MeanTargetFileCharPrecisionAt6 += arm.Metrics.TargetFileCharPrecisionAt6
			aggregate.MeanFileCoverage += arm.Index.FileCoverage
			aggregate.MeanDocuments += float64(arm.Index.Documents)
			aggregate.MeanIndexDurationMS += float64(arm.Index.DurationMS)
			aggregate.FallbackDocuments += arm.Index.FallbackDocuments
			aggregates[name] = aggregate
		}
	}
	for representation, aggregate := range aggregates {
		denominator := successes[representation]
		if denominator > 0 {
			value := float64(denominator)
			aggregate.MeanTargetFileRecallAt4 /= value
			aggregate.MeanTargetFileRecallAt6 /= value
			aggregate.MeanTargetFileReciprocalRank /= value
			aggregate.MeanHunkAnchorRecallAt4 /= value
			aggregate.MeanHunkAnchorRecallAt6 /= value
			aggregate.MeanTargetFileCharPrecisionAt6 /= value
			aggregate.MeanFileCoverage /= value
			aggregate.MeanDocuments /= value
			aggregate.MeanIndexDurationMS /= value
		}
		aggregates[representation] = aggregate
	}
	return aggregates
}

func failedCaseCount(results []CaseResult) int {
	failed := 0
	for _, result := range results {
		if result.Error != "" {
			failed++
			continue
		}
		for _, arm := range result.Arms {
			if arm.Error != "" {
				failed++
				break
			}
		}
	}
	return failed
}

func hashFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return hashText(string(data)), nil
}

func hashText(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
