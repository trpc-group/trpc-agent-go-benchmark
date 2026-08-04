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
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/tiktoken-go/tokenizer"
)

const lmeBuildPlanVersion = 5

type lmeBuildPlanConfig struct {
	Tokenizer    string `json:"tokenizer"`
	Model        string `json:"model"`
	Encoding     string `json:"encoding,omitempty"`
	MaxTokens    int    `json:"max_tokens"`
	ReplayDigest string `json:"replay_digest"`
}

type lmeBuildStats struct {
	CaseCount             int `json:"case_count"`
	SessionCount          int `json:"session_count"`
	TurnCount             int `json:"turn_count"`
	PairCount             int `json:"pair_count"`
	ChunkCount            int `json:"chunk_count"`
	ChunkedSessionCount   int `json:"chunked_session_count"`
	ChunkedPairCount      int `json:"chunked_pair_count"`
	SplitTurnCount        int `json:"split_turn_count"`
	OriginalTokens        int `json:"original_tokens"`
	FinalTokens           int `json:"final_tokens"`
	OriginalBytes         int `json:"original_bytes"`
	FinalBytes            int `json:"final_bytes"`
	MaxOriginalTurnTokens int `json:"max_original_turn_tokens"`
	MaxOriginalPairTokens int `json:"max_original_pair_tokens"`
	MaxSessionTokens      int `json:"max_session_tokens"`
	MaxChunkTokens        int `json:"max_chunk_tokens"`
}

type lmeBuildPlanIndex struct {
	Version         int                `json:"version"`
	Protocol        string             `json:"protocol"`
	Config          lmeBuildPlanConfig `json:"config"`
	ConfigDigest    string             `json:"config_digest"`
	BuildPlanDigest string             `json:"build_plan_digest"`
	Stats           lmeBuildStats      `json:"stats"`
	Cases           []lmeArtifactEntry `json:"cases"`
}

type lmeBuildPlanCorpus struct {
	Index *lmeBuildPlanIndex
	Cases map[string]*lmeBuildCasePlan
}

type lmeBuildCasePlan struct {
	Version      int                   `json:"version"`
	CaseID       string                `json:"case_id"`
	ReplayDigest string                `json:"replay_digest"`
	ConfigDigest string                `json:"config_digest"`
	Stats        lmeBuildStats         `json:"stats"`
	Sessions     []lmeBuildSessionPlan `json:"sessions"`
}

type lmeBuildSessionPlan struct {
	SessionIndex    int                `json:"session_index"`
	SessionID       string             `json:"session_id"`
	ObservationTime string             `json:"observation_time"`
	Pairs           []lmeBuildPairPlan `json:"pairs"`
}

type lmeBuildPairPlan struct {
	PairID         string              `json:"pair_id"`
	SourceTurnIDs  []string            `json:"source_turn_ids"`
	OriginalTokens int                 `json:"original_tokens"`
	FinalTokens    int                 `json:"final_tokens"`
	OriginalBytes  int                 `json:"original_bytes"`
	FinalBytes     int                 `json:"final_bytes"`
	Chunks         []lmeBuildChunkPlan `json:"chunks"`
}

type lmeBuildChunkPlan struct {
	ChunkID    string             `json:"chunk_id"`
	Index      int                `json:"index"`
	TokenCount int                `json:"token_count"`
	ByteCount  int                `json:"byte_count"`
	Turns      []lmeBuildTurnPart `json:"turns"`
}

type lmeBuildTurnPart struct {
	SourceTurnID    string `json:"source_turn_id"`
	SourceTurnIndex int    `json:"source_turn_index"`
	Role            string `json:"role"`
	Content         string `json:"content"`
	StartByte       int    `json:"start_byte"`
	EndByte         int    `json:"end_byte"`
	StartToken      int    `json:"start_token"`
	EndToken        int    `json:"end_token"`
}

type lmeBuildPlanner struct {
	config  lmeBuildPlanConfig
	chunker *lmeTextChunker
}

func newLMEBuildPlanner(
	config lmeBuildPlanConfig,
	tokenize lmeTokenizeFunc,
) (*lmeBuildPlanner, error) {
	if err := validateLMEBuildPlanConfig(config); err != nil {
		return nil, err
	}
	chunker, err := newLMETextChunker(tokenize, config.MaxTokens)
	if err != nil {
		return nil, fmt.Errorf("create LongMemEval build chunker: %w", err)
	}
	return &lmeBuildPlanner{config: config, chunker: chunker}, nil
}

func validateLMEBuildPlanConfig(config lmeBuildPlanConfig) error {
	if config.Tokenizer == "" || config.Model == "" || config.ReplayDigest == "" {
		return fmt.Errorf("LongMemEval build plan configuration is incomplete")
	}
	if config.MaxTokens <= 0 {
		return fmt.Errorf("LongMemEval build max tokens must be positive: %d", config.MaxTokens)
	}
	return nil
}

func (p *lmeBuildPlanner) buildCase(
	replayCase *lmeReplayCase,
	configDigest string,
) (*lmeBuildCasePlan, error) {
	if replayCase == nil {
		return nil, fmt.Errorf("nil replay case")
	}
	plan := &lmeBuildCasePlan{
		Version:      lmeBuildPlanVersion,
		CaseID:       replayCase.CaseID,
		ReplayDigest: p.config.ReplayDigest,
		ConfigDigest: configDigest,
		Sessions:     make([]lmeBuildSessionPlan, 0, len(replayCase.Sessions)),
	}
	plan.Stats.CaseCount = 1
	for _, replaySession := range replayCase.Sessions {
		sessionPlan, err := p.buildSession(replayCase.CaseID, replaySession)
		if err != nil {
			return nil, fmt.Errorf("build session %s: %w", replaySession.SessionID, err)
		}
		plan.Sessions = append(plan.Sessions, sessionPlan)
		accumulateLMEBuildSessionStats(&plan.Stats, sessionPlan)
	}
	return plan, nil
}

func (p *lmeBuildPlanner) buildSession(
	caseID string,
	replaySession lmeReplaySession,
) (lmeBuildSessionPlan, error) {
	if _, err := time.Parse(time.RFC3339Nano, replaySession.ObservationTime); err != nil {
		return lmeBuildSessionPlan{}, fmt.Errorf("parse observation time: %w", err)
	}
	pairs, err := p.buildTurnPairs(caseID, replaySession)
	if err != nil {
		return lmeBuildSessionPlan{}, err
	}
	return lmeBuildSessionPlan{
		SessionIndex:    replaySession.SessionIndex,
		SessionID:       replaySession.SessionID,
		ObservationTime: replaySession.ObservationTime,
		Pairs:           pairs,
	}, nil
}

func (p *lmeBuildPlanner) buildTurnPairs(
	caseID string,
	sess lmeReplaySession,
) ([]lmeBuildPairPlan, error) {
	groups, err := lmeTurnPairGroups(sess.Turns)
	if err != nil {
		return nil, fmt.Errorf("group session %s turns: %w", sess.SessionID, err)
	}
	pairs := make([]lmeBuildPairPlan, 0, len(groups))
	for _, turns := range groups {
		pair, err := p.buildTurnPair(caseID, sess.SessionID, len(pairs), turns)
		if err != nil {
			return nil, err
		}
		pairs = append(pairs, pair)
	}
	return pairs, nil
}

func lmeTurnPairGroups(turns []lmeReplayTurn) ([][]lmeReplayTurn, error) {
	groups := make([][]lmeReplayTurn, 0, (len(turns)+1)/2)
	for i := 0; i < len(turns); {
		if turns[i].Role != "user" && turns[i].Role != "assistant" {
			return nil, fmt.Errorf(
				"turn %s at index %d has unsupported role %q",
				turns[i].TurnID,
				i,
				turns[i].Role,
			)
		}
		group := []lmeReplayTurn{turns[i]}
		leadingAssistant := turns[i].Role == "assistant"
		i++
		if !leadingAssistant && i < len(turns) && turns[i].Role == "assistant" {
			group = append(group, turns[i])
			i++
		}
		groups = append(groups, group)
	}
	return groups, nil
}

func (p *lmeBuildPlanner) buildTurnPair(
	caseID string,
	sessionID string,
	pairIndex int,
	turns []lmeReplayTurn,
) (lmeBuildPairPlan, error) {
	pairID := lmeStableArtifactID(
		"build-pair",
		caseID,
		sessionID,
		strconv.Itoa(pairIndex),
	)
	chunkTurns := make([]lmeTextTurn, 0, len(turns))
	for _, turn := range turns {
		if turn.Role != "user" && turn.Role != "assistant" {
			return lmeBuildPairPlan{}, fmt.Errorf("turn %s has unsupported role %q", turn.TurnID, turn.Role)
		}
		chunkTurns = append(chunkTurns, lmeTextTurn{Role: turn.Role, Content: turn.Content})
	}
	chunkPlan, err := p.chunker.chunkTurns(chunkTurns)
	if err != nil {
		return lmeBuildPairPlan{}, fmt.Errorf("chunk build pair %s: %w", pairID, err)
	}
	pair := lmeBuildPairPlan{
		PairID:         pairID,
		OriginalTokens: chunkPlan.OriginalTokens,
		FinalTokens:    chunkPlan.FinalTokens,
		OriginalBytes:  chunkPlan.OriginalBytes,
		FinalBytes:     chunkPlan.FinalBytes,
		Chunks:         make([]lmeBuildChunkPlan, 0, len(chunkPlan.Chunks)),
	}
	for _, turn := range turns {
		pair.SourceTurnIDs = append(pair.SourceTurnIDs, turn.TurnID)
	}
	tokenOffsets := make([]int, len(turns))
	for chunkIndex, sourceChunk := range chunkPlan.Chunks {
		chunk, err := lmeBuildChunkFromTokenChunk(
			sourceChunk,
			turns,
			pairID,
			chunkIndex,
			tokenOffsets,
		)
		if err != nil {
			return lmeBuildPairPlan{}, err
		}
		pair.Chunks = append(pair.Chunks, chunk)
	}
	return pair, nil
}

func lmeBuildChunkFromTokenChunk(
	source lmeTextChunk,
	turns []lmeReplayTurn,
	pairID string,
	chunkIndex int,
	tokenOffsets []int,
) (lmeBuildChunkPlan, error) {
	chunkID := lmeStableArtifactID("build-chunk", pairID, strconv.Itoa(chunkIndex))
	chunk := lmeBuildChunkPlan{
		ChunkID:    chunkID,
		Index:      chunkIndex,
		TokenCount: source.TokenCount,
	}
	for _, span := range source.Spans {
		sourceIndex := span.TurnIndex
		if sourceIndex >= len(turns) || sourceIndex >= len(tokenOffsets) {
			return lmeBuildChunkPlan{}, fmt.Errorf("chunk source turn index %d out of range", sourceIndex)
		}
		turn := turns[sourceIndex]
		startToken := tokenOffsets[sourceIndex]
		endToken := startToken + span.TokenCount
		tokenOffsets[sourceIndex] = endToken
		chunk.ByteCount += len(span.Content)
		chunk.Turns = append(chunk.Turns, lmeBuildTurnPart{
			SourceTurnID:    turn.TurnID,
			SourceTurnIndex: turn.TurnIndex,
			Role:            turn.Role,
			Content:         span.Content,
			StartByte:       span.StartByte,
			EndByte:         span.EndByte,
			StartToken:      startToken,
			EndToken:        endToken,
		})
	}
	return chunk, nil
}

func accumulateLMEBuildSessionStats(stats *lmeBuildStats, sessionPlan lmeBuildSessionPlan) {
	stats.SessionCount++
	sessionTokens := 0
	sessionChunked := false
	for _, pair := range sessionPlan.Pairs {
		stats.PairCount++
		stats.TurnCount += len(pair.SourceTurnIDs)
		if len(pair.Chunks) > 1 {
			stats.ChunkedPairCount++
			sessionChunked = true
		}
		stats.ChunkCount += len(pair.Chunks)
		stats.OriginalTokens += pair.OriginalTokens
		stats.FinalTokens += pair.FinalTokens
		stats.OriginalBytes += pair.OriginalBytes
		stats.FinalBytes += pair.FinalBytes
		stats.MaxOriginalPairTokens = max(stats.MaxOriginalPairTokens, pair.OriginalTokens)
		sessionTokens += pair.OriginalTokens

		turnTokens := make(map[string]int, len(pair.SourceTurnIDs))
		turnChunks := make(map[string]map[string]struct{}, len(pair.SourceTurnIDs))
		for _, chunk := range pair.Chunks {
			stats.MaxChunkTokens = max(stats.MaxChunkTokens, chunk.TokenCount)
			for _, part := range chunk.Turns {
				turnTokens[part.SourceTurnID] += part.EndToken - part.StartToken
				if turnChunks[part.SourceTurnID] == nil {
					turnChunks[part.SourceTurnID] = make(map[string]struct{})
				}
				turnChunks[part.SourceTurnID][chunk.ChunkID] = struct{}{}
			}
		}
		for _, sourceTurnID := range pair.SourceTurnIDs {
			stats.MaxOriginalTurnTokens = max(
				stats.MaxOriginalTurnTokens,
				turnTokens[sourceTurnID],
			)
			if len(turnChunks[sourceTurnID]) > 1 {
				stats.SplitTurnCount++
			}
		}
	}
	if sessionChunked {
		stats.ChunkedSessionCount++
	}
	stats.MaxSessionTokens = max(stats.MaxSessionTokens, sessionTokens)
}

func addLMEBuildStats(target *lmeBuildStats, source lmeBuildStats) {
	target.CaseCount += source.CaseCount
	target.SessionCount += source.SessionCount
	target.TurnCount += source.TurnCount
	target.PairCount += source.PairCount
	target.ChunkCount += source.ChunkCount
	target.ChunkedSessionCount += source.ChunkedSessionCount
	target.ChunkedPairCount += source.ChunkedPairCount
	target.SplitTurnCount += source.SplitTurnCount
	target.OriginalTokens += source.OriginalTokens
	target.FinalTokens += source.FinalTokens
	target.OriginalBytes += source.OriginalBytes
	target.FinalBytes += source.FinalBytes
	target.MaxOriginalTurnTokens = max(target.MaxOriginalTurnTokens, source.MaxOriginalTurnTokens)
	target.MaxOriginalPairTokens = max(target.MaxOriginalPairTokens, source.MaxOriginalPairTokens)
	target.MaxSessionTokens = max(target.MaxSessionTokens, source.MaxSessionTokens)
	target.MaxChunkTokens = max(target.MaxChunkTokens, source.MaxChunkTokens)
}

const lmeChunkFragmentAttempts = 8

var errLMEChunkLimitTooSmall = errors.New(
	"token limit is too small for a UTF-8 fragment",
)

type lmeTokenizeFunc func(string) ([]string, error)

type lmeTextTokenizer struct {
	codec tokenizer.Codec
	model string
}

func newLMETextTokenizer(modelName, encodingName string) (*lmeTextTokenizer, error) {
	modelName = strings.TrimSpace(modelName)
	if modelName == "" {
		return nil, errors.New("LongMemEval tokenizer model is required")
	}
	encodingName = strings.TrimSpace(encodingName)
	if encodingName != "" {
		codec, err := tokenizer.Get(tokenizer.Encoding(encodingName))
		if err != nil {
			return nil, fmt.Errorf("resolve tokenizer encoding %q: %w", encodingName, err)
		}
		return &lmeTextTokenizer{codec: codec, model: modelName}, nil
	}
	codec, err := tokenizer.ForModel(tokenizer.Model(modelName))
	if err != nil {
		return nil, fmt.Errorf(
			"resolve tokenizer model %q; configure an explicit encoding for an alias: %w",
			modelName,
			err,
		)
	}
	return &lmeTextTokenizer{codec: codec, model: modelName}, nil
}

func (t *lmeTextTokenizer) name() string {
	return t.codec.GetName()
}

func (t *lmeTextTokenizer) tokenize(text string) ([]string, error) {
	_, pieces, err := t.codec.Encode(text)
	if err != nil {
		return nil, fmt.Errorf("encode text: %w", err)
	}
	return pieces, nil
}

type lmeTextTurn struct {
	Role    string
	Content string
}

type lmeTextSpan struct {
	TurnIndex  int
	Role       string
	Content    string
	StartByte  int
	EndByte    int
	TokenCount int
}

type lmeTextChunk struct {
	TokenCount int
	Spans      []lmeTextSpan
}

type lmeTextChunkResult struct {
	OriginalTokens int
	FinalTokens    int
	OriginalBytes  int
	FinalBytes     int
	Chunks         []lmeTextChunk
}

type lmeTokenPiece struct {
	endByte int
}

type lmeEncodedTurn struct {
	role    string
	content string
	tokens  []lmeTokenPiece
}

type lmeTextChunker struct {
	tokenize  lmeTokenizeFunc
	maxTokens int
	mu        sync.Mutex
}

func newLMETextChunker(
	tokenize lmeTokenizeFunc,
	maxTokens int,
) (*lmeTextChunker, error) {
	if tokenize == nil {
		return nil, errors.New("LongMemEval tokenizer is required")
	}
	if maxTokens <= 0 {
		return nil, fmt.Errorf("LongMemEval token limit must be positive: %d", maxTokens)
	}
	return &lmeTextChunker{tokenize: tokenize, maxTokens: maxTokens}, nil
}

// chunkTurns preserves turn order, UTF-8 boundaries, and complete source-byte
// coverage while enforcing the provider token limit.
func (c *lmeTextChunker) chunkTurns(turns []lmeTextTurn) (*lmeTextChunkResult, error) {
	encoded, originalTokens, originalBytes, err := c.encodeTurns(turns)
	if err != nil {
		return nil, err
	}
	chunks, err := c.buildChunks(encoded)
	if err != nil {
		return nil, err
	}
	result := &lmeTextChunkResult{
		OriginalTokens: originalTokens,
		OriginalBytes:  originalBytes,
		Chunks:         chunks,
	}
	for _, chunk := range chunks {
		result.FinalTokens += chunk.TokenCount
		for _, span := range chunk.Spans {
			result.FinalBytes += len(span.Content)
		}
	}
	return result, nil
}

func (c *lmeTextChunker) encodeTurns(
	turns []lmeTextTurn,
) ([]lmeEncodedTurn, int, int, error) {
	encoded := make([]lmeEncodedTurn, 0, len(turns))
	totalTokens := 0
	totalBytes := 0
	for i, turn := range turns {
		if !utf8.ValidString(turn.Content) {
			return nil, 0, 0, fmt.Errorf("turn %d contains invalid UTF-8", i)
		}
		pieces, err := c.tokenizeText(turn.Content)
		if err != nil {
			return nil, 0, 0, fmt.Errorf("tokenize turn %d: %w", i, err)
		}
		encoded = append(encoded, lmeEncodedTurn{
			role:    turn.Role,
			content: turn.Content,
			tokens:  pieces,
		})
		totalTokens += len(pieces)
		totalBytes += len(turn.Content)
	}
	return encoded, totalTokens, totalBytes, nil
}

func (c *lmeTextChunker) tokenizeText(text string) ([]lmeTokenPiece, error) {
	if text == "" {
		return nil, nil
	}
	c.mu.Lock()
	raw, err := c.tokenize(text)
	c.mu.Unlock()
	if err != nil {
		return nil, err
	}
	pieces := make([]lmeTokenPiece, 0, len(raw))
	byteOffset := 0
	for i, piece := range raw {
		if piece == "" {
			return nil, fmt.Errorf("token %d is empty", i)
		}
		endByte := byteOffset + len(piece)
		if endByte > len(text) || text[byteOffset:endByte] != piece {
			return nil, fmt.Errorf("token %d does not match source bytes", i)
		}
		pieces = append(pieces, lmeTokenPiece{endByte: endByte})
		byteOffset = endByte
	}
	if byteOffset != len(text) {
		return nil, fmt.Errorf(
			"tokenized bytes %d do not match source bytes %d",
			byteOffset,
			len(text),
		)
	}
	return pieces, nil
}

func (c *lmeTextChunker) buildChunks(turns []lmeEncodedTurn) ([]lmeTextChunk, error) {
	if len(turns) == 0 {
		return nil, nil
	}
	var chunks []lmeTextChunk
	var current *lmeTextChunk
	for turnIndex, turn := range turns {
		if len(turn.tokens) == 0 {
			current, chunks = appendLMEEmptySpan(current, chunks, turnIndex, turn)
			continue
		}
		startToken := 0
		for startToken < len(turn.tokens) {
			if current == nil {
				current = &lmeTextChunk{}
			}
			remaining := c.maxTokens - current.TokenCount
			if remaining == 0 {
				chunks = append(chunks, *current)
				current = nil
				continue
			}
			endToken, tokenCount, err := c.fragmentEnd(turn, startToken, remaining)
			if err != nil {
				return nil, fmt.Errorf("split turn %d: %w", turnIndex, err)
			}
			if endToken == startToken {
				if current.TokenCount > 0 {
					chunks = append(chunks, *current)
					current = nil
					continue
				}
				return nil, fmt.Errorf(
					"turn %d with limit %d: %w",
					turnIndex,
					c.maxTokens,
					errLMEChunkLimitTooSmall,
				)
			}
			appendLMETextSpan(current, turnIndex, turn, startToken, endToken, tokenCount)
			startToken = endToken
			if startToken < len(turn.tokens) || current.TokenCount == c.maxTokens {
				chunks = append(chunks, *current)
				current = nil
			}
		}
	}
	if current != nil {
		chunks = append(chunks, *current)
	}
	return chunks, nil
}

func appendLMEEmptySpan(
	current *lmeTextChunk,
	chunks []lmeTextChunk,
	turnIndex int,
	turn lmeEncodedTurn,
) (*lmeTextChunk, []lmeTextChunk) {
	span := lmeTextSpan{TurnIndex: turnIndex, Role: turn.role}
	if current != nil {
		current.Spans = append(current.Spans, span)
		return current, chunks
	}
	if len(chunks) > 0 {
		chunks[len(chunks)-1].Spans = append(chunks[len(chunks)-1].Spans, span)
		return nil, chunks
	}
	return &lmeTextChunk{Spans: []lmeTextSpan{span}}, chunks
}

func (c *lmeTextChunker) fragmentEnd(
	turn lmeEncodedTurn,
	startToken int,
	maxTokens int,
) (int, int, error) {
	endToken := len(turn.tokens)
	if maxTokens < len(turn.tokens)-startToken {
		endToken = startToken + maxTokens
	}
	startByte := lmeTokenStartByte(turn.tokens, startToken)
	attempts := 0
	for endToken > startToken {
		endByte := turn.tokens[endToken-1].endByte
		fragment := turn.content[startByte:endByte]
		if !utf8.ValidString(fragment) {
			endToken--
			continue
		}
		pieces, err := c.tokenizeText(fragment)
		if err != nil {
			return 0, 0, fmt.Errorf("tokenize output fragment: %w", err)
		}
		if len(pieces) <= maxTokens {
			return endToken, len(pieces), nil
		}
		span := endToken - startToken
		if span == 1 {
			return startToken, 0, nil
		}
		attempts++
		nextSpan := span / 2
		if attempts <= lmeChunkFragmentAttempts {
			nextSpan = span * maxTokens / len(pieces)
		}
		if nextSpan >= span {
			nextSpan = span - 1
		}
		if nextSpan < 1 {
			nextSpan = 1
		}
		endToken = startToken + nextSpan
	}
	return startToken, 0, nil
}

func appendLMETextSpan(
	chunk *lmeTextChunk,
	turnIndex int,
	turn lmeEncodedTurn,
	startToken int,
	endToken int,
	tokenCount int,
) {
	startByte := lmeTokenStartByte(turn.tokens, startToken)
	endByte := turn.tokens[endToken-1].endByte
	chunk.Spans = append(chunk.Spans, lmeTextSpan{
		TurnIndex:  turnIndex,
		Role:       turn.role,
		Content:    turn.content[startByte:endByte],
		StartByte:  startByte,
		EndByte:    endByte,
		TokenCount: tokenCount,
	})
	chunk.TokenCount += tokenCount
}

func lmeTokenStartByte(tokens []lmeTokenPiece, tokenIndex int) int {
	if tokenIndex == 0 {
		return 0
	}
	return tokens[tokenIndex-1].endByte
}

// ensureLMEBuildPlan creates or validates the immutable plan consumed by every
// comparable backend.
func ensureLMEBuildPlan(
	cfg lmeRunConfig,
	replay *lmeReplayCorpus,
) (*lmeBuildPlanCorpus, string, error) {
	if replay == nil || replay.Index == nil {
		return nil, "", fmt.Errorf("nil replay corpus for build plan")
	}
	tokenizer, err := newLMETextTokenizer(
		cfg.BuildTokenizerModel,
		cfg.BuildTokenizerEncoding,
	)
	if err != nil {
		return nil, "", fmt.Errorf("create LongMemEval tokenizer: %w", err)
	}
	config := lmeBuildPlanConfig{
		Tokenizer:    tokenizer.name(),
		Model:        tokenizer.model,
		Encoding:     cfg.BuildTokenizerEncoding,
		MaxTokens:    cfg.BuildMaxTokens,
		ReplayDigest: replay.Index.ReplayDigest,
	}
	configDigest, err := lmeJSONDigest(config)
	if err != nil {
		return nil, "", fmt.Errorf("digest LongMemEval build config: %w", err)
	}
	root := filepath.Join(filepath.Dir(cfg.ReplayRoot), "build-plans", configDigest)
	if _, err := os.Stat(filepath.Join(root, lmeReplayIndexFile)); err == nil {
		plan, loadErr := loadLMEBuildPlan(root)
		if loadErr != nil {
			return nil, "", loadErr
		}
		if plan.Index.ConfigDigest != configDigest || plan.Index.Config != config {
			return nil, "", fmt.Errorf("existing build plan config mismatch")
		}
		if err := verifyLMEBuildPlanSource(plan, replay); err != nil {
			return nil, "", err
		}
		return plan, root, nil
	} else if !os.IsNotExist(err) {
		return nil, "", fmt.Errorf("stat build plan index: %w", err)
	}
	planner, err := newLMEBuildPlanner(config, tokenizer.tokenize)
	if err != nil {
		return nil, "", err
	}
	plan, err := newLMEBuildPlanCorpus(planner, replay, configDigest)
	if err != nil {
		return nil, "", err
	}
	if err := writeLMEBuildPlan(root, plan); err != nil {
		return nil, "", err
	}
	loaded, err := loadLMEBuildPlan(root)
	if err != nil {
		return nil, "", err
	}
	if err := verifyLMEBuildPlanSource(loaded, replay); err != nil {
		return nil, "", err
	}
	return loaded, root, nil
}

func newLMEBuildPlanCorpus(
	planner *lmeBuildPlanner,
	replay *lmeReplayCorpus,
	configDigest string,
) (*lmeBuildPlanCorpus, error) {
	corpus := &lmeBuildPlanCorpus{
		Index: &lmeBuildPlanIndex{
			Version:      lmeBuildPlanVersion,
			Protocol:     lmeBuildProtocol,
			Config:       planner.config,
			ConfigDigest: configDigest,
			Cases:        make([]lmeArtifactEntry, 0, len(replay.Index.Cases)),
		},
		Cases: make(map[string]*lmeBuildCasePlan, len(replay.Index.Cases)),
	}
	for _, replayEntry := range replay.Index.Cases {
		replayCase := replay.Cases[replayEntry.CaseID]
		casePlan, err := planner.buildCase(replayCase, configDigest)
		if err != nil {
			return nil, fmt.Errorf("build plan for case %s: %w", replayEntry.CaseID, err)
		}
		data, err := lmeMarshalArtifact(casePlan)
		if err != nil {
			return nil, fmt.Errorf("marshal build plan case %s: %w", replayEntry.CaseID, err)
		}
		corpus.Index.Cases = append(corpus.Index.Cases, lmeArtifactEntry{
			CaseID: replayEntry.CaseID,
			File:   lmeCaseArtifactFile(replayEntry.CaseID),
			Digest: lmeDigestBytes(data),
		})
		corpus.Cases[replayEntry.CaseID] = casePlan
		addLMEBuildStats(&corpus.Index.Stats, casePlan.Stats)
	}
	digest, err := lmeBuildPlanIndexDigest(corpus.Index)
	if err != nil {
		return nil, err
	}
	corpus.Index.BuildPlanDigest = digest
	return corpus, nil
}

func writeLMEBuildPlan(root string, corpus *lmeBuildPlanCorpus) error {
	if corpus == nil || corpus.Index == nil {
		return fmt.Errorf("nil LongMemEval build plan")
	}
	if err := os.MkdirAll(filepath.Join(root, "cases"), 0755); err != nil {
		return fmt.Errorf("create build plan cases directory: %w", err)
	}
	for _, entry := range corpus.Index.Cases {
		casePlan := corpus.Cases[entry.CaseID]
		if casePlan == nil {
			return fmt.Errorf("build plan case %s missing before write", entry.CaseID)
		}
		if err := lmeWriteImmutableJSON(filepath.Join(root, filepath.FromSlash(entry.File)), casePlan); err != nil {
			return err
		}
	}
	return lmeWriteImmutableJSON(filepath.Join(root, lmeReplayIndexFile), corpus.Index)
}

func loadLMEBuildPlan(root string) (*lmeBuildPlanCorpus, error) {
	index, err := loadLMEBuildPlanIndex(root)
	if err != nil {
		return nil, err
	}
	cases, actualStats, err := loadLMEBuildPlanCases(root, index)
	if err != nil {
		return nil, err
	}
	if actualStats != index.Stats {
		return nil, fmt.Errorf("build plan aggregate statistics mismatch")
	}
	return &lmeBuildPlanCorpus{Index: index, Cases: cases}, nil
}

func loadLMEBuildPlanIndex(root string) (*lmeBuildPlanIndex, error) {
	var index lmeBuildPlanIndex
	if err := lmeReadStrictJSON(filepath.Join(root, lmeReplayIndexFile), &index); err != nil {
		return nil, fmt.Errorf("read build plan index: %w", err)
	}
	if index.Version != lmeBuildPlanVersion {
		return nil, fmt.Errorf("build plan version %d does not match %d", index.Version, lmeBuildPlanVersion)
	}
	if index.Protocol != lmeBuildProtocol {
		return nil, fmt.Errorf("unsupported LongMemEval build protocol %q", index.Protocol)
	}
	if err := validateLMEBuildPlanConfig(index.Config); err != nil {
		return nil, fmt.Errorf("validate build plan configuration: %w", err)
	}
	configDigest, err := lmeJSONDigest(index.Config)
	if err != nil {
		return nil, err
	}
	if configDigest != index.ConfigDigest {
		return nil, fmt.Errorf("build plan config digest mismatch: got %s want %s", configDigest, index.ConfigDigest)
	}
	digest, err := lmeBuildPlanIndexDigest(&index)
	if err != nil {
		return nil, err
	}
	if digest != index.BuildPlanDigest {
		return nil, fmt.Errorf("build plan digest mismatch: got %s want %s", digest, index.BuildPlanDigest)
	}
	return &index, nil
}

func loadLMEBuildPlanCases(
	root string,
	index *lmeBuildPlanIndex,
) (map[string]*lmeBuildCasePlan, lmeBuildStats, error) {
	cases := make(map[string]*lmeBuildCasePlan, len(index.Cases))
	var actualStats lmeBuildStats
	seen := make(map[string]struct{}, len(index.Cases))
	for _, entry := range index.Cases {
		if _, ok := seen[entry.CaseID]; ok {
			return nil, lmeBuildStats{}, fmt.Errorf("build plan contains duplicate case %s", entry.CaseID)
		}
		seen[entry.CaseID] = struct{}{}
		casePlan, err := loadLMEBuildPlanCaseArtifact(root, index, entry)
		if err != nil {
			return nil, lmeBuildStats{}, err
		}
		cases[entry.CaseID] = casePlan
		addLMEBuildStats(&actualStats, casePlan.Stats)
	}
	return cases, actualStats, nil
}

func loadLMEBuildPlanCaseArtifact(
	root string,
	index *lmeBuildPlanIndex,
	entry lmeArtifactEntry,
) (*lmeBuildCasePlan, error) {
	path, err := lmeArtifactPath(root, entry.File)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read build plan case %s: %w", entry.CaseID, err)
	}
	if got := lmeDigestBytes(data); got != entry.Digest {
		return nil, fmt.Errorf(
			"build plan case %s digest mismatch: got %s want %s",
			entry.CaseID,
			got,
			entry.Digest,
		)
	}
	var casePlan lmeBuildCasePlan
	if err := lmeDecodeStrict(data, &casePlan); err != nil {
		return nil, fmt.Errorf("decode build plan case %s: %w", entry.CaseID, err)
	}
	if casePlan.Version != lmeBuildPlanVersion || casePlan.CaseID != entry.CaseID ||
		casePlan.ReplayDigest != index.Config.ReplayDigest || casePlan.ConfigDigest != index.ConfigDigest {
		return nil, fmt.Errorf("build plan case %s identity or source mismatch", entry.CaseID)
	}
	if entry.File != lmeCaseArtifactFile(entry.CaseID) {
		return nil, fmt.Errorf("build plan case %s has non-canonical artifact path", entry.CaseID)
	}
	if err := validateLMEBuildCasePlan(&casePlan, index.Config.MaxTokens); err != nil {
		return nil, fmt.Errorf("validate build plan case %s: %w", entry.CaseID, err)
	}
	return &casePlan, nil
}

func validateLMEBuildCasePlan(plan *lmeBuildCasePlan, maxTokens int) error {
	if maxTokens <= 0 {
		return fmt.Errorf("build max tokens must be positive: %d", maxTokens)
	}
	var actualStats lmeBuildStats
	actualStats.CaseCount = 1
	seenSessionIDs := make(map[string]struct{}, len(plan.Sessions))
	for sessionIndex, sessionPlan := range plan.Sessions {
		if sessionPlan.SessionIndex != sessionIndex {
			return fmt.Errorf(
				"session %s index is %d, want %d",
				sessionPlan.SessionID,
				sessionPlan.SessionIndex,
				sessionIndex,
			)
		}
		if sessionPlan.SessionID == "" {
			return fmt.Errorf("session %d has incomplete identity", sessionIndex)
		}
		if _, ok := seenSessionIDs[sessionPlan.SessionID]; ok {
			return fmt.Errorf("duplicate build session ID %q", sessionPlan.SessionID)
		}
		seenSessionIDs[sessionPlan.SessionID] = struct{}{}
		if _, err := time.Parse(time.RFC3339Nano, sessionPlan.ObservationTime); err != nil {
			return fmt.Errorf("session %s observation time: %w", sessionPlan.SessionID, err)
		}
		for _, pair := range sessionPlan.Pairs {
			if err := validateLMEBuildPair(pair, maxTokens); err != nil {
				return fmt.Errorf("pair %s: %w", pair.PairID, err)
			}
		}
		accumulateLMEBuildSessionStats(&actualStats, sessionPlan)
	}
	if actualStats != plan.Stats {
		return fmt.Errorf("case build statistics mismatch")
	}
	return nil
}

func validateLMEBuildPair(pair lmeBuildPairPlan, maxTokens int) error {
	if pair.PairID == "" || len(pair.Chunks) == 0 {
		return fmt.Errorf("pair identity or chunks are missing")
	}
	sourceIDs, err := validateLMEBuildPairSources(pair.SourceTurnIDs)
	if err != nil {
		return err
	}
	if pair.OriginalTokens < 0 || pair.FinalTokens < 0 ||
		pair.OriginalBytes < 0 || pair.FinalBytes < 0 {
		return fmt.Errorf("pair contains negative accounting")
	}
	if pair.OriginalBytes != pair.FinalBytes {
		return fmt.Errorf("pair is not lossless")
	}
	var finalTokens int
	var finalBytes int
	for chunkIndex, chunk := range pair.Chunks {
		chunkTokens, chunkBytes, err := validateLMEBuildChunk(
			chunk,
			chunkIndex,
			maxTokens,
			sourceIDs,
		)
		if err != nil {
			return err
		}
		finalBytes += chunkBytes
		finalTokens += chunkTokens
	}
	if finalBytes != pair.FinalBytes || finalTokens != pair.FinalTokens {
		return fmt.Errorf("pair chunk accounting mismatch")
	}
	return nil
}

func validateLMEBuildPairSources(sourceTurnIDs []string) (map[string]struct{}, error) {
	if len(sourceTurnIDs) == 0 || len(sourceTurnIDs) > 2 {
		return nil, fmt.Errorf(
			"turn pair contains %d source turns, want one or two",
			len(sourceTurnIDs),
		)
	}
	sourceIDs := make(map[string]struct{}, len(sourceTurnIDs))
	for _, sourceTurnID := range sourceTurnIDs {
		if sourceTurnID == "" {
			return nil, fmt.Errorf("turn pair contains an empty source turn ID")
		}
		if _, exists := sourceIDs[sourceTurnID]; exists {
			return nil, fmt.Errorf("turn pair repeats source turn %s", sourceTurnID)
		}
		sourceIDs[sourceTurnID] = struct{}{}
	}
	return sourceIDs, nil
}

func validateLMEBuildChunk(
	chunk lmeBuildChunkPlan,
	chunkIndex int,
	maxTokens int,
	sourceIDs map[string]struct{},
) (int, int, error) {
	if chunk.ChunkID == "" || chunk.Index != chunkIndex {
		return 0, 0, fmt.Errorf("chunk %d identity or index mismatch", chunkIndex)
	}
	if chunk.TokenCount < 0 || chunk.TokenCount > maxTokens {
		return 0, 0, fmt.Errorf(
			"chunk %d token count %d exceeds limit %d",
			chunkIndex,
			chunk.TokenCount,
			maxTokens,
		)
	}
	var chunkTokens int
	var chunkBytes int
	for _, part := range chunk.Turns {
		partTokens, partBytes, err := validateLMEBuildTurnPart(part, chunkIndex, sourceIDs)
		if err != nil {
			return 0, 0, err
		}
		chunkBytes += partBytes
		chunkTokens += partTokens
	}
	if chunkBytes != chunk.ByteCount || chunkTokens != chunk.TokenCount {
		return 0, 0, fmt.Errorf("chunk %d accounting mismatch", chunkIndex)
	}
	return chunkTokens, chunkBytes, nil
}

func validateLMEBuildTurnPart(
	part lmeBuildTurnPart,
	chunkIndex int,
	sourceIDs map[string]struct{},
) (int, int, error) {
	if part.SourceTurnID == "" || (part.Role != "user" && part.Role != "assistant") {
		return 0, 0, fmt.Errorf("chunk %d contains an invalid turn part", chunkIndex)
	}
	if _, ok := sourceIDs[part.SourceTurnID]; !ok {
		return 0, 0, fmt.Errorf(
			"chunk %d references source turn %s outside its turn pair",
			chunkIndex,
			part.SourceTurnID,
		)
	}
	if part.SourceTurnIndex < 0 {
		return 0, 0, fmt.Errorf("chunk %d contains a negative source turn index", chunkIndex)
	}
	if part.StartByte < 0 || part.EndByte < part.StartByte ||
		part.EndByte-part.StartByte != len(part.Content) {
		return 0, 0, fmt.Errorf("chunk %d contains invalid byte boundaries", chunkIndex)
	}
	if part.StartToken < 0 || part.EndToken < part.StartToken {
		return 0, 0, fmt.Errorf("chunk %d contains invalid token boundaries", chunkIndex)
	}
	return part.EndToken - part.StartToken, len(part.Content), nil
}

func verifyLMEBuildPlanSource(plan *lmeBuildPlanCorpus, replay *lmeReplayCorpus) error {
	if plan == nil || plan.Index == nil || replay == nil || replay.Index == nil {
		return fmt.Errorf("nil build plan or replay source")
	}
	if plan.Index.Config.ReplayDigest != replay.Index.ReplayDigest {
		return fmt.Errorf("build plan replay digest mismatch")
	}
	if len(plan.Index.Cases) != len(replay.Index.Cases) {
		return fmt.Errorf("build plan and replay case counts differ")
	}
	for i, replayEntry := range replay.Index.Cases {
		planEntry := plan.Index.Cases[i]
		if planEntry.CaseID != replayEntry.CaseID {
			return fmt.Errorf("build plan and replay case order differ at index %d", i)
		}
		if err := verifyLMEBuildCaseSource(
			plan.Cases[planEntry.CaseID],
			replay.Cases[replayEntry.CaseID],
		); err != nil {
			return fmt.Errorf("verify build plan case %s: %w", planEntry.CaseID, err)
		}
	}
	return nil
}

func verifyLMEBuildCaseSource(plan *lmeBuildCasePlan, replay *lmeReplayCase) error {
	if plan == nil || replay == nil || plan.CaseID != replay.CaseID {
		return fmt.Errorf("case identity mismatch")
	}
	if len(plan.Sessions) != len(replay.Sessions) {
		return fmt.Errorf("session count mismatch")
	}
	for sessionIndex, replaySession := range replay.Sessions {
		planSession := plan.Sessions[sessionIndex]
		if err := verifyLMEBuildSessionSource(sessionIndex, planSession, replaySession); err != nil {
			return err
		}
	}
	return nil
}

func verifyLMEBuildSessionSource(
	sessionIndex int,
	plan lmeBuildSessionPlan,
	replay lmeReplaySession,
) error {
	if plan.SessionID != replay.SessionID || plan.ObservationTime != replay.ObservationTime {
		return fmt.Errorf("session %d identity mismatch", sessionIndex)
	}
	expectedGroups, err := lmeTurnPairGroups(replay.Turns)
	if err != nil {
		return fmt.Errorf("group replay session %s turns: %w", replay.SessionID, err)
	}
	if len(plan.Pairs) != len(expectedGroups) {
		return fmt.Errorf(
			"session %s has %d build pairs, want %d turn pairs",
			replay.SessionID,
			len(plan.Pairs),
			len(expectedGroups),
		)
	}
	for pairIndex, expectedTurns := range expectedGroups {
		pair := plan.Pairs[pairIndex]
		if len(pair.SourceTurnIDs) != len(expectedTurns) {
			return fmt.Errorf("turn-pair boundary mismatch at pair %d", pairIndex)
		}
		for turnIndex, expectedTurn := range expectedTurns {
			if pair.SourceTurnIDs[turnIndex] != expectedTurn.TurnID {
				return fmt.Errorf("turn-pair boundary mismatch at pair %d", pairIndex)
			}
		}
	}
	return verifyLMEBuildSessionContent(plan, replay)
}

func verifyLMEBuildSessionContent(plan lmeBuildSessionPlan, replay lmeReplaySession) error {
	contents := make(map[string]*strings.Builder, len(replay.Turns))
	turnsByID := make(map[string]lmeReplayTurn, len(replay.Turns))
	tokenOffsets := make(map[string]int, len(replay.Turns))
	for _, turn := range replay.Turns {
		contents[turn.TurnID] = &strings.Builder{}
		turnsByID[turn.TurnID] = turn
	}
	var sourceTurnIDs []string
	for _, pair := range plan.Pairs {
		sourceTurnIDs = append(sourceTurnIDs, pair.SourceTurnIDs...)
		for _, chunk := range pair.Chunks {
			for _, part := range chunk.Turns {
				builder := contents[part.SourceTurnID]
				if builder == nil {
					return fmt.Errorf("unknown source turn %s", part.SourceTurnID)
				}
				turn := turnsByID[part.SourceTurnID]
				if part.SourceTurnIndex != turn.TurnIndex || part.Role != turn.Role {
					return fmt.Errorf("source turn %s identity mismatch", part.SourceTurnID)
				}
				if part.StartByte != builder.Len() || part.EndByte != builder.Len()+len(part.Content) {
					return fmt.Errorf("source turn %s byte offsets are not contiguous", part.SourceTurnID)
				}
				if part.StartToken != tokenOffsets[part.SourceTurnID] {
					return fmt.Errorf("source turn %s token offsets are not contiguous", part.SourceTurnID)
				}
				tokenOffsets[part.SourceTurnID] = part.EndToken
				builder.WriteString(part.Content)
			}
		}
	}
	if len(sourceTurnIDs) != len(replay.Turns) {
		return fmt.Errorf("source turn count mismatch")
	}
	for turnIndex, turn := range replay.Turns {
		if sourceTurnIDs[turnIndex] != turn.TurnID {
			return fmt.Errorf("source turn order mismatch at index %d", turnIndex)
		}
		if contents[turn.TurnID].String() != turn.Content {
			return fmt.Errorf("source turn %s is not lossless", turn.TurnID)
		}
	}
	return nil
}

func loadLMEBuildCasePlan(cfg lmeRunConfig, caseID string) (*lmeBuildCasePlan, error) {
	plan, err := loadLMEBuildPlan(cfg.BuildPlanRoot)
	if err != nil {
		return nil, err
	}
	if plan.Index.BuildPlanDigest != cfg.BuildPlanDigest ||
		plan.Index.Config.ReplayDigest != cfg.ReplayDigest ||
		plan.Index.Protocol != lmeBuildProtocol {
		return nil, fmt.Errorf("build plan does not match current run configuration")
	}
	casePlan := plan.Cases[caseID]
	if casePlan == nil {
		return nil, fmt.Errorf("build plan case %s is not present in immutable index", caseID)
	}
	return casePlan, nil
}

func lmeBuildPlanIndexDigest(index *lmeBuildPlanIndex) (string, error) {
	copyIndex := *index
	copyIndex.BuildPlanDigest = ""
	return lmeJSONDigest(copyIndex)
}

func lmeJSONDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return lmeDigestBytes(data), nil
}
