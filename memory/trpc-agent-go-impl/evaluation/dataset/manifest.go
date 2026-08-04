//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package dataset

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
)

const (
	// LongMemEvalManifestSchemaVersion is the current rich manifest schema.
	LongMemEvalManifestSchemaVersion = 1

	// LongMemEvalManifestSplitDev identifies the development side of a split.
	LongMemEvalManifestSplitDev = "dev"
	// LongMemEvalManifestSplitHoldout identifies the holdout side of a split.
	LongMemEvalManifestSplitHoldout = "holdout"
)

// LongMemEvalManifestMethod identifies how a manifest selected its cases.
type LongMemEvalManifestMethod string

const (
	// LongMemEvalManifestMethodStratifiedSHA256 ranks cases by a seeded SHA-256 hash within each question type.
	LongMemEvalManifestMethodStratifiedSHA256 LongMemEvalManifestMethod = "stratified-sha256"
	// LongMemEvalManifestMethodFullCategory selects every case from each requested question type.
	LongMemEvalManifestMethodFullCategory LongMemEvalManifestMethod = "full-category"
	// LongMemEvalManifestMethodLegacyFirst preserves historical source-order selection.
	LongMemEvalManifestMethodLegacyFirst LongMemEvalManifestMethod = "legacy-first"
)

// LongMemEvalManifestCase binds a selected case ID to its question type.
type LongMemEvalManifestCase struct {
	CaseID       string `json:"case_id"`
	QuestionType string `json:"question_type"`
}

// LongMemEvalManifest stores a fixed LongMemEval case subset.
//
// CaseIDs remains the compatibility field consumed by older benchmark
// versions. The other fields make newly generated manifests reproducible and
// auditable. A legacy manifest containing only case_ids remains valid.
type LongMemEvalManifest struct {
	SchemaVersion int                       `json:"schema_version,omitempty"`
	Method        LongMemEvalManifestMethod `json:"method,omitempty"`
	Seed          string                    `json:"seed,omitempty"`
	Split         string                    `json:"split,omitempty"`
	QuestionTypes []string                  `json:"question_types,omitempty"`
	Quotas        map[string]int            `json:"quotas,omitempty"`
	// RankOffsets records each question type's zero-based start in the seeded ranking.
	RankOffsets   map[string]int            `json:"rank_offsets,omitempty"`
	TotalSize     int                       `json:"total_size,omitempty"`
	CaseIDs       []string                  `json:"case_ids"`
	Cases         []LongMemEvalManifestCase `json:"cases,omitempty"`
	DatasetDigest string                    `json:"dataset_digest,omitempty"`
	// ManifestDigest covers the complete manifest except this field itself.
	ManifestDigest string `json:"manifest_digest,omitempty"`
}

// IsLegacy reports whether the manifest uses the historical case_ids-only schema.
func (m *LongMemEvalManifest) IsLegacy() bool {
	if m == nil {
		return false
	}
	return m.SchemaVersion == 0 && m.Method == "" && m.Seed == "" &&
		m.Split == "" && len(m.QuestionTypes) == 0 && len(m.Quotas) == 0 &&
		len(m.RankOffsets) == 0 && m.TotalSize == 0 && len(m.Cases) == 0 &&
		m.DatasetDigest == "" && m.ManifestDigest == ""
}

// ParseLongMemEvalManifest parses and validates a LongMemEval manifest.
func ParseLongMemEvalManifest(data []byte) (*LongMemEvalManifest, error) {
	var manifest LongMemEvalManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse LongMemEval manifest: %w", err)
	}
	if err := validateLongMemEvalManifestShape(&manifest); err != nil {
		return nil, err
	}
	return &manifest, nil
}

// LoadLongMemEvalManifest loads a fixed LongMemEval case manifest.
func LoadLongMemEvalManifest(path string) (*LongMemEvalManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read LongMemEval manifest %s: %w", path, err)
	}
	manifest, err := ParseLongMemEvalManifest(data)
	if err != nil {
		return nil, fmt.Errorf("load LongMemEval manifest %s: %w", path, err)
	}
	return manifest, nil
}

// WriteLongMemEvalManifest writes a validated LongMemEval manifest as JSON.
func WriteLongMemEvalManifest(path string, manifest *LongMemEvalManifest) error {
	if err := validateLongMemEvalManifestShape(manifest); err != nil {
		return err
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal LongMemEval manifest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create LongMemEval manifest directory: %w", err)
	}
	// #nosec G306 -- Manifests are shareable benchmark artifacts, not private application state.
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write LongMemEval manifest %s: %w", path, err)
	}
	return nil
}

// VerifyLongMemEvalManifest verifies a manifest against its source dataset.
func VerifyLongMemEvalManifest(
	instances []*LongMemEvalInstance,
	manifest *LongMemEvalManifest,
) error {
	if err := validateLongMemEvalManifestShape(manifest); err != nil {
		return err
	}
	inventory, err := newLongMemEvalManifestInventory(instances, !manifest.IsLegacy())
	if err != nil {
		return err
	}
	selectedCounts := make(map[string]int)
	for i, id := range manifest.CaseIDs {
		inst := inventory.byID[id]
		if inst == nil {
			return fmt.Errorf("LongMemEval manifest case_id %q not found in dataset", id)
		}
		if manifest.IsLegacy() {
			continue
		}
		caseType := manifest.Cases[i].QuestionType
		if caseType != inst.QuestionType {
			return fmt.Errorf(
				"LongMemEval manifest case %q has question type %q, dataset has %q",
				id,
				caseType,
				inst.QuestionType,
			)
		}
		selectedCounts[caseType]++
	}
	if manifest.IsLegacy() {
		return nil
	}
	digestInstances := filterLongMemEvalManifestQuestionTypes(instances, manifest.QuestionTypes)
	datasetDigest, err := LongMemEvalDatasetDigest(digestInstances)
	if err != nil {
		return err
	}
	if manifest.DatasetDigest != datasetDigest {
		return fmt.Errorf(
			"LongMemEval dataset digest mismatch: manifest has %s, dataset has %s",
			manifest.DatasetDigest,
			datasetDigest,
		)
	}
	for _, questionType := range manifest.QuestionTypes {
		if selectedCounts[questionType] != manifest.Quotas[questionType] {
			return fmt.Errorf(
				"LongMemEval manifest quota for %q is %d, selected %d",
				questionType,
				manifest.Quotas[questionType],
				selectedCounts[questionType],
			)
		}
	}
	expectedSelection := LongMemEvalManifestSelection{
		Method:        manifest.Method,
		Seed:          manifest.Seed,
		QuestionTypes: manifest.QuestionTypes,
	}
	if manifest.Method != LongMemEvalManifestMethodFullCategory {
		expectedSelection.Quotas = cloneLongMemEvalQuotas(manifest.Quotas)
	}
	expected, err := buildLongMemEvalManifest(
		instances,
		expectedSelection,
		manifest.Split,
		manifest.RankOffsets,
	)
	if err != nil {
		return fmt.Errorf("rebuild LongMemEval manifest selection: %w", err)
	}
	if !reflect.DeepEqual(manifest.CaseIDs, expected.CaseIDs) {
		return errors.New("LongMemEval manifest case order does not match its selection metadata")
	}
	return nil
}

// VerifyLongMemEvalManifestSplit verifies a paired development and holdout split.
func VerifyLongMemEvalManifestSplit(
	instances []*LongMemEvalInstance,
	dev *LongMemEvalManifest,
	holdout *LongMemEvalManifest,
) error {
	if err := validateLongMemEvalManifestShape(dev); err != nil {
		return fmt.Errorf("validate development manifest: %w", err)
	}
	if err := validateLongMemEvalManifestShape(holdout); err != nil {
		return fmt.Errorf("validate holdout manifest: %w", err)
	}
	if dev.IsLegacy() || holdout.IsLegacy() {
		return errors.New("LongMemEval dev/holdout verification requires rich manifests")
	}
	if dev.Split != LongMemEvalManifestSplitDev {
		return fmt.Errorf("development manifest split is %q, want %q", dev.Split, LongMemEvalManifestSplitDev)
	}
	if holdout.Split != LongMemEvalManifestSplitHoldout {
		return fmt.Errorf("holdout manifest split is %q, want %q", holdout.Split, LongMemEvalManifestSplitHoldout)
	}
	if dev.Method != LongMemEvalManifestMethodStratifiedSHA256 || holdout.Method != dev.Method {
		return errors.New("LongMemEval dev/holdout manifests must use stratified-sha256")
	}
	if dev.Seed != holdout.Seed {
		return errors.New("LongMemEval dev/holdout manifests use different seeds")
	}
	if dev.DatasetDigest != holdout.DatasetDigest {
		return errors.New("LongMemEval dev/holdout manifests use different dataset digests")
	}
	if !reflect.DeepEqual(dev.QuestionTypes, holdout.QuestionTypes) {
		return errors.New("LongMemEval dev/holdout manifests use different question types")
	}
	if !reflect.DeepEqual(holdout.RankOffsets, dev.Quotas) {
		return errors.New("LongMemEval holdout rank offsets do not match development quotas")
	}
	seen := make(map[string]struct{}, len(dev.CaseIDs))
	for _, id := range dev.CaseIDs {
		seen[id] = struct{}{}
	}
	for _, id := range holdout.CaseIDs {
		if _, ok := seen[id]; ok {
			return fmt.Errorf("LongMemEval dev/holdout manifests overlap at case_id %q", id)
		}
	}
	if err := VerifyLongMemEvalManifest(instances, dev); err != nil {
		return fmt.Errorf("verify development manifest: %w", err)
	}
	if err := VerifyLongMemEvalManifest(instances, holdout); err != nil {
		return fmt.Errorf("verify holdout manifest: %w", err)
	}
	return nil
}

// FilterLongMemEvalByManifest filters instances by fixed case IDs and preserves
// manifest order.
func FilterLongMemEvalByManifest(
	instances []*LongMemEvalInstance,
	manifest *LongMemEvalManifest,
) ([]*LongMemEvalInstance, error) {
	if manifest == nil {
		return instances, nil
	}
	if err := VerifyLongMemEvalManifest(instances, manifest); err != nil {
		return nil, err
	}
	byID := make(map[string]*LongMemEvalInstance, len(instances))
	for _, inst := range instances {
		if inst != nil {
			byID[inst.QuestionID] = inst
		}
	}
	filtered := make([]*LongMemEvalInstance, 0, len(manifest.CaseIDs))
	for _, id := range manifest.CaseIDs {
		filtered = append(filtered, byID[id])
	}
	return filtered, nil
}

func validateLongMemEvalManifestShape(manifest *LongMemEvalManifest) error {
	if manifest == nil {
		return errors.New("LongMemEval manifest is nil")
	}
	if len(manifest.CaseIDs) == 0 {
		return errors.New("LongMemEval manifest has no case_ids")
	}
	if err := validateLongMemEvalCaseIDs(manifest.CaseIDs); err != nil {
		return err
	}
	if manifest.IsLegacy() {
		return nil
	}
	if manifest.SchemaVersion != LongMemEvalManifestSchemaVersion {
		return fmt.Errorf(
			"unsupported LongMemEval manifest schema_version %d",
			manifest.SchemaVersion,
		)
	}
	if err := validateLongMemEvalManifestMethod(manifest); err != nil {
		return err
	}
	if len(manifest.QuestionTypes) == 0 {
		return errors.New("LongMemEval manifest question_types is required")
	}
	if err := validateLongMemEvalQuestionTypes(manifest.QuestionTypes); err != nil {
		return err
	}
	if manifest.TotalSize != len(manifest.CaseIDs) {
		return fmt.Errorf(
			"LongMemEval manifest total_size is %d, want %d",
			manifest.TotalSize,
			len(manifest.CaseIDs),
		)
	}
	if len(manifest.Cases) != len(manifest.CaseIDs) {
		return fmt.Errorf(
			"LongMemEval manifest has %d cases, want %d",
			len(manifest.Cases),
			len(manifest.CaseIDs),
		)
	}
	if err := validateLongMemEvalManifestQuotas(manifest); err != nil {
		return err
	}
	if err := validateLongMemEvalManifestRankOffsets(manifest); err != nil {
		return err
	}
	for i, manifestCase := range manifest.Cases {
		if manifestCase.CaseID != manifest.CaseIDs[i] {
			return fmt.Errorf(
				"LongMemEval manifest cases[%d].case_id is %q, want %q",
				i,
				manifestCase.CaseID,
				manifest.CaseIDs[i],
			)
		}
		if _, ok := manifest.Quotas[manifestCase.QuestionType]; !ok {
			return fmt.Errorf(
				"LongMemEval manifest case %q has unconfigured question type %q",
				manifestCase.CaseID,
				manifestCase.QuestionType,
			)
		}
	}
	if !validLongMemEvalDigest(manifest.DatasetDigest) {
		return fmt.Errorf("invalid LongMemEval dataset_digest %q", manifest.DatasetDigest)
	}
	if !validLongMemEvalDigest(manifest.ManifestDigest) {
		return fmt.Errorf("invalid LongMemEval manifest_digest %q", manifest.ManifestDigest)
	}
	wantDigest, err := LongMemEvalManifestDigest(manifest)
	if err != nil {
		return err
	}
	if manifest.ManifestDigest != wantDigest {
		return fmt.Errorf(
			"LongMemEval manifest digest mismatch: have %s, want %s",
			manifest.ManifestDigest,
			wantDigest,
		)
	}
	return nil
}

func validateLongMemEvalManifestMethod(manifest *LongMemEvalManifest) error {
	switch manifest.Method {
	case LongMemEvalManifestMethodStratifiedSHA256:
		if err := validateLongMemEvalSeed(manifest.Seed); err != nil {
			return err
		}
		if manifest.Split != "" && manifest.Split != LongMemEvalManifestSplitDev &&
			manifest.Split != LongMemEvalManifestSplitHoldout {
			return fmt.Errorf("invalid LongMemEval manifest split %q", manifest.Split)
		}
	case LongMemEvalManifestMethodFullCategory, LongMemEvalManifestMethodLegacyFirst:
		if manifest.Seed != "" {
			return fmt.Errorf("LongMemEval %s manifest must not set seed", manifest.Method)
		}
		if manifest.Split != "" {
			return fmt.Errorf("LongMemEval %s manifest must not set split", manifest.Method)
		}
	default:
		return fmt.Errorf("unsupported LongMemEval manifest method %q", manifest.Method)
	}
	return nil
}

func validateLongMemEvalManifestRankOffsets(manifest *LongMemEvalManifest) error {
	if manifest.Split != LongMemEvalManifestSplitHoldout {
		if len(manifest.RankOffsets) != 0 {
			return errors.New("LongMemEval rank_offsets are only valid for a holdout manifest")
		}
		return nil
	}
	return validateLongMemEvalHoldoutRankOffsets(manifest.QuestionTypes, manifest.RankOffsets)
}

func validateLongMemEvalHoldoutRankOffsets(questionTypes []string, rankOffsets map[string]int) error {
	if len(rankOffsets) != len(questionTypes) {
		return errors.New("LongMemEval holdout rank_offsets must cover every question type exactly once")
	}
	typeSet := make(map[string]struct{}, len(questionTypes))
	for _, questionType := range questionTypes {
		typeSet[questionType] = struct{}{}
		offset, ok := rankOffsets[questionType]
		if !ok {
			return fmt.Errorf("LongMemEval holdout rank offset missing for %q", questionType)
		}
		if offset < 0 {
			return fmt.Errorf("LongMemEval holdout rank offset for %q is negative", questionType)
		}
	}
	for questionType := range rankOffsets {
		if _, ok := typeSet[questionType]; !ok {
			return fmt.Errorf("LongMemEval holdout rank offset has unknown question type %q", questionType)
		}
	}
	return nil
}

func validateLongMemEvalManifestQuotas(manifest *LongMemEvalManifest) error {
	if len(manifest.Quotas) != len(manifest.QuestionTypes) {
		return errors.New("LongMemEval manifest quotas must cover every question type exactly once")
	}
	typeSet := make(map[string]struct{}, len(manifest.QuestionTypes))
	quotaTotal := 0
	for _, questionType := range manifest.QuestionTypes {
		typeSet[questionType] = struct{}{}
		quota, ok := manifest.Quotas[questionType]
		if !ok {
			return fmt.Errorf("LongMemEval manifest quota missing for %q", questionType)
		}
		if quota < 0 {
			return fmt.Errorf("LongMemEval manifest quota for %q is negative", questionType)
		}
		quotaTotal += quota
	}
	for questionType := range manifest.Quotas {
		if _, ok := typeSet[questionType]; !ok {
			return fmt.Errorf("LongMemEval manifest quota has unknown question type %q", questionType)
		}
	}
	if quotaTotal != manifest.TotalSize {
		return fmt.Errorf(
			"LongMemEval manifest quota total is %d, want %d",
			quotaTotal,
			manifest.TotalSize,
		)
	}
	return nil
}

func validateLongMemEvalCaseIDs(ids []string) error {
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if strings.TrimSpace(id) == "" {
			return errors.New("LongMemEval manifest contains an empty case_id")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("LongMemEval manifest contains duplicate case_id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func validateLongMemEvalQuestionTypes(questionTypes []string) error {
	seen := make(map[string]struct{}, len(questionTypes))
	for _, questionType := range questionTypes {
		if strings.TrimSpace(questionType) == "" {
			return errors.New("LongMemEval manifest contains an empty question type")
		}
		if _, ok := seen[questionType]; ok {
			return fmt.Errorf("LongMemEval manifest contains duplicate question type %q", questionType)
		}
		seen[questionType] = struct{}{}
	}
	return nil
}
