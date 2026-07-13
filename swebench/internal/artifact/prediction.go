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
	var byID map[string]contract.Prediction
	if err := json.Unmarshal(data, &byID); err == nil && byID != nil {
		for id, pred := range byID {
			if pred.InstanceID == "" {
				pred.InstanceID = id
				byID[id] = pred
			}
		}
		return byID, nil
	}
	var rows []contract.Prediction
	if err := json.Unmarshal(data, &rows); err == nil {
		out := map[string]contract.Prediction{}
		for _, row := range rows {
			out[row.InstanceID] = row
		}
		return out, nil
	}
	out := map[string]contract.Prediction{}
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var row contract.Prediction
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return nil, err
		}
		out[row.InstanceID] = row
	}
	return out, scanner.Err()
}
