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
	"sort"
	"strings"
)

// PatchStats summarizes a unified diff patch.
type PatchStats struct {
	ChangedFiles []string `json:"changed_files"`
	AddedLines   int      `json:"added_lines"`
	DeletedLines int      `json:"deleted_lines"`
	PatchLines   int      `json:"patch_lines"`
}

// ComputePatchStats summarizes changed files and line counts from a unified diff.
func ComputePatchStats(patch string) PatchStats {
	stats := PatchStats{ChangedFiles: []string{}}
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		if line == "" {
			continue
		}
		stats.PatchLines++
		if file, ok := changedFileFromPatchLine(line); ok {
			if !seen[file] {
				seen[file] = true
				stats.ChangedFiles = append(stats.ChangedFiles, file)
			}
		}
		if strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---") {
			continue
		}
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			stats.AddedLines++
		}
		if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			stats.DeletedLines++
		}
	}
	sort.Strings(stats.ChangedFiles)
	return stats
}

func changedFileFromPatchLine(line string) (string, bool) {
	prefixes := []string{"+++ b/", "--- a/", "rename from ", "rename to "}
	for _, prefix := range prefixes {
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		file := strings.TrimSpace(strings.TrimPrefix(line, prefix))
		file = strings.Trim(file, `"`)
		if file == "" || file == "/dev/null" {
			return "", false
		}
		return file, true
	}
	return "", false
}
