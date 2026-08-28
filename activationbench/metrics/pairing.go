//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package metrics

import (
	"fmt"
	"strings"
)

// ValidatePairedResults verifies that static and dynamic slices can be
// compared element-by-element.  PairID is preferred when present (the CLI
// and AnnotateRepetition populate it); otherwise TaskID is used.  Repeated
// unannotated TaskIDs are rejected because their pairing would depend on
// incidental slice order after serialization or filtering.
//
// The validator intentionally checks order as well as set membership.  Raw
// slices are stored in Comparison and downstream paired tests commonly use
// the same index, so silently accepting a reordered arm would make those
// tests compare different tasks.
func ValidatePairedResults(static, dynamic []RunResult) error {
	if len(static) != len(dynamic) {
		return fmt.Errorf(
			"paired result length mismatch: static=%d dynamic=%d",
			len(static), len(dynamic),
		)
	}
	seenStatic := make(map[string]int, len(static))
	seenDynamic := make(map[string]int, len(dynamic))
	for index := range static {
		staticKey, err := resultPairKey(static[index])
		if err != nil {
			return fmt.Errorf("static result %d: %w", index, err)
		}
		dynamicKey, err := resultPairKey(dynamic[index])
		if err != nil {
			return fmt.Errorf("dynamic result %d: %w", index, err)
		}
		if previous, exists := seenStatic[staticKey]; exists {
			return fmt.Errorf(
				"duplicate static pair key %q at indexes %d and %d",
				staticKey, previous, index,
			)
		}
		if previous, exists := seenDynamic[dynamicKey]; exists {
			return fmt.Errorf(
				"duplicate dynamic pair key %q at indexes %d and %d",
				dynamicKey, previous, index,
			)
		}
		seenStatic[staticKey] = index
		seenDynamic[dynamicKey] = index
		if staticKey != dynamicKey {
			return fmt.Errorf(
				"pair key mismatch at index %d: static=%q dynamic=%q",
				index, staticKey, dynamicKey,
			)
		}
		// PairID is caller-supplied metadata.  Guard against a malformed
		// pair that reuses a PairID while naming different tasks.
		staticTaskID := strings.TrimSpace(static[index].TaskID)
		dynamicTaskID := strings.TrimSpace(dynamic[index].TaskID)
		if staticTaskID != "" && dynamicTaskID != "" && staticTaskID != dynamicTaskID {
			return fmt.Errorf(
				"task id mismatch at index %d: static=%q dynamic=%q",
				index, staticTaskID, dynamicTaskID,
			)
		}
		if err := validatePairModes(static[index].Mode, dynamic[index].Mode); err != nil {
			return fmt.Errorf("pair at index %d: %w", index, err)
		}
	}
	return nil
}

// validatePairModes catches the easy-to-miss case where a caller accidentally
// supplies the Dynamic arm as the first slice (or compares two copies of one
// arm). Empty modes remain accepted for generic metrics callers and for older
// serialized results that predate the Mode field.
func validatePairModes(static, dynamic string) error {
	static = strings.TrimSpace(static)
	dynamic = strings.TrimSpace(dynamic)
	if static == "" && dynamic == "" {
		return nil
	}
	if static == "" || dynamic == "" {
		return fmt.Errorf("mode metadata is incomplete: static=%q dynamic=%q", static, dynamic)
	}
	if static == dynamic {
		return fmt.Errorf("both paired results use mode %q", static)
	}
	const (
		staticAll         = "static-all"
		dynamicActivation = "dynamic-activation"
	)
	// If either side uses a benchmark's canonical label, require the complete
	// and correctly ordered pair. Unknown labels are left available to callers
	// that use this package for a different experiment.
	if static == staticAll || static == dynamicActivation ||
		dynamic == staticAll || dynamic == dynamicActivation {
		if static != staticAll || dynamic != dynamicActivation {
			return fmt.Errorf("unexpected benchmark mode pair: static=%q dynamic=%q", static, dynamic)
		}
	}
	return nil
}

// NewCheckedComparison validates pairing before constructing a Comparison.
// NewComparison remains available for backward compatibility with callers
// that intentionally aggregate independently collected slices; benchmark
// runners should use this checked form so an accidental task/order mismatch
// cannot become a misleading quality or token delta.
func NewCheckedComparison(
	benchmark string,
	static, dynamic []RunResult,
) (Comparison, error) {
	if err := ValidatePairedResults(static, dynamic); err != nil {
		return Comparison{}, err
	}
	return NewComparison(benchmark, static, dynamic), nil
}

func resultPairKey(result RunResult) (string, error) {
	if pairID := strings.TrimSpace(result.PairID); pairID != "" {
		return "pair:" + pairID, nil
	}
	if taskID := strings.TrimSpace(result.TaskID); taskID != "" {
		return "task:" + taskID, nil
	}
	return "", fmt.Errorf("missing PairID and TaskID")
}
