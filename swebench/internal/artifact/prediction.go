//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package artifact

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

// ReadPredictions reads SWE-Bench predictions from map JSON, array JSON, or JSONL.
func ReadPredictions(path string) (map[string]contract.Prediction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(data)) == "" {
		return nil, fmt.Errorf("predictions file %s is empty", path)
	}
	if json.Valid(data) && strings.HasPrefix(strings.TrimSpace(string(data)), "{") {
		if err := rejectDuplicateTopLevelKeys(data); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
	}
	var byID map[string]contract.Prediction
	if err := json.Unmarshal(data, &byID); err == nil && byID != nil {
		if len(byID) == 0 {
			return nil, fmt.Errorf("predictions file %s contains no predictions", path)
		}
		for id, pred := range byID {
			if err := validatePredictionID(id); err != nil {
				return nil, fmt.Errorf("prediction map key: %w", err)
			}
			if pred.InstanceID == "" {
				pred.InstanceID = id
			} else if pred.InstanceID != id {
				return nil, fmt.Errorf(
					"prediction map key %q does not match instance_id %q",
					id,
					pred.InstanceID,
				)
			}
			byID[id] = pred
		}
		return byID, nil
	}
	var rows []contract.Prediction
	if err := json.Unmarshal(data, &rows); err == nil {
		return predictionsByID(rows, path)
	}
	rows = nil
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 64*1024), maxJSONLLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row contract.Prediction
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", path, lineNumber, err)
		}
		rows = append(rows, row)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return predictionsByID(rows, path)
}

func rejectDuplicateTopLevelKeys(data []byte) error {
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil
	}
	seen := map[string]struct{}{}
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return fmt.Errorf("non-string object key")
		}
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate top-level key %q", key)
		}
		seen[key] = struct{}{}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}

func predictionsByID(rows []contract.Prediction, source string) (map[string]contract.Prediction, error) {
	if len(rows) == 0 {
		return nil, fmt.Errorf("predictions file %s contains no predictions", source)
	}
	out := make(map[string]contract.Prediction, len(rows))
	for i, row := range rows {
		if err := validatePredictionID(row.InstanceID); err != nil {
			return nil, fmt.Errorf("prediction at index %d: %w", i, err)
		}
		if _, ok := out[row.InstanceID]; ok {
			return nil, fmt.Errorf("duplicate prediction instance_id %q in %s", row.InstanceID, source)
		}
		out[row.InstanceID] = row
	}
	return out, nil
}

func validatePredictionID(id string) error {
	if strings.TrimSpace(id) == "" {
		return fmt.Errorf("empty instance_id")
	}
	if strings.TrimSpace(id) != id {
		return fmt.Errorf("surrounding whitespace in instance_id %q", id)
	}
	return nil
}
