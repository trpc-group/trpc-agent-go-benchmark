//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/internal/benchruntime"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/internal/longmemeval"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

func buildLongMemEvalConfig(llm model.Model) longmemeval.Config {
	return longmemeval.Config{
		ModelName:              getModelName(),
		Model:                  llm,
		OutputDir:              *flagOutput,
		Scenario:               *flagScenario,
		DatasetPath:            longMemEvalDatasetPath(),
		ManifestPath:           strings.TrimSpace(*flagLMEManifest),
		ReplayRoot:             longMemEvalReplayRoot(),
		BuildMaxTokens:         *flagLMEBuildMaxTokens,
		BuildTokenizerModel:    strings.TrimSpace(*flagLMEBuildTokenizerModel),
		BuildTokenizerEncoding: strings.TrimSpace(*flagLMEBuildTokenizerEncoding),
		QuestionTypes:          parseCSV(*flagLMEQuestionTypes),
		MaxTasks:               *flagMaxTasks,
		MaxRetries:             max(*flagLMEMaxRetries, 0),
		AnswerMaxTokens:        max(*flagLMEAnswerMaxTokens, 1),
		JudgeMaxTokens:         max(*flagLMEJudgeMaxTokens, 1),
		AutoExtractionWait:     *flagLMEExtractionWait,
		AutoQAOnly:             *flagLMEAutoQAOnly,
		AutoMemoryTable:        longMemEvalAutoMemoryTableName(),
		AutoUpdatePolicy:       strings.TrimSpace(*flagLMEAutoUpdatePolicy),
		ConversationExtraction: strings.TrimSpace(*flagLMEConversationExtraction),
		EmbeddingCacheEnabled:  *flagLMEEmbeddingCache,
		EmbeddingCachePath:     longMemEvalEmbeddingCachePath(getEmbedModelName()),
		EmbedModelName:         getEmbedModelName(),
		PGVectorDSN:            getPGVectorDSN(),
		LLMBaseURL:             lmeLLMBaseURL(),
		EmbeddingAPIKey:        firstNonEmptyEnv(envOpenAIEmbeddingAPIKey, envOpenAIAPIKey),
		EmbeddingBaseURL:       firstNonEmptyEnv(envOpenAIEmbeddingBaseURL, envOpenAIBaseURL),
		TableSuffix:            *flagTableSuffix,
		Resume:                 *flagResume,
		Mem0Host:               longMemEvalMem0Host(),
		Mem0APIKey:             longMemEvalMem0APIKey(),
		Mem0Version:            longMemEvalMem0Version(),
		Mem0Revision:           longMemEvalMem0Revision(),
		Mem0PreflightPath:      longMemEvalMem0PreflightPath(),
		Mem0IngestTimeout:      *flagMem0IngestTimeout,
		Mem0ProxyUsageLog:      strings.TrimSpace(*flagMem0ProxyUsageLog),
		Mem0ProxyRunID:         longMemEvalMem0ProxyRunID(),
		TraceContentMode:       strings.TrimSpace(*flagLMETraceContent),
		TraceGzip:              *flagLMETraceGzip,
	}
}

func longMemEvalDatasetPath() string {
	if strings.HasSuffix(*flagDataset, ".json") || strings.HasSuffix(*flagDataset, ".jsonl") {
		return *flagDataset
	}
	dataFile := *flagDataFile
	if dataFile == "locomo10.json" {
		dataFile = "longmemeval_s_cleaned.json"
	}
	return filepath.Join(*flagDataset, dataFile)
}

func longMemEvalReplayRoot() string {
	if root := strings.TrimSpace(*flagLMEReplayRoot); root != "" {
		return root
	}
	return filepath.Join(*flagOutput, "longmemeval", "replay")
}

func longMemEvalAutoMemoryTableName() string {
	if table := strings.TrimSpace(*flagLMEAutoMemoryTable); table != "" {
		return table
	}
	return tableNameWithSuffix(pgvectorTableAutoBase)
}

func longMemEvalEmbeddingCachePath(modelName string) string {
	cacheDir := strings.TrimSpace(*flagLMEEmbeddingCacheDir)
	if cacheDir == "" {
		cacheDir = filepath.Join(*flagOutput, "longmemeval", ".cache")
	}
	return filepath.Join(cacheDir, fmt.Sprintf(
		"embeddings_%s",
		benchruntime.SanitizeCacheFileName(modelName),
	))
}

func longMemEvalMem0Host() string {
	if host := strings.TrimSpace(*flagMem0Host); host != "" {
		return host
	}
	return strings.TrimSpace(os.Getenv("MEM0_HOST"))
}

func longMemEvalMem0APIKey() string {
	if apiKey := strings.TrimSpace(*flagMem0APIKey); apiKey != "" {
		return apiKey
	}
	return strings.TrimSpace(os.Getenv("MEM0_API_KEY"))
}

func longMemEvalMem0Version() string {
	if version := strings.TrimSpace(*flagMem0Version); version != "" {
		return version
	}
	return strings.TrimSpace(os.Getenv("MEM0_VERSION"))
}

func longMemEvalMem0Revision() string {
	if revision := strings.TrimSpace(*flagMem0Revision); revision != "" {
		return revision
	}
	return strings.TrimSpace(os.Getenv("MEM0_REVISION"))
}

func longMemEvalMem0PreflightPath() string {
	if path := strings.TrimSpace(*flagMem0Preflight); path != "" {
		return path
	}
	return strings.TrimSpace(os.Getenv("MEM0_PREFLIGHT"))
}

func longMemEvalMem0ProxyRunID() string {
	if runID := strings.TrimSpace(*flagMem0ProxyRunID); runID != "" {
		return runID
	}
	return strings.TrimSpace(os.Getenv("MEM0_PROXY_RUN_ID"))
}
