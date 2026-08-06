//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tagagent

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"

	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
	"trpc.group/trpc-go/trpc-agent-go/model"
)

// RetrievalTraceDocument is the portable identity of one returned code
// search document. Source text is represented by a digest, while the raw
// local result remains available separately for an authorized offline replay.
type RetrievalTraceDocument struct {
	ID            string  `json:"id,omitempty"`
	Path          string  `json:"path,omitempty"`
	Lines         string  `json:"lines,omitempty"`
	Symbol        string  `json:"symbol,omitempty"`
	Score         float64 `json:"score"`
	ContentSHA256 string  `json:"content_sha256"`
}

// RetrievalTraceEntry binds one model query to the exact raw and rendered
// retrieval result produced by the frozen task-start index.
type RetrievalTraceEntry struct {
	Call              int                      `json:"call"`
	ToolCallID        string                   `json:"tool_call_id"`
	Query             string                   `json:"query"`
	Status            string                   `json:"status"`
	Error             string                   `json:"error,omitempty"`
	ErrorSHA256       string                   `json:"error_sha256,omitempty"`
	ArgumentsSHA256   string                   `json:"arguments_sha256"`
	ResultSHA256      string                   `json:"result_sha256"`
	ObservationSHA256 string                   `json:"observation_sha256,omitempty"`
	ResultBytes       int                      `json:"result_bytes"`
	ObservationBytes  int                      `json:"observation_bytes,omitempty"`
	Documents         []RetrievalTraceDocument `json:"documents"`
}

// State captures one case's terminal value and model accounting.
type State struct {
	mu                          sync.Mutex
	submission                  string
	submitted                   bool
	llmCalls                    int
	toolCalls                   int
	toolLoopWarningCount        int
	firstToolLoopWarningLLMCall int
	toolLoopWarningLLMCalls     []int
	codeSearchCalls             int
	codeSearchErrors            int
	codeSearchResultBytes       int
	codeSearchObservationBytes  int
	codeSearchRawResults        []json.RawMessage
	retrievalTrace              []RetrievalTraceEntry
	usage                       model.Usage
	responses                   []*model.Response
}

func (s *State) recordCodeSearchResult(toolCallID string, arguments []byte, result any) {
	payload, err := json.Marshal(result)
	if err != nil {
		s.recordCodeSearchError(
			toolCallID,
			arguments,
			fmt.Errorf("marshal code_search result: %w", err),
		)
		return
	}
	var request struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(arguments, &request)
	entry := RetrievalTraceEntry{
		ToolCallID:      toolCallID,
		Query:           request.Query,
		Status:          "success",
		ArgumentsSHA256: digestBytes(arguments),
		ResultSHA256:    digestBytes(payload),
		ResultBytes:     len(payload),
		Documents:       []RetrievalTraceDocument{},
	}
	if response, ok := result.(*knowledgetool.KnowledgeSearchResponse); ok && response != nil {
		for _, doc := range response.Documents {
			if doc == nil {
				continue
			}
			entry.Documents = append(entry.Documents, RetrievalTraceDocument{
				ID:            doc.ID,
				Path:          codeSearchPath(doc.Metadata),
				Lines:         codeSearchLines(doc.Metadata),
				Symbol:        codeSearchSymbol(doc.Metadata),
				Score:         doc.Score,
				ContentSHA256: digestBytes([]byte(doc.Text)),
			})
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Call = len(s.retrievalTrace) + 1
	s.codeSearchResultBytes += len(payload)
	s.codeSearchRawResults = append(s.codeSearchRawResults, append(json.RawMessage(nil), payload...))
	s.retrievalTrace = append(s.retrievalTrace, entry)
}

func (s *State) recordCodeSearchError(toolCallID string, arguments []byte, callErr error) {
	message := "code_search failed"
	if callErr != nil && callErr.Error() != "" {
		message = callErr.Error()
	}
	payload, _ := json.Marshal(struct {
		Error string `json:"error"`
	}{Error: message})
	var request struct {
		Query string `json:"query"`
	}
	_ = json.Unmarshal(arguments, &request)
	entry := RetrievalTraceEntry{
		ToolCallID:      toolCallID,
		Query:           request.Query,
		Status:          "error",
		Error:           message,
		ErrorSHA256:     digestBytes([]byte(message)),
		ArgumentsSHA256: digestBytes(arguments),
		ResultSHA256:    digestBytes(payload),
		ResultBytes:     len(payload),
		Documents:       []RetrievalTraceDocument{},
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	entry.Call = len(s.retrievalTrace) + 1
	s.codeSearchErrors++
	s.codeSearchResultBytes += len(payload)
	s.codeSearchRawResults = append(s.codeSearchRawResults, append(json.RawMessage(nil), payload...))
	s.retrievalTrace = append(s.retrievalTrace, entry)
}

func (s *State) recordCodeSearchObservation(toolCallID, observation string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codeSearchObservationBytes += len([]byte(observation))
	for index := len(s.retrievalTrace) - 1; index >= 0; index-- {
		if s.retrievalTrace[index].ToolCallID == toolCallID {
			s.retrievalTrace[index].ObservationBytes = len([]byte(observation))
			s.retrievalTrace[index].ObservationSHA256 = digestBytes([]byte(observation))
			return
		}
	}
}

func (s *State) setSubmission(patch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submission = patch
	s.submitted = true
}

func (s *State) recordModelCall() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmCalls++
	return s.llmCalls
}

func (s *State) recordToolLoopWarning(llmCall int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolLoopWarningCount == 0 {
		s.firstToolLoopWarningLLMCall = llmCall
	}
	s.toolLoopWarningCount++
	s.toolLoopWarningLLMCalls = append(s.toolLoopWarningLLMCalls, llmCall)
}

func (s *State) recordResponse(response *model.Response) {
	if response == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.responses = append(s.responses, response.Clone())
	if response.Usage == nil {
		return
	}
	s.usage.PromptTokens += response.Usage.PromptTokens
	s.usage.CompletionTokens += response.Usage.CompletionTokens
	s.usage.TotalTokens += response.Usage.TotalTokens
	s.usage.PromptTokensDetails.CachedTokens += response.Usage.PromptTokensDetails.CachedTokens
	s.usage.CompletionTokensDetails.ReasoningTokens += response.Usage.CompletionTokensDetails.ReasoningTokens
}

func (s *State) recordToolCall(toolName string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls++
	if toolName == "code_search" {
		s.codeSearchCalls++
	}
}

// Snapshot returns a stable copy for result projection.
func (s *State) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	responses := make([]*model.Response, len(s.responses))
	for i, response := range s.responses {
		responses[i] = response.Clone()
	}
	warningCalls := append([]int(nil), s.toolLoopWarningLLMCalls...)
	rawResults := make([]json.RawMessage, len(s.codeSearchRawResults))
	for i, raw := range s.codeSearchRawResults {
		rawResults[i] = append(json.RawMessage(nil), raw...)
	}
	trace := make([]RetrievalTraceEntry, len(s.retrievalTrace))
	for i, entry := range s.retrievalTrace {
		trace[i] = entry
		trace[i].Documents = append([]RetrievalTraceDocument(nil), entry.Documents...)
	}
	return Snapshot{
		Submission:                  s.submission,
		Submitted:                   s.submitted,
		LLMCalls:                    s.llmCalls,
		ToolCalls:                   s.toolCalls,
		ToolLoopWarningCount:        s.toolLoopWarningCount,
		FirstToolLoopWarningLLMCall: s.firstToolLoopWarningLLMCall,
		ToolLoopWarningLLMCalls:     warningCalls,
		CodeSearchCalls:             s.codeSearchCalls,
		CodeSearchErrors:            s.codeSearchErrors,
		CodeSearchResultBytes:       s.codeSearchResultBytes,
		CodeSearchObservationBytes:  s.codeSearchObservationBytes,
		CodeSearchRawResults:        rawResults,
		RetrievalTrace:              trace,
		Usage:                       s.usage,
		Responses:                   responses,
	}
}

// Snapshot is the immutable case state consumed by the runner.
type Snapshot struct {
	Submission                  string
	Submitted                   bool
	LLMCalls                    int
	ToolCalls                   int
	ToolLoopWarningCount        int
	FirstToolLoopWarningLLMCall int
	ToolLoopWarningLLMCalls     []int
	CodeSearchCalls             int
	CodeSearchErrors            int
	CodeSearchResultBytes       int
	CodeSearchObservationBytes  int
	CodeSearchRawResults        []json.RawMessage
	RetrievalTrace              []RetrievalTraceEntry
	Usage                       model.Usage
	Responses                   []*model.Response
}

func digestBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// DigestBytes returns the canonical SHA-256 digest used by retrieval evidence.
func DigestBytes(value []byte) string {
	return digestBytes(value)
}
