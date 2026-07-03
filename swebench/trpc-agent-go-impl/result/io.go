//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package result

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// WriteJSON writes an indented JSON file.
func WriteJSON(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0644)
}

// WriteCasesJSONL writes normalized case results.
func WriteCasesJSONL(path string, cases []CaseResult) error {
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
	for _, c := range cases {
		if err := enc.Encode(c); err != nil {
			return err
		}
	}
	return w.Flush()
}

// ReadCasesJSONL reads normalized case results.
func ReadCasesJSONL(path string) ([]CaseResult, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cases []CaseResult
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c CaseResult
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, scanner.Err()
}

// Summarize computes aggregate metrics.
func Summarize(runID, runner string, cases []CaseResult) Summary {
	s := Summary{RunID: runID, Runner: runner, Total: len(cases)}
	for _, c := range cases {
		switch c.Status {
		case "resolved":
			s.Resolved++
		case "error":
			s.Errors++
		case "incomplete", "pending_verifier":
			s.Incomplete++
		default:
			s.Unresolved++
		}
		s.TotalTokens += c.Usage.TotalTokens
		s.APICalls += c.Usage.APICalls
		s.DurationMs += c.DurationMs
	}
	if s.Total > 0 {
		s.ResolvedRate = float64(s.Resolved) / float64(s.Total)
	}
	return s
}
