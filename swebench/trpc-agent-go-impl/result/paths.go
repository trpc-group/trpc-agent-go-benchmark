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
	"os"
	"path/filepath"
)

// RunDir returns base/runID, unless base already ends with runID.
func RunDir(base, runID string) string {
	if filepath.Base(base) == runID {
		return base
	}
	return filepath.Join(base, runID)
}

// EnsureDir creates a directory and returns it.
func EnsureDir(dir string) (string, error) {
	return dir, os.MkdirAll(dir, 0755)
}
