//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"errors"
	"os"
	"path/filepath"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/artifact"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/mini-swe-agent-go-impl/internal/sweagent"
)

type resumeState struct {
	Predictions map[string]contract.Prediction
	Pending     []contract.Case
	Skipped     map[string]sweagent.CaseResult
}

const (
	resumePolicyUpstream  = "upstream"
	resumePolicyRetryable = "retryable"
)

func prepareResume(output, predictionsPath string, selected []contract.Case, redoExisting bool, policy string) (resumeState, error) {
	state := resumeState{
		Predictions: map[string]contract.Prediction{},
		Skipped:     map[string]sweagent.CaseResult{},
	}
	existing, err := artifact.ReadPredictions(predictionsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return state, err
	}
	for id, prediction := range existing {
		state.Predictions[id] = prediction
	}
	for _, c := range selected {
		prediction, found := state.Predictions[c.InstanceID]
		if redoExisting || !found {
			delete(state.Predictions, c.InstanceID)
			state.Pending = append(state.Pending, c)
			continue
		}
		var result sweagent.CaseResult
		tracePath := filepath.Join(output, c.InstanceID, c.InstanceID+".traj.json")
		traceErr := artifact.ReadJSONFile(tracePath, &result)
		if policy == resumePolicyRetryable && (traceErr != nil || result.Info.ExitStatus == "" || result.Info.Retryable) {
			delete(state.Predictions, c.InstanceID)
			state.Pending = append(state.Pending, c)
			continue
		}
		if traceErr != nil || result.Info.ExitStatus == "" {
			// Upstream mini-SWE-agent considers preds.json authoritative and
			// skips every existing key without inspecting its trajectory.
			result.InstanceID = c.InstanceID
			result.Info.ExitStatus = "ExistingPrediction"
		}
		if prediction.InstanceID == "" {
			prediction.InstanceID = c.InstanceID
			state.Predictions[c.InstanceID] = prediction
		}
		state.Skipped[c.InstanceID] = result
	}
	return state, nil
}
