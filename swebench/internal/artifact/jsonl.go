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
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

const maxJSONLLineBytes = 64 * 1024 * 1024

// WriteCasesJSONL writes safe case manifests as JSONL.
func WriteCasesJSONL(path string, cases []contract.Case) error {
	if err := validateCases(cases); err != nil {
		return err
	}
	var b bytes.Buffer
	for _, c := range cases {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return WriteFileAtomic(path, b.Bytes(), 0o644)
}

// ReadCasesJSONL reads safe case manifests from JSONL.
func ReadCasesJSONL(path string) ([]contract.Case, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var cases []contract.Case
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxJSONLLineBytes)
	lineNumber := 0
	for scanner.Scan() {
		lineNumber++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c contract.Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, fmt.Errorf("parse %s line %d: %w", path, lineNumber, err)
		}
		cases = append(cases, c)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if err := validateCases(cases); err != nil {
		return nil, fmt.Errorf("validate %s: %w", path, err)
	}
	return cases, nil
}

func validateCases(cases []contract.Case) error {
	if len(cases) == 0 {
		return fmt.Errorf("case manifest contains no cases")
	}
	seen := make(map[string]struct{}, len(cases))
	for i, c := range cases {
		id := strings.TrimSpace(c.InstanceID)
		if id == "" {
			return fmt.Errorf("case at index %d has empty instance_id", i)
		}
		if id != c.InstanceID {
			return fmt.Errorf("case at index %d has surrounding whitespace in instance_id %q", i, c.InstanceID)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate case instance_id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}
