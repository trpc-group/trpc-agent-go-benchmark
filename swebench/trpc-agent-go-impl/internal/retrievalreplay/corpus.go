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
	"archive/tar"
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const (
	maxCorpusFiles     = 100_000
	maxCorpusFileBytes = 64 << 20
	maxCorpusTotal     = 2 << 30
)

var replayExcludedDirectories = map[string]struct{}{
	".git": {}, ".tox": {}, ".venv": {}, "venv": {}, "node_modules": {},
	"build": {}, "dist": {}, "__pycache__": {},
}

type corpusFile struct {
	Path    string
	Content []byte
}

type corpusJSONLRecord struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func scanFrozenCorpus(root string) ([]corpusFile, error) {
	root, err := resolveRealDirectory(root, "case corpus directory")
	if err != nil {
		return nil, err
	}
	var files []corpusFile
	var total int64
	err = filepath.WalkDir(root, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if filePath == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("corpus contains symlink %q", filePath)
		}
		if entry.IsDir() {
			if _, excluded := replayExcludedDirectories[entry.Name()]; excluded {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeType != 0 {
			return fmt.Errorf("corpus contains special file %q", filePath)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("corpus contains non-regular file %q", filePath)
		}
		if strings.ToLower(filepath.Ext(entry.Name())) != ".py" || info.Size() == 0 {
			return nil
		}
		if info.Size() > maxCorpusFileBytes {
			return fmt.Errorf("eligible corpus file %q exceeds %d bytes", filePath, maxCorpusFileBytes)
		}
		relative, err := filepath.Rel(root, filePath)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(filepath.Clean(relative))
		if err := validateCorpusPath(relative); err != nil {
			return fmt.Errorf("eligible corpus file %q: %w", filePath, err)
		}
		content, err := os.ReadFile(filePath)
		if err != nil {
			return err
		}
		total += int64(len(content))
		if total > maxCorpusTotal {
			return fmt.Errorf("eligible corpus exceeds %d bytes", maxCorpusTotal)
		}
		files = append(files, corpusFile{Path: relative, Content: content})
		if len(files) > maxCorpusFiles {
			return fmt.Errorf("eligible corpus exceeds %d files", maxCorpusFiles)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, errors.New("case corpus contains no eligible non-empty .py files")
	}
	slices.SortFunc(files, func(left, right corpusFile) int {
		return strings.Compare(left.Path, right.Path)
	})
	return files, nil
}

func validateCorpusPath(value string) error {
	if err := validatePortablePath(value); err != nil {
		return err
	}
	if strings.ToLower(filepath.Ext(value)) != ".py" {
		return errors.New("portable corpus may contain only .py files")
	}
	for _, part := range strings.Split(value, "/") {
		if _, excluded := replayExcludedDirectories[part]; excluded {
			return fmt.Errorf("path contains excluded directory %q", part)
		}
	}
	return nil
}

func writeDeterministicCorpusTar(destination string, files []corpusFile) error {
	if len(files) == 0 {
		return errors.New("cannot write an empty corpus")
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	keep := false
	defer func() {
		_ = file.Close()
		if !keep {
			_ = os.Remove(destination)
		}
	}()
	writer := tar.NewWriter(file)
	previous := ""
	for _, one := range files {
		if err := validateCorpusPath(one.Path); err != nil {
			return err
		}
		if previous != "" && strings.Compare(previous, one.Path) >= 0 {
			return errors.New("corpus files must be strictly sorted")
		}
		if len(one.Content) == 0 || len(one.Content) > maxCorpusFileBytes {
			return fmt.Errorf("corpus file %q has invalid size", one.Path)
		}
		header := &tar.Header{
			Name: one.Path, Mode: 0o644, Size: int64(len(one.Content)),
			ModTime: time.Unix(0, 0).UTC(), Format: tar.FormatPAX,
		}
		if err := writer.WriteHeader(header); err != nil {
			return err
		}
		if _, err := writer.Write(one.Content); err != nil {
			return err
		}
		previous = one.Path
	}
	if err := writer.Close(); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	keep = true
	return nil
}

func materializeCorpus(artifact VerifiedArtifact, format, destination string) error {
	reader, err := artifact.Open()
	if err != nil {
		return err
	}
	defer reader.Close()
	switch format {
	case "workspace-tar-v1":
		return materializeCorpusTar(reader, destination)
	case "eligible-corpus-jsonl-v1":
		return materializeCorpusJSONL(reader, destination)
	default:
		return fmt.Errorf("unsupported corpus format %q", format)
	}
}

func materializeCorpusTar(reader io.Reader, destination string) error {
	archive := tar.NewReader(reader)
	seen := make(map[string]struct{})
	previous := ""
	var total int64
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read corpus tar: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return fmt.Errorf("corpus tar entry %q is not a regular file", header.Name)
		}
		if err := validateCorpusPath(header.Name); err != nil {
			return fmt.Errorf("corpus tar entry %q: %w", header.Name, err)
		}
		if _, duplicate := seen[header.Name]; duplicate {
			return fmt.Errorf("corpus tar repeats path %q", header.Name)
		}
		if previous != "" && strings.Compare(previous, header.Name) >= 0 {
			return errors.New("corpus tar entries are not strictly sorted")
		}
		if header.Size <= 0 || header.Size > maxCorpusFileBytes {
			return fmt.Errorf("corpus tar entry %q has invalid size", header.Name)
		}
		total += header.Size
		if total > maxCorpusTotal || len(seen) >= maxCorpusFiles {
			return errors.New("corpus tar exceeds materialization limits")
		}
		content, err := io.ReadAll(io.LimitReader(archive, header.Size+1))
		if err != nil {
			return err
		}
		if int64(len(content)) != header.Size {
			return fmt.Errorf("corpus tar entry %q size mismatch", header.Name)
		}
		if err := writeCorpusFile(destination, header.Name, content); err != nil {
			return err
		}
		seen[header.Name] = struct{}{}
		previous = header.Name
	}
	if len(seen) == 0 {
		return errors.New("corpus tar is empty")
	}
	return nil
}

func materializeCorpusJSONL(reader io.Reader, destination string) error {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), maxCorpusFileBytes+(1<<20))
	seen := make(map[string]struct{})
	previous := ""
	var total int64
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			return errors.New("eligible corpus JSONL contains a blank line")
		}
		var record corpusJSONLRecord
		if err := decodeStrictJSON(line, &record); err != nil {
			return fmt.Errorf("decode eligible corpus JSONL row %d: %w", len(seen)+1, err)
		}
		if err := validateCorpusPath(record.Path); err != nil {
			return fmt.Errorf("eligible corpus path %q: %w", record.Path, err)
		}
		if _, duplicate := seen[record.Path]; duplicate {
			return fmt.Errorf("eligible corpus repeats path %q", record.Path)
		}
		if previous != "" && strings.Compare(previous, record.Path) >= 0 {
			return errors.New("eligible corpus rows are not strictly sorted")
		}
		content := []byte(record.Content)
		if len(content) == 0 || len(content) > maxCorpusFileBytes {
			return fmt.Errorf("eligible corpus file %q has invalid size", record.Path)
		}
		total += int64(len(content))
		if total > maxCorpusTotal || len(seen) >= maxCorpusFiles {
			return errors.New("eligible corpus exceeds materialization limits")
		}
		if err := writeCorpusFile(destination, record.Path, content); err != nil {
			return err
		}
		seen[record.Path] = struct{}{}
		previous = record.Path
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(seen) == 0 {
		return errors.New("eligible corpus JSONL is empty")
	}
	return nil
}

func writeCorpusFile(root, relative string, content []byte) error {
	destination := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, destination)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("corpus path %q escapes destination", relative)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(content); err != nil {
		_ = file.Close()
		return err
	}
	return file.Close()
}

func encodeCorpusJSONL(files []corpusFile) ([]byte, error) {
	var result bytes.Buffer
	encoder := json.NewEncoder(&result)
	encoder.SetEscapeHTML(false)
	for _, one := range files {
		if err := encoder.Encode(corpusJSONLRecord{Path: one.Path, Content: string(one.Content)}); err != nil {
			return nil, err
		}
	}
	return result.Bytes(), nil
}
