//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package prediction

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// WriteJSONL writes predictions as one JSON object per line.
func WriteJSONL(path string, preds []Prediction) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	w := bufio.NewWriter(f)
	enc := json.NewEncoder(w)
	for _, pred := range preds {
		if err := enc.Encode(pred); err != nil {
			return err
		}
	}
	return w.Flush()
}

// WriteJSON writes predictions as a JSON array.
func WriteJSON(path string, preds []Prediction) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(preds, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}

// Read reads either JSONL or JSON-array prediction files.
func Read(path string) ([]Prediction, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" {
		return nil, nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var preds []Prediction
		if err := json.Unmarshal([]byte(trimmed), &preds); err != nil {
			return nil, err
		}
		return preds, nil
	}
	var preds []Prediction
	scanner := bufio.NewScanner(strings.NewReader(trimmed))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var pred Prediction
		if err := json.Unmarshal([]byte(line), &pred); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		preds = append(preds, pred)
	}
	return preds, scanner.Err()
}
