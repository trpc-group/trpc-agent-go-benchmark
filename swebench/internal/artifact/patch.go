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
		if strings.HasPrefix(line, "+++ b/") {
			file := strings.TrimPrefix(line, "+++ b/")
			if file != "/dev/null" && !seen[file] {
				seen[file] = true
				stats.ChangedFiles = append(stats.ChangedFiles, file)
			}
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
