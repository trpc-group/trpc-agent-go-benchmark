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
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

func prepareResume(
	output, predictionsPath string,
	selected []contract.Case,
	redoExisting bool,
	policy string,
	identity runIdentity,
) (resumeState, error) {
	state := resumeState{
		Predictions: map[string]contract.Prediction{},
		Skipped:     map[string]sweagent.CaseResult{},
	}
	if policy != resumePolicyUpstream && policy != resumePolicyRetryable {
		return state, fmt.Errorf("unknown resume policy %q", policy)
	}
	if err := validateRunIdentity(identity); err != nil {
		return state, err
	}
	selectedIDs := make(map[string]struct{}, len(selected))
	selectedIDList := make([]string, 0, len(selected))
	for _, c := range selected {
		if err := validateArtifactName("selected instance id", c.InstanceID); err != nil {
			return state, err
		}
		if _, ok := selectedIDs[c.InstanceID]; ok {
			return state, fmt.Errorf("duplicate selected instance id %q", c.InstanceID)
		}
		selectedIDs[c.InstanceID] = struct{}{}
		selectedIDList = append(selectedIDList, c.InstanceID)
	}
	selectedHash, err := selectedInstancesSHA256(selectedIDList)
	if err != nil {
		return state, err
	}
	if selectedHash != identity.SelectedInstancesSHA256 {
		return state, fmt.Errorf("selected instance hash %q does not match run identity %q", selectedHash, identity.SelectedInstancesSHA256)
	}
	existing, err := artifact.ReadPredictions(predictionsPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return state, err
	}
	for id, prediction := range existing {
		if err := validateArtifactName("prediction instance id", id); err != nil {
			return state, err
		}
		if _, ok := selectedIDs[id]; !ok {
			return state, fmt.Errorf("existing prediction %q is not part of the current selected instance set", id)
		}
		state.Predictions[id] = prediction
	}
	for _, c := range selected {
		prediction, found := state.Predictions[c.InstanceID]
		if !found {
			state.Pending = append(state.Pending, c)
			continue
		}
		var result sweagent.CaseResult
		tracePath := filepath.Join(output, c.InstanceID, c.InstanceID+".traj.json")
		if err := artifact.ReadJSONFile(tracePath, &result); err != nil {
			return state, fmt.Errorf("cannot validate provenance for existing prediction %s: %w", c.InstanceID, err)
		}
		if err := validateResumeResult(c.InstanceID, result, identity); err != nil {
			return state, err
		}
		if redoExisting {
			delete(state.Predictions, c.InstanceID)
			state.Pending = append(state.Pending, c)
			continue
		}
		if policy == resumePolicyRetryable && (result.Info.ExitStatus == "" || result.Info.Retryable) {
			delete(state.Predictions, c.InstanceID)
			state.Pending = append(state.Pending, c)
			continue
		}
		if result.Info.ExitStatus == "" {
			// The upstream policy treats key presence in preds.json as the
			// completion signal. This runner first requires matching per-case
			// provenance, then preserves that key-presence behavior.
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

func validateRunIdentity(identity runIdentity) error {
	if err := validateArtifactName("run id", identity.RunID); err != nil {
		return err
	}
	required := []struct {
		name  string
		value string
	}{
		{"observation codec", identity.ObservationCodec},
		{"binary hash", identity.BinarySHA256},
		{"model config hash", identity.ModelConfigHash},
		{"environment config hash", identity.EnvironmentConfigSHA256},
		{"cases hash", identity.CasesHash},
		{"command timeout", identity.CommandTimeout},
		{"case timeout", identity.CaseTimeout},
		{"selected instances hash", identity.SelectedInstancesSHA256},
	}
	for _, field := range required {
		if strings.TrimSpace(field.value) == "" {
			return fmt.Errorf("run identity has empty %s", field.name)
		}
	}
	return nil
}

func validateResumeResult(instanceID string, result sweagent.CaseResult, expected runIdentity) error {
	if result.InstanceID != instanceID {
		return fmt.Errorf("existing prediction %s has trajectory instance_id %q", instanceID, result.InstanceID)
	}
	info := result.Info
	actualCodec := info.ObservationCodec
	if actualCodec == "" {
		actualCodec = string(sweagent.ObservationCodecXML)
	}
	checks := []struct {
		name     string
		actual   string
		expected string
	}{
		{"run id", info.RunID, expected.RunID},
		{"observation codec", actualCodec, expected.ObservationCodec},
		{"source revision", info.SourceRevision, expected.SourceRevision},
		{"binary hash", info.BinarySHA256, expected.BinarySHA256},
		{"model config hash", info.ModelConfigHash, expected.ModelConfigHash},
		{"environment config hash", info.EnvironmentConfigSHA256, expected.EnvironmentConfigSHA256},
		{"cases hash", info.CasesHash, expected.CasesHash},
		{"command timeout", info.CommandTimeout, expected.CommandTimeout},
		{"case timeout", info.CaseTimeout, expected.CaseTimeout},
		{"selected instances hash", info.SelectedInstancesSHA256, expected.SelectedInstancesSHA256},
	}
	for _, check := range checks {
		if check.expected != "" && check.actual != check.expected {
			return fmt.Errorf("existing prediction %s has %s %q, want %q", instanceID, check.name, check.actual, check.expected)
		}
	}
	if expected.SourceModified != info.SourceModified {
		return fmt.Errorf("existing prediction %s has source_modified=%t, want %t", instanceID, info.SourceModified, expected.SourceModified)
	}
	return nil
}
