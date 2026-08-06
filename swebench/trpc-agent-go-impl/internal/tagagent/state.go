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
	toolLoopWarningLLMCalls     []int
	usage                       model.Usage
	responses                   []*model.Response
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

func (s *State) recordToolCall() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.toolCalls++
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
	return Snapshot{
		Submission:                  s.submission,
		Submitted:                   s.submitted,
		LLMCalls:                    s.llmCalls,
		ToolCalls:                   s.toolCalls,
		ToolLoopWarningCount:        s.toolLoopWarningCount,
		FirstToolLoopWarningLLMCall: s.firstToolLoopWarningLLMCall,
		ToolLoopWarningLLMCalls:     warningCalls,
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
	Usage                       model.Usage
	Responses                   []*model.Response
}
