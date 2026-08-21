//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package dataset

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

const longMemEvalDigestPrefix = "sha256:"

type longMemEvalDigestInstance struct {
	QuestionID         string              `json:"question_id"`
	QuestionType       string              `json:"question_type"`
	Question           string              `json:"question"`
	QuestionDate       string              `json:"question_date"`
	Answer             string              `json:"answer"`
	AnswerSessionIDs   []string            `json:"answer_session_ids"`
	HaystackDates      []string            `json:"haystack_dates"`
	HaystackSessionIDs []string            `json:"haystack_session_ids"`
	HaystackSessions   [][]LongMemEvalTurn `json:"haystack_sessions"`
}

// LongMemEvalDatasetDigest returns an order-independent semantic dataset digest.
// Session and turn order within each case remain significant.
func LongMemEvalDatasetDigest(instances []*LongMemEvalInstance) (string, error) {
	canonical := make([]longMemEvalDigestInstance, 0, len(instances))
	seen := make(map[string]struct{}, len(instances))
	for i, inst := range instances {
		if inst == nil {
			return "", fmt.Errorf("LongMemEval dataset instance %d is nil", i)
		}
		if _, ok := seen[inst.QuestionID]; ok {
			return "", fmt.Errorf("LongMemEval dataset contains duplicate question_id %q", inst.QuestionID)
		}
		seen[inst.QuestionID] = struct{}{}
		answer := inst.Answer
		if answer == "" && len(inst.RawAnswer) > 0 {
			decoded, err := decodeLongMemEvalAnswer(inst.RawAnswer)
			if err != nil {
				return "", fmt.Errorf("decode answer for dataset digest case %q: %w", inst.QuestionID, err)
			}
			answer = decoded
		}
		canonical = append(canonical, longMemEvalDigestInstance{
			QuestionID:         inst.QuestionID,
			QuestionType:       inst.QuestionType,
			Question:           inst.Question,
			QuestionDate:       inst.QuestionDate,
			Answer:             answer,
			AnswerSessionIDs:   inst.AnswerSessionIDs,
			HaystackDates:      inst.HaystackDates,
			HaystackSessionIDs: inst.HaystackSessionIDs,
			HaystackSessions:   inst.HaystackSessions,
		})
	}
	sort.Slice(canonical, func(i int, j int) bool {
		return canonical[i].QuestionID < canonical[j].QuestionID
	})
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal LongMemEval dataset digest input: %w", err)
	}
	return longMemEvalSHA256Digest(data), nil
}

// LongMemEvalManifestDigest returns the canonical digest of a manifest while
// excluding the manifest_digest field itself.
func LongMemEvalManifestDigest(manifest *LongMemEvalManifest) (string, error) {
	if manifest == nil {
		return "", fmt.Errorf("LongMemEval manifest is nil")
	}
	canonical := *manifest
	canonical.ManifestDigest = ""
	data, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("marshal LongMemEval manifest digest input: %w", err)
	}
	return longMemEvalSHA256Digest(data), nil
}

func longMemEvalSHA256Digest(data []byte) string {
	digest := sha256.Sum256(data)
	return longMemEvalDigestPrefix + hex.EncodeToString(digest[:])
}

func validLongMemEvalDigest(value string) bool {
	if !strings.HasPrefix(value, longMemEvalDigestPrefix) {
		return false
	}
	raw := strings.TrimPrefix(value, longMemEvalDigestPrefix)
	if len(raw) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(raw)
	return err == nil
}
