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
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
)

// ErrEngineUnavailable is returned by the standalone command until a concrete
// offline workspace retrieval adapter is linked. The package deliberately has
// no fallback that copies or summarizes recorded retrieval traces.
var ErrEngineUnavailable = errors.New(
	"offline retrieval replay engine is not linked; a Build/Search adapter over the frozen corpus and embedding cache is required",
)

func classifySearchError(message string) string {
	switch message {
	case "search failed: no relevant documents found",
		"search failed: no relevant information found",
		"no relevant documents found",
		"no relevant information found":
		return "no_hit"
	default:
		return "error"
	}
}

// Replay rebuilds every case index, executes recorded queries sequentially,
// and compares exact ranked result fingerprints. Differences are represented
// by report.Status="mismatch" and are not execution errors.
func Replay(ctx context.Context, loaded *LoadedBundle, engine Engine) (*Report, error) {
	if loaded == nil {
		return nil, errors.New("loaded bundle is required")
	}
	if engine == nil {
		return nil, ErrEngineUnavailable
	}
	descriptor := engine.Descriptor()
	if err := validateEngineDescriptor(descriptor); err != nil {
		return nil, fmt.Errorf("runtime engine: %w", err)
	}
	if descriptor != loaded.Spec.Engine {
		return nil, fmt.Errorf(
			"runtime engine identity does not match bundle: got %+v, want %+v",
			descriptor,
			loaded.Spec.Engine,
		)
	}
	report := &Report{
		SchemaVersion: ReportSchemaVersion, Kind: ReportKind, Status: "completed",
		BundleSHA256:            loaded.BundleSHA256,
		NativeRunManifestSHA256: loaded.NativeManifestSHA256,
		NativeRunID:             loaded.Spec.NativeRun.RunID,
		Engine:                  descriptor, EngineConfigSHA256: loaded.Spec.EngineConfig.SHA256,
		CaseSetSHA256: loaded.Spec.CaseSetSHA256,
		Cases:         make([]CaseReport, 0, len(loaded.Cases)),
	}
	for _, one := range loaded.Cases {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		caseReport, err := replayCase(ctx, loaded.EngineConfig, one, engine)
		if err != nil {
			return nil, fmt.Errorf("replay case %s: %w", one.Spec.InstanceID, err)
		}
		report.Cases = append(report.Cases, caseReport)
		report.Summary.Cases++
		for _, query := range caseReport.Queries {
			report.Summary.Queries++
			if query.Match {
				report.Summary.Matches++
			} else {
				report.Summary.Mismatches++
				report.Status = "mismatch"
			}
		}
	}
	return report, nil
}

func replayCase(
	ctx context.Context,
	engineConfig VerifiedArtifact,
	one LoadedCase,
	engine Engine,
) (CaseReport, error) {
	index, err := engine.Build(ctx, BuildInput{
		InstanceID: one.Spec.InstanceID, Repository: one.Native.Repo,
		Corpus: one.Corpus, EmbeddingCache: one.EmbeddingCache,
		EngineConfig: engineConfig, Provenance: one.Spec.Retrieval,
	})
	if err != nil {
		return CaseReport{}, fmt.Errorf("build offline index: %w", err)
	}
	if index == nil {
		return CaseReport{}, errors.New("engine returned a nil index")
	}
	result, replayErr := searchCase(ctx, one, index)
	closeErr := index.Close()
	if replayErr != nil || closeErr != nil {
		return CaseReport{}, errors.Join(replayErr, closeErr)
	}
	return result, nil
}

func searchCase(ctx context.Context, one LoadedCase, index Index) (CaseReport, error) {
	if err := validateIndexIdentity(index.Identity(), one.Spec); err != nil {
		return CaseReport{}, err
	}
	report := CaseReport{
		InstanceID: one.Spec.InstanceID, InputSHA256: one.Spec.InputSHA256,
		NativeArtifactSHA256: one.NativeSHA256, ResponsesSHA256: one.ResponsesSHA256,
		CorpusSHA256: one.Spec.Corpus.SHA256, QuerySetSHA256: one.QuerySetSHA256,
		ExpectedResultsSHA256:         one.ExpectedSHA256,
		EmittedCodeSearchCalls:        one.Queries.EmittedCodeSearchCalls,
		ExecutedCodeSearchCalls:       len(one.Queries.Queries),
		SkippedEmittedCodeSearchCalls: one.Queries.SkippedEmittedCodeSearchCalls,
		Queries:                       make([]QueryComparison, 0, len(one.Queries.Queries)), Match: true,
	}
	for queryIndex, query := range one.Queries.Queries {
		if err := ctx.Err(); err != nil {
			return CaseReport{}, err
		}
		documents, searchErr := index.Search(ctx, SearchRequest{
			Ordinal: query.Ordinal, Query: query.Query, QuerySHA256: query.QuerySHA256,
			Arguments:       append([]byte(nil), query.Arguments...),
			ArgumentsSHA256: query.ArgumentsSHA256,
			MaxResults:      one.Spec.Retrieval.MaxResults,
		})
		if err := ctx.Err(); err != nil {
			return CaseReport{}, err
		}
		actualStatus := "success"
		actualErrorSHA := ""
		actual := []RankedDocument{}
		if searchErr != nil {
			actualStatus = classifySearchError(searchErr.Error())
			actualErrorSHA = digestBytes([]byte(searchErr.Error()))
		} else {
			var err error
			actual, err = rankEngineDocuments(documents, one.Spec.Retrieval.MaxResults)
			if err != nil {
				return CaseReport{}, fmt.Errorf("search query %d result: %w", query.Ordinal, err)
			}
			if len(actual) == 0 {
				return CaseReport{}, fmt.Errorf(
					"search query %d returned zero documents without the knowledge-tool no-hit error",
					query.Ordinal,
				)
			}
		}
		actualSHA, err := OutcomeSHA256(
			query.QuerySHA256,
			actualStatus,
			actualErrorSHA,
			actual,
		)
		if err != nil {
			return CaseReport{}, err
		}
		expected := one.Expected.Results[queryIndex]
		match := expected.Status == actualStatus && expected.ErrorSHA256 == actualErrorSHA &&
			expected.ResultSHA256 == actualSHA && slices.Equal(expected.Documents, actual)
		report.Queries = append(report.Queries, QueryComparison{
			Ordinal: query.Ordinal, QuerySHA256: query.QuerySHA256,
			ExpectedStatus: expected.Status, ActualStatus: actualStatus,
			ExpectedErrorSHA256: expected.ErrorSHA256, ActualErrorSHA256: actualErrorSHA,
			ExpectedResultSHA256: expected.ResultSHA256, ActualResultSHA256: actualSHA,
			Match:             match,
			ExpectedDocuments: append([]RankedDocument(nil), expected.Documents...),
			ActualDocuments:   append([]RankedDocument(nil), actual...),
		})
		if !match {
			report.Match = false
		}
	}
	return report, nil
}

func validateIndexIdentity(actual IndexIdentity, spec CaseSpec) error {
	expectedEmbeddingSHA := ""
	if spec.Retrieval.Embedding != nil {
		expectedEmbeddingSHA = spec.Retrieval.Embedding.IdentitySHA256
	}
	expectedCacheSHA := ""
	if spec.EmbeddingCache != nil {
		expectedCacheSHA = spec.EmbeddingCache.SHA256
	}
	expected := IndexIdentity{
		CorpusSHA256:                  spec.Retrieval.CorpusSHA256,
		EligibleFileSetSHA256:         spec.Retrieval.EligibleFileSetSHA256,
		EligibleContentSHA256:         spec.Retrieval.EligibleContentSHA256,
		DocumentSetSHA256:             spec.Retrieval.DocumentSetSHA256,
		WorkspaceRepresentation:       spec.Retrieval.WorkspaceRepresentation,
		WorkspaceRepresentationSchema: spec.Retrieval.WorkspaceRepresentationSchema,
		WorkspaceRepresentationSHA256: spec.Retrieval.WorkspaceRepresentationSHA256,
		ParserDependency:              spec.Retrieval.ParserDependency,
		ParserRuntimeSHA256:           spec.Retrieval.ParserRuntimeSHA256,
		RetrievalMode:                 spec.Retrieval.RetrievalMode,
		MaxResults:                    spec.Retrieval.MaxResults,
		InvocationDedup:               spec.Retrieval.InvocationDedup,
		EmbeddingIdentitySHA256:       expectedEmbeddingSHA,
		EmbeddingCacheSHA256:          expectedCacheSHA,
	}
	if actual != expected {
		return fmt.Errorf("rebuilt index identity does not match frozen provenance: got %+v, want %+v", actual, expected)
	}
	return nil
}

func rankEngineDocuments(documents []Document, maxResults int) ([]RankedDocument, error) {
	if documents == nil {
		documents = []Document{}
	}
	if len(documents) > maxResults {
		return nil, fmt.Errorf("engine returned %d documents, max_results=%d", len(documents), maxResults)
	}
	ranked := make([]RankedDocument, len(documents))
	for index, document := range documents {
		if math.IsNaN(document.Score) || math.IsInf(document.Score, 0) {
			return nil, fmt.Errorf("document %d has non-finite score", index+1)
		}
		ranked[index] = RankedDocument{
			Rank: index + 1, DocumentID: document.ID, SourcePath: document.SourcePath,
			ContentSHA256: document.ContentSHA256, MetadataSHA256: document.MetadataSHA256,
			ScoreBits: fmt.Sprintf("%016x", math.Float64bits(document.Score)),
		}
	}
	if err := validateRankedDocuments(ranked); err != nil {
		return nil, err
	}
	return ranked, nil
}
