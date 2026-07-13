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
	"path/filepath"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

// WriteCasesJSONL writes safe case manifests as JSONL.
func WriteCasesJSONL(path string, cases []contract.Case) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, c := range cases {
		data, err := json.Marshal(c)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
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
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var c contract.Case
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			return nil, err
		}
		cases = append(cases, c)
	}
	return cases, scanner.Err()
}
