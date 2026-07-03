//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package dataset

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// LoadJSONL loads SWE-Bench instances from a JSONL file.
func LoadJSONL(path string) ([]Instance, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	var instances []Instance
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 64*1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var inst Instance
		if err := json.Unmarshal([]byte(line), &inst); err != nil {
			return nil, fmt.Errorf("%s:%d: %w", path, lineNo, err)
		}
		if inst.InstanceID == "" {
			return nil, fmt.Errorf("%s:%d: missing instance_id", path, lineNo)
		}
		inst.Raw = append(inst.Raw[:0], line...)
		instances = append(instances, inst)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return instances, nil
}

// Select filters instances by selector. Selector accepts "all",
// comma-separated instance IDs, or a path to a file containing one ID per line.
func Select(instances []Instance, selector string, max int) ([]Instance, error) {
	if selector == "" {
		selector = "all"
	}
	ids, err := selectorIDs(selector)
	if err != nil {
		return nil, err
	}

	byID := make(map[string]Instance, len(instances))
	for _, inst := range instances {
		byID[inst.InstanceID] = inst
	}

	var selected []Instance
	if len(ids) == 0 {
		selected = append(selected, instances...)
	} else {
		for _, id := range ids {
			inst, ok := byID[id]
			if !ok {
				return nil, fmt.Errorf("selected instance %q not found", id)
			}
			selected = append(selected, inst)
		}
	}
	if max > 0 && len(selected) > max {
		selected = selected[:max]
	}
	return selected, nil
}

// IDs returns sorted instance IDs.
func IDs(instances []Instance) []string {
	ids := make([]string, 0, len(instances))
	for _, inst := range instances {
		ids = append(ids, inst.InstanceID)
	}
	sort.Strings(ids)
	return ids
}

func selectorIDs(selector string) ([]string, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" || selector == "all" {
		return nil, nil
	}
	if data, err := os.ReadFile(selector); err == nil {
		return parseIDList(string(data)), nil
	}
	return parseIDList(selector), nil
}

func parseIDList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == '\n' || r == '\r' || r == '\t' || r == ' '
	})
	ids := make([]string, 0, len(fields))
	seen := map[string]bool{}
	for _, field := range fields {
		id := strings.TrimSpace(field)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids
}
