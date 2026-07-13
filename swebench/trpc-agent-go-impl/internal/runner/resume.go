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
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/internal/sweagent"
)

type resumeState struct {
	Predictions map[string]contract.Prediction
	Pending     []contract.Case
	Skipped     map[string]sweagent.CaseResult
}

func prepareResume(output, predictionsPath string, selected []contract.Case, redoExisting bool) (resumeState, error) {
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
		if err := artifact.ReadJSONFile(tracePath, &result); err != nil || result.Info.ExitStatus == "" || result.Info.Retryable {
			delete(state.Predictions, c.InstanceID)
			state.Pending = append(state.Pending, c)
			continue
		}
		if prediction.InstanceID == "" {
			prediction.InstanceID = c.InstanceID
			state.Predictions[c.InstanceID] = prediction
		}
		state.Skipped[c.InstanceID] = result
	}
	return state, nil
}
