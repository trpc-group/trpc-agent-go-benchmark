//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"bufio"
	"fmt"
	"slices"
	"strings"
)

func parsePatchTargets(patch string) (patchTargets, error) {
	var targets patchTargets
	targetSet := make(map[string]struct{})
	newFileSet := make(map[string]struct{})
	anchorSet := make(map[string]struct{})
	var currentFile string
	var pendingOld string
	var hunkLines []string

	flushHunk := func() {
		if currentFile == "" || len(hunkLines) == 0 {
			hunkLines = nil
			return
		}
		windowSize := min(3, len(hunkLines))
		for start := 0; start+windowSize <= len(hunkLines); start++ {
			text := strings.Join(hunkLines[start:start+windowSize], " ")
			key := currentFile + "\x00" + text
			if _, exists := anchorSet[key]; exists {
				continue
			}
			anchorSet[key] = struct{}{}
			targets.Anchors = append(targets.Anchors, patchAnchor{
				File: currentFile,
				Text: text,
			})
		}
		hunkLines = nil
	}

	scanner := bufio.NewScanner(strings.NewReader(patch))
	scanner.Buffer(make([]byte, 64*1024), 32*1024*1024)
	inHunk := false
	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushHunk()
			currentFile = ""
			pendingOld = ""
			inHunk = false
		case !inHunk && strings.HasPrefix(line, "--- "):
			flushHunk()
			pendingOld = normalizePatchPath(strings.TrimSpace(strings.TrimPrefix(line, "--- ")))
			currentFile = ""
			inHunk = false
		case !inHunk && strings.HasPrefix(line, "+++ "):
			newPath := normalizePatchPath(strings.TrimSpace(strings.TrimPrefix(line, "+++ ")))
			if pendingOld == "" {
				if newPath != "" {
					newFileSet[newPath] = struct{}{}
				}
				currentFile = ""
			} else {
				currentFile = pendingOld
				targetSet[currentFile] = struct{}{}
			}
			inHunk = false
		case strings.HasPrefix(line, "@@ "):
			flushHunk()
			inHunk = true
		case inHunk && len(line) > 0 && (line[0] == ' ' || line[0] == '-'):
			normalized := normalizeAnchorText(line[1:])
			if normalized != "" {
				hunkLines = append(hunkLines, normalized)
			}
		case inHunk && strings.HasPrefix(line, `\ No newline at end of file`):
			continue
		}
	}
	flushHunk()
	if err := scanner.Err(); err != nil {
		return patchTargets{}, fmt.Errorf("scan gold patch: %w", err)
	}

	for path := range targetSet {
		targets.TargetFiles = append(targets.TargetFiles, path)
	}
	for path := range newFileSet {
		targets.NewFiles = append(targets.NewFiles, path)
	}
	slices.Sort(targets.TargetFiles)
	slices.Sort(targets.NewFiles)
	slices.SortFunc(targets.Anchors, func(a, b patchAnchor) int {
		if a.File != b.File {
			return strings.Compare(a.File, b.File)
		}
		return strings.Compare(a.Text, b.Text)
	})
	return targets, nil
}

func normalizePatchPath(path string) string {
	fields := strings.Fields(path)
	if len(fields) == 0 {
		return ""
	}
	path = fields[0]
	if path == "/dev/null" {
		return ""
	}
	path = strings.TrimPrefix(path, "a/")
	path = strings.TrimPrefix(path, "b/")
	return strings.TrimPrefix(strings.ReplaceAll(path, "\\", "/"), "./")
}

func normalizeAnchorText(text string) string {
	return strings.Join(strings.Fields(text), " ")
}
