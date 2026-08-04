//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package dataset

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

// LongMemEvalManifestSelection configures one manifest selection.
type LongMemEvalManifestSelection struct {
	Method        LongMemEvalManifestMethod
	Seed          string
	QuestionTypes []string
	TotalSize     int
	Quotas        map[string]int
}

// LongMemEvalManifestAllocation configures one side of a dev/holdout split.
// TotalSize and Quotas are mutually exclusive.
type LongMemEvalManifestAllocation struct {
	TotalSize int
	Quotas    map[string]int
}

// LongMemEvalManifestSplitSelection configures a deterministic dev/holdout split.
type LongMemEvalManifestSplitSelection struct {
	Seed          string
	QuestionTypes []string
	Dev           LongMemEvalManifestAllocation
	Holdout       LongMemEvalManifestAllocation
}

// BuildLongMemEvalManifest builds a deterministic LongMemEval manifest.
func BuildLongMemEvalManifest(
	instances []*LongMemEvalInstance,
	selection LongMemEvalManifestSelection,
) (*LongMemEvalManifest, error) {
	return buildLongMemEvalManifest(instances, selection, "", nil)
}

func buildLongMemEvalManifest(
	instances []*LongMemEvalInstance,
	selection LongMemEvalManifestSelection,
	split string,
	rankOffsets map[string]int,
) (*LongMemEvalManifest, error) {
	inventory, err := newLongMemEvalManifestInventory(instances, true)
	if err != nil {
		return nil, err
	}
	questionTypes, err := resolveLongMemEvalQuestionTypes(inventory, selection.QuestionTypes)
	if err != nil {
		return nil, err
	}
	availability := inventory.availability(questionTypes)
	var quotas map[string]int
	switch selection.Method {
	case LongMemEvalManifestMethodFullCategory:
		if selection.Seed != "" || selection.TotalSize != 0 || len(selection.Quotas) != 0 ||
			split != "" || len(rankOffsets) != 0 {
			return nil, errors.New(
				"LongMemEval full-category selection does not accept seed, split, size, quotas, or rank offsets",
			)
		}
		quotas = cloneLongMemEvalQuotas(availability)
	case LongMemEvalManifestMethodStratifiedSHA256:
		if err := validateLongMemEvalSeed(selection.Seed); err != nil {
			return nil, err
		}
		if err := validateLongMemEvalSelectionOffsets(questionTypes, split, rankOffsets); err != nil {
			return nil, err
		}
		quotas, err = resolveLongMemEvalAllocation(
			questionTypes,
			availability,
			LongMemEvalManifestAllocation{
				TotalSize: selection.TotalSize,
				Quotas:    selection.Quotas,
			},
		)
		if err != nil {
			return nil, err
		}
	case LongMemEvalManifestMethodLegacyFirst:
		if selection.Seed != "" || split != "" || len(rankOffsets) != 0 {
			return nil, errors.New("LongMemEval legacy-first selection does not accept seed, split, or rank offsets")
		}
		quotas, err = resolveLongMemEvalAllocation(
			questionTypes,
			availability,
			LongMemEvalManifestAllocation{
				TotalSize: selection.TotalSize,
				Quotas:    selection.Quotas,
			},
		)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unsupported LongMemEval manifest method %q", selection.Method)
	}

	selected, err := selectLongMemEvalManifestCases(
		inventory,
		questionTypes,
		selection.Method,
		selection.Seed,
		quotas,
		rankOffsets,
	)
	if err != nil {
		return nil, err
	}
	return newLongMemEvalManifest(
		instances,
		selection.Method,
		selection.Seed,
		split,
		questionTypes,
		quotas,
		rankOffsets,
		selected,
	)
}

func selectLongMemEvalManifestCases(
	inventory *longMemEvalManifestInventory,
	questionTypes []string,
	method LongMemEvalManifestMethod,
	seed string,
	quotas map[string]int,
	rankOffsets map[string]int,
) (map[string][]*LongMemEvalInstance, error) {
	selected := make(map[string][]*LongMemEvalInstance, len(questionTypes))
	for _, questionType := range questionTypes {
		candidates := append([]*LongMemEvalInstance(nil), inventory.byType[questionType]...)
		switch method {
		case LongMemEvalManifestMethodFullCategory:
			sort.Slice(candidates, func(i int, j int) bool {
				return candidates[i].QuestionID < candidates[j].QuestionID
			})
		case LongMemEvalManifestMethodStratifiedSHA256:
			sortLongMemEvalBySeededRank(candidates, seed)
		case LongMemEvalManifestMethodLegacyFirst:
			// Preserve dataset order only for explicit historical reproduction.
		}
		start := rankOffsets[questionType]
		quota := quotas[questionType]
		if start > len(candidates)-quota {
			return nil, fmt.Errorf(
				"LongMemEval rank offset %d plus quota %d for %q exceeds %d available cases",
				start,
				quota,
				questionType,
				len(candidates),
			)
		}
		selected[questionType] = candidates[start : start+quota]
	}
	return selected, nil
}

// BuildLongMemEvalManifestSplit builds disjoint seeded development and holdout manifests.
func BuildLongMemEvalManifestSplit(
	instances []*LongMemEvalInstance,
	selection LongMemEvalManifestSplitSelection,
) (*LongMemEvalManifest, *LongMemEvalManifest, error) {
	if err := validateLongMemEvalSeed(selection.Seed); err != nil {
		return nil, nil, err
	}
	inventory, err := newLongMemEvalManifestInventory(instances, true)
	if err != nil {
		return nil, nil, err
	}
	questionTypes, err := resolveLongMemEvalQuestionTypes(inventory, selection.QuestionTypes)
	if err != nil {
		return nil, nil, err
	}
	availability := inventory.availability(questionTypes)
	devQuotas, err := resolveLongMemEvalAllocation(questionTypes, availability, selection.Dev)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve LongMemEval development allocation: %w", err)
	}
	remaining := make(map[string]int, len(questionTypes))
	for _, questionType := range questionTypes {
		remaining[questionType] = availability[questionType] - devQuotas[questionType]
	}
	holdoutQuotas, err := resolveLongMemEvalAllocation(questionTypes, remaining, selection.Holdout)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve LongMemEval holdout allocation: %w", err)
	}
	devSelected := make(map[string][]*LongMemEvalInstance, len(questionTypes))
	holdoutSelected := make(map[string][]*LongMemEvalInstance, len(questionTypes))
	for _, questionType := range questionTypes {
		candidates := append([]*LongMemEvalInstance(nil), inventory.byType[questionType]...)
		sortLongMemEvalBySeededRank(candidates, selection.Seed)
		devEnd := devQuotas[questionType]
		holdoutEnd := devEnd + holdoutQuotas[questionType]
		devSelected[questionType] = candidates[:devEnd]
		holdoutSelected[questionType] = candidates[devEnd:holdoutEnd]
	}
	dev, err := newLongMemEvalManifest(
		instances,
		LongMemEvalManifestMethodStratifiedSHA256,
		selection.Seed,
		LongMemEvalManifestSplitDev,
		questionTypes,
		devQuotas,
		nil,
		devSelected,
	)
	if err != nil {
		return nil, nil, err
	}
	holdout, err := newLongMemEvalManifest(
		instances,
		LongMemEvalManifestMethodStratifiedSHA256,
		selection.Seed,
		LongMemEvalManifestSplitHoldout,
		questionTypes,
		holdoutQuotas,
		devQuotas,
		holdoutSelected,
	)
	if err != nil {
		return nil, nil, err
	}
	return dev, holdout, nil
}

func validateLongMemEvalSeed(seed string) error {
	if !utf8.ValidString(seed) {
		return errors.New("LongMemEval seed must be valid UTF-8")
	}
	trimmed := strings.TrimSpace(seed)
	if trimmed == "" {
		return errors.New("LongMemEval stratified selection seed is required")
	}
	if seed != trimmed {
		return errors.New("LongMemEval seed must not have leading or trailing whitespace")
	}
	return nil
}

func validateLongMemEvalSelectionOffsets(
	questionTypes []string,
	split string,
	rankOffsets map[string]int,
) error {
	switch split {
	case "", LongMemEvalManifestSplitDev:
		if len(rankOffsets) != 0 {
			return errors.New("LongMemEval rank offsets are only valid for a holdout selection")
		}
		return nil
	case LongMemEvalManifestSplitHoldout:
		return validateLongMemEvalHoldoutRankOffsets(questionTypes, rankOffsets)
	default:
		return fmt.Errorf("invalid LongMemEval manifest split %q", split)
	}
}

type longMemEvalManifestInventory struct {
	byID   map[string]*LongMemEvalInstance
	byType map[string][]*LongMemEvalInstance
}

func newLongMemEvalManifestInventory(
	instances []*LongMemEvalInstance,
	requireQuestionType bool,
) (*longMemEvalManifestInventory, error) {
	inventory := &longMemEvalManifestInventory{
		byID:   make(map[string]*LongMemEvalInstance, len(instances)),
		byType: make(map[string][]*LongMemEvalInstance),
	}
	for i, inst := range instances {
		if inst == nil {
			if requireQuestionType {
				return nil, fmt.Errorf("LongMemEval dataset instance %d is nil", i)
			}
			continue
		}
		if strings.TrimSpace(inst.QuestionID) == "" {
			return nil, fmt.Errorf("LongMemEval dataset instance %d has an empty question_id", i)
		}
		if !utf8.ValidString(inst.QuestionID) {
			return nil, fmt.Errorf("LongMemEval dataset instance %d has an invalid UTF-8 question_id", i)
		}
		if _, ok := inventory.byID[inst.QuestionID]; ok {
			return nil, fmt.Errorf("LongMemEval dataset contains duplicate question_id %q", inst.QuestionID)
		}
		if requireQuestionType && strings.TrimSpace(inst.QuestionType) == "" {
			return nil, fmt.Errorf("LongMemEval dataset case %q has an empty question_type", inst.QuestionID)
		}
		if !utf8.ValidString(inst.QuestionType) {
			return nil, fmt.Errorf("LongMemEval dataset case %q has an invalid UTF-8 question_type", inst.QuestionID)
		}
		inventory.byID[inst.QuestionID] = inst
		inventory.byType[inst.QuestionType] = append(inventory.byType[inst.QuestionType], inst)
	}
	return inventory, nil
}

func (inventory *longMemEvalManifestInventory) availability(questionTypes []string) map[string]int {
	availability := make(map[string]int, len(questionTypes))
	for _, questionType := range questionTypes {
		availability[questionType] = len(inventory.byType[questionType])
	}
	return availability
}

func resolveLongMemEvalQuestionTypes(
	inventory *longMemEvalManifestInventory,
	requested []string,
) ([]string, error) {
	questionTypes := make([]string, 0, len(requested))
	if len(requested) == 0 {
		for questionType := range inventory.byType {
			if questionType != "" {
				questionTypes = append(questionTypes, questionType)
			}
		}
		sort.Strings(questionTypes)
	} else {
		for _, questionType := range requested {
			questionTypes = append(questionTypes, strings.TrimSpace(questionType))
		}
	}
	if len(questionTypes) == 0 {
		return nil, errors.New("LongMemEval selection has no question types")
	}
	if err := validateLongMemEvalQuestionTypes(questionTypes); err != nil {
		return nil, err
	}
	for _, questionType := range questionTypes {
		if len(inventory.byType[questionType]) == 0 {
			return nil, fmt.Errorf("LongMemEval dataset has no cases for question type %q", questionType)
		}
	}
	return questionTypes, nil
}

func resolveLongMemEvalAllocation(
	questionTypes []string,
	availability map[string]int,
	allocation LongMemEvalManifestAllocation,
) (map[string]int, error) {
	if allocation.TotalSize < 0 {
		return nil, errors.New("LongMemEval total size must not be negative")
	}
	if allocation.TotalSize > 0 && len(allocation.Quotas) > 0 {
		return nil, errors.New("LongMemEval total size and per-type quotas are mutually exclusive")
	}
	if len(allocation.Quotas) > 0 {
		quotas := cloneLongMemEvalQuotas(allocation.Quotas)
		if len(quotas) != len(questionTypes) {
			return nil, errors.New("LongMemEval quotas must cover every requested question type exactly once")
		}
		requested := make(map[string]struct{}, len(questionTypes))
		total := 0
		for _, questionType := range questionTypes {
			requested[questionType] = struct{}{}
			quota, ok := quotas[questionType]
			if !ok {
				return nil, fmt.Errorf("LongMemEval quota missing for question type %q", questionType)
			}
			if quota < 0 {
				return nil, fmt.Errorf("LongMemEval quota for %q must not be negative", questionType)
			}
			if quota > availability[questionType] {
				return nil, fmt.Errorf(
					"LongMemEval quota for %q is %d, only %d cases are available",
					questionType,
					quota,
					availability[questionType],
				)
			}
			total += quota
		}
		for questionType := range quotas {
			if _, ok := requested[questionType]; !ok {
				return nil, fmt.Errorf("LongMemEval quota has unrequested question type %q", questionType)
			}
		}
		if total == 0 {
			return nil, errors.New("LongMemEval quotas select no cases")
		}
		return quotas, nil
	}
	if allocation.TotalSize == 0 {
		return nil, errors.New("LongMemEval selection requires total size or per-type quotas")
	}
	return allocateLongMemEvalLargestRemainder(questionTypes, availability, allocation.TotalSize)
}

func allocateLongMemEvalLargestRemainder(
	questionTypes []string,
	availability map[string]int,
	totalSize int,
) (map[string]int, error) {
	totalAvailable := 0
	for _, questionType := range questionTypes {
		totalAvailable += availability[questionType]
	}
	if totalSize > totalAvailable {
		return nil, fmt.Errorf(
			"LongMemEval total size is %d, only %d cases are available",
			totalSize,
			totalAvailable,
		)
	}
	type remainder struct {
		questionType string
		value        int64
	}
	quotas := make(map[string]int, len(questionTypes))
	remainders := make([]remainder, 0, len(questionTypes))
	allocated := 0
	for _, questionType := range questionTypes {
		numerator := int64(totalSize) * int64(availability[questionType])
		quota := int(numerator / int64(totalAvailable))
		quotas[questionType] = quota
		allocated += quota
		remainders = append(remainders, remainder{
			questionType: questionType,
			value:        numerator % int64(totalAvailable),
		})
	}
	sort.Slice(remainders, func(i int, j int) bool {
		if remainders[i].value != remainders[j].value {
			return remainders[i].value > remainders[j].value
		}
		return remainders[i].questionType < remainders[j].questionType
	})
	for i := 0; allocated < totalSize; i++ {
		questionType := remainders[i].questionType
		if quotas[questionType] >= availability[questionType] {
			continue
		}
		quotas[questionType]++
		allocated++
	}
	return quotas, nil
}

func sortLongMemEvalBySeededRank(instances []*LongMemEvalInstance, seed string) {
	sort.Slice(instances, func(i int, j int) bool {
		left := longMemEvalSeededRank(seed, instances[i].QuestionID)
		right := longMemEvalSeededRank(seed, instances[j].QuestionID)
		if cmp := bytes.Compare(left[:], right[:]); cmp != 0 {
			return cmp < 0
		}
		return instances[i].QuestionID < instances[j].QuestionID
	})
}

func longMemEvalSeededRank(seed string, caseID string) [sha256.Size]byte {
	return sha256.Sum256([]byte("longmemeval-manifest\x00" + seed + "\x00" + caseID))
}

func newLongMemEvalManifest(
	instances []*LongMemEvalInstance,
	method LongMemEvalManifestMethod,
	seed string,
	split string,
	questionTypes []string,
	quotas map[string]int,
	rankOffsets map[string]int,
	selected map[string][]*LongMemEvalInstance,
) (*LongMemEvalManifest, error) {
	digestInstances := filterLongMemEvalManifestQuestionTypes(instances, questionTypes)
	datasetDigest, err := LongMemEvalDatasetDigest(digestInstances)
	if err != nil {
		return nil, err
	}
	manifest := &LongMemEvalManifest{
		SchemaVersion: LongMemEvalManifestSchemaVersion,
		Method:        method,
		Seed:          seed,
		Split:         split,
		QuestionTypes: append([]string(nil), questionTypes...),
		Quotas:        cloneLongMemEvalQuotas(quotas),
		DatasetDigest: datasetDigest,
	}
	if len(rankOffsets) != 0 {
		manifest.RankOffsets = cloneLongMemEvalQuotas(rankOffsets)
	}
	for _, questionType := range questionTypes {
		for _, inst := range selected[questionType] {
			manifest.CaseIDs = append(manifest.CaseIDs, inst.QuestionID)
			manifest.Cases = append(manifest.Cases, LongMemEvalManifestCase{
				CaseID:       inst.QuestionID,
				QuestionType: questionType,
			})
		}
	}
	manifest.TotalSize = len(manifest.CaseIDs)
	manifest.ManifestDigest, err = LongMemEvalManifestDigest(manifest)
	if err != nil {
		return nil, err
	}
	if err := validateLongMemEvalManifestShape(manifest); err != nil {
		return nil, err
	}
	return manifest, nil
}

func cloneLongMemEvalQuotas(quotas map[string]int) map[string]int {
	cloned := make(map[string]int, len(quotas))
	for questionType, quota := range quotas {
		cloned[questionType] = quota
	}
	return cloned
}

func filterLongMemEvalManifestQuestionTypes(
	instances []*LongMemEvalInstance,
	questionTypes []string,
) []*LongMemEvalInstance {
	typeSet := make(map[string]struct{}, len(questionTypes))
	for _, questionType := range questionTypes {
		typeSet[questionType] = struct{}{}
	}
	filtered := make([]*LongMemEvalInstance, 0, len(instances))
	for _, inst := range instances {
		if inst == nil {
			continue
		}
		if _, ok := typeSet[inst.QuestionType]; ok {
			filtered = append(filtered, inst)
		}
	}
	return filtered
}
