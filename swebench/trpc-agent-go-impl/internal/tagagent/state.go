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
	"encoding/json"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/model"
)

// State captures one case's terminal value and model accounting.
type State struct {
	mu                          sync.Mutex
	submission                  string
	submitted                   bool
	llmCalls                    int
	toolCalls                   int
	toolLoopWarningCount        int
	firstToolLoopWarningLLMCall int
	codeSearchCalls             int
	codeSearchResultBytes       int
	codeSearchObservationBytes  int
	codeSearchRawResults        []json.RawMessage
	usage                       model.Usage
	responses                   []*model.Response
}

func (s *State) recordCodeSearchResult(payload []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codeSearchResultBytes += len(payload)
	s.codeSearchRawResults = append(
		s.codeSearchRawResults,
		append(json.RawMessage(nil), payload...),
	)
}

func (s *State) recordCodeSearchObservationBytes(size int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.codeSearchObservationBytes += size
}

func (s *State) setSubmission(patch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.submission = patch
	s.submitted = true
}

func (s *State) submittedValue() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.submitted
}

func (s *State) recordToolLoopWarning() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.toolLoopWarningCount == 0 {
		s.firstToolLoopWarningLLMCall = s.llmCalls
	}
	s.toolLoopWarningCount++
}

func (s *State) recordModelCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.llmCalls++
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
	rawResults := make([]json.RawMessage, len(s.codeSearchRawResults))
	for i, raw := range s.codeSearchRawResults {
		rawResults[i] = append(json.RawMessage(nil), raw...)
	}
	return Snapshot{
		Submission:                  s.submission,
		Submitted:                   s.submitted,
		LLMCalls:                    s.llmCalls,
		ToolCalls:                   s.toolCalls,
		ToolLoopWarningCount:        s.toolLoopWarningCount,
		FirstToolLoopWarningLLMCall: s.firstToolLoopWarningLLMCall,
		CodeSearchCalls:             s.codeSearchCalls,
		CodeSearchResultBytes:       s.codeSearchResultBytes,
		CodeSearchObservationBytes:  s.codeSearchObservationBytes,
		CodeSearchRawResults:        rawResults,
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
	CodeSearchCalls             int
	CodeSearchResultBytes       int
	CodeSearchObservationBytes  int
	CodeSearchRawResults        []json.RawMessage
	Usage                       model.Usage
	Responses                   []*model.Response
}
