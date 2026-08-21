//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package dataset

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
)

// EventExtensionLongMemEvalTurnID stores the stable benchmark artifact ID for
// a LongMemEval turn on seeded session events.
const EventExtensionLongMemEvalTurnID = "longmemeval.turn_id"

// LongMemEvalTurn represents one turn in a LongMemEval session.
type LongMemEvalTurn struct {
	Role      string `json:"role"`
	Content   string `json:"content"`
	HasAnswer bool   `json:"has_answer,omitempty"`
}

// LongMemEvalInstance represents one official LongMemEval question instance.
type LongMemEvalInstance struct {
	QuestionID         string              `json:"question_id"`
	QuestionType       string              `json:"question_type"`
	Question           string              `json:"question"`
	QuestionDate       string              `json:"question_date"`
	Answer             string              `json:"-"`
	RawAnswer          json.RawMessage     `json:"answer"`
	AnswerSessionIDs   []string            `json:"answer_session_ids"`
	HaystackDates      []string            `json:"haystack_dates"`
	HaystackSessionIDs []string            `json:"haystack_session_ids"`
	HaystackSessions   [][]LongMemEvalTurn `json:"haystack_sessions"`
}

// IsAbstention reports whether this instance is an abstention question.
func (inst *LongMemEvalInstance) IsAbstention() bool {
	if inst == nil {
		return false
	}
	return strings.Contains(inst.QuestionID, "_abs")
}

// TotalTurns returns the number of turns across all haystack sessions.
func (inst *LongMemEvalInstance) TotalTurns() int {
	total := 0
	if inst == nil {
		return total
	}
	for _, sess := range inst.HaystackSessions {
		total += len(sess)
	}
	return total
}

// LoadLongMemEval loads and validates LongMemEval instances from a JSON file.
func LoadLongMemEval(path string) ([]*LongMemEvalInstance, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read LongMemEval file %s: %w", path, err)
	}
	var instances []*LongMemEvalInstance
	if err := json.Unmarshal(data, &instances); err != nil {
		return nil, fmt.Errorf("parse LongMemEval JSON: %w", err)
	}
	for i, inst := range instances {
		if err := normalizeLongMemEvalInstance(inst); err != nil {
			return nil, fmt.Errorf("instance %d: %w", i, err)
		}
	}
	return instances, nil
}

// FilterLongMemEval filters instances by question type and keeps source order.
func FilterLongMemEval(
	instances []*LongMemEvalInstance,
	questionTypes []string,
) []*LongMemEvalInstance {
	if len(questionTypes) == 0 {
		return instances
	}
	typeSet := make(map[string]struct{}, len(questionTypes))
	for _, qt := range questionTypes {
		qt = strings.TrimSpace(strings.ToLower(qt))
		if qt != "" {
			typeSet[qt] = struct{}{}
		}
	}
	filtered := make([]*LongMemEvalInstance, 0, len(instances))
	for _, inst := range instances {
		qt := strings.ToLower(strings.TrimSpace(inst.QuestionType))
		if _, ok := typeSet[qt]; ok {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}

// LongMemEvalQuestionTypes returns sorted question types present in instances.
func LongMemEvalQuestionTypes(instances []*LongMemEvalInstance) []string {
	seen := make(map[string]struct{})
	for _, inst := range instances {
		if inst == nil || inst.QuestionType == "" {
			continue
		}
		seen[inst.QuestionType] = struct{}{}
	}
	types := make([]string, 0, len(seen))
	for t := range seen {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}

func normalizeLongMemEvalInstance(inst *LongMemEvalInstance) error {
	if inst == nil {
		return fmt.Errorf("nil instance")
	}
	if strings.TrimSpace(inst.QuestionID) == "" {
		return fmt.Errorf("question_id is required")
	}
	if strings.TrimSpace(inst.QuestionType) == "" {
		return fmt.Errorf("question_type is required for %s", inst.QuestionID)
	}
	if strings.TrimSpace(inst.Question) == "" {
		return fmt.Errorf("question is required for %s", inst.QuestionID)
	}
	answer, err := decodeLongMemEvalAnswer(inst.RawAnswer)
	if err != nil {
		return fmt.Errorf("decode answer for %s: %w", inst.QuestionID, err)
	}
	inst.Answer = answer
	if len(inst.HaystackSessions) == 0 {
		return fmt.Errorf("haystack_sessions is empty for %s", inst.QuestionID)
	}
	if len(inst.HaystackSessions) != len(inst.HaystackSessionIDs) {
		return fmt.Errorf("haystack session count mismatch for %s", inst.QuestionID)
	}
	if len(inst.HaystackSessions) != len(inst.HaystackDates) {
		return fmt.Errorf("haystack date count mismatch for %s", inst.QuestionID)
	}
	for sessIdx, sess := range inst.HaystackSessions {
		if strings.TrimSpace(inst.HaystackSessionIDs[sessIdx]) == "" {
			return fmt.Errorf("empty haystack_session_ids[%d] for %s", sessIdx, inst.QuestionID)
		}
		if len(sess) == 0 {
			return fmt.Errorf("empty haystack session %d for %s", sessIdx, inst.QuestionID)
		}
		for turnIdx, turn := range sess {
			if turn.Role != "user" && turn.Role != "assistant" {
				return fmt.Errorf(
					"invalid role %q at session %d turn %d for %s",
					turn.Role,
					sessIdx,
					turnIdx,
					inst.QuestionID,
				)
			}
		}
	}
	if !inst.IsAbstention() && len(inst.AnswerSessionIDs) == 0 {
		return fmt.Errorf("answer_session_ids is empty for %s", inst.QuestionID)
	}
	return nil
}

func decodeLongMemEvalAnswer(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", fmt.Errorf("missing answer")
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s, nil
	}
	var i int64
	if err := json.Unmarshal(raw, &i); err == nil {
		return strconv.FormatInt(i, 10), nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
	return "", fmt.Errorf("unsupported answer JSON: %s", strings.TrimSpace(string(raw)))
}
