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
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
)

const (
	maxBundleBytes    int64 = 16 << 20
	maxQueryBytes     int64 = 64 << 20
	maxExpectedBytes  int64 = 64 << 20
	maxManifestBytes  int64 = 32 << 20
	maxNativeBytes    int64 = 256 << 20
	maxResponsesBytes int64 = 512 << 20
)

var instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

func resolveRealDirectory(value, label string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", fmt.Errorf("%s is required", label)
	}
	abs, err := filepath.Abs(value)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", label, err)
	}
	abs = filepath.Clean(abs)
	info, err := os.Lstat(abs)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("%s must be a real directory", label)
	}
	return abs, nil
}

func validateInstanceID(instanceID string) error {
	if !instanceIDPattern.MatchString(instanceID) || instanceID == "." || instanceID == ".." {
		return fmt.Errorf("invalid instance_id %q", instanceID)
	}
	return nil
}

func validateArtifactRef(ref ArtifactRef) error {
	if err := validatePortablePath(ref.Path); err != nil {
		return fmt.Errorf("artifact path %q: %w", ref.Path, err)
	}
	if !isHexSHA256(ref.SHA256) {
		return fmt.Errorf("artifact %q has invalid sha256 %q", ref.Path, ref.SHA256)
	}
	return nil
}

func validatePortablePath(value string) error {
	if value == "" || strings.TrimSpace(value) != value {
		return errors.New("path must be non-empty and have no surrounding whitespace")
	}
	if strings.ContainsRune(value, '\x00') || strings.Contains(value, `\`) {
		return errors.New("path contains a NUL or non-portable backslash")
	}
	for _, character := range value {
		if character < 0x20 || character == 0x7f {
			return errors.New("path contains a non-portable control character")
		}
	}
	if path.IsAbs(value) || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return errors.New("absolute paths are forbidden")
	}
	if path.Clean(value) != value || value == "." || value == ".." ||
		strings.HasPrefix(value, "../") {
		return errors.New("path must be clean and cannot traverse its bundle root")
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("path contains an empty or traversal component")
		}
	}
	return nil
}

func boundedArtifactPath(root, relative string) (string, error) {
	if err := validatePortablePath(relative); err != nil {
		return "", err
	}
	candidate := filepath.Join(root, filepath.FromSlash(relative))
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || filepath.IsAbs(rel) {
		return "", fmt.Errorf("artifact path escapes bundle root")
	}
	current := root
	parts := strings.Split(relative, "/")
	for index, part := range parts {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("path component %q is a symlink", strings.Join(parts[:index+1], "/"))
		}
		if index < len(parts)-1 && !info.IsDir() {
			return "", fmt.Errorf("path component %q is not a directory", strings.Join(parts[:index+1], "/"))
		}
	}
	return candidate, nil
}

func openVerifiedArtifact(root string, ref ArtifactRef) (io.ReadCloser, error) {
	if err := validateArtifactRef(ref); err != nil {
		return nil, err
	}
	filePath, err := boundedArtifactPath(root, ref.Path)
	if err != nil {
		return nil, fmt.Errorf("resolve artifact %q: %w", ref.Path, err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open artifact %q: %w", ref.Path, err)
	}
	closeOnError := func(cause error) (io.ReadCloser, error) {
		return nil, errors.Join(cause, file.Close())
	}
	info, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("stat artifact %q: %w", ref.Path, err))
	}
	if !info.Mode().IsRegular() {
		return closeOnError(fmt.Errorf("artifact %q is not a regular file", ref.Path))
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return closeOnError(fmt.Errorf("hash artifact %q: %w", ref.Path, err))
	}
	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != ref.SHA256 {
		return closeOnError(fmt.Errorf(
			"artifact %q sha256=%s, want %s",
			ref.Path,
			actual,
			ref.SHA256,
		))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return closeOnError(fmt.Errorf("rewind artifact %q: %w", ref.Path, err))
	}
	return file, nil
}

func readVerifiedArtifact(root string, ref ArtifactRef, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("artifact read limit must be positive")
	}
	reader, err := openVerifiedArtifact(root, ref)
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read artifact %q: %w", ref.Path, err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("artifact %q exceeds %d-byte limit", ref.Path, limit)
	}
	return data, nil
}

func readRegularFile(filePath, label string, limit int64) ([]byte, error) {
	if limit <= 0 {
		return nil, errors.New("file read limit must be positive")
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a real regular file", label)
	}
	if info.Size() > limit {
		return nil, fmt.Errorf("%s exceeds %d-byte limit", label, limit)
	}
	data, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	return data, nil
}

func digestRegularFile(filePath, label string, limit int64) (string, error) {
	if limit <= 0 {
		return "", errors.New("file hash limit must be positive")
	}
	info, err := os.Lstat(filePath)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", label, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s must be a real regular file", label)
	}
	if info.Size() > limit {
		return "", fmt.Errorf("%s exceeds %d-byte limit", label, limit)
	}
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("open %s: %w", label, err)
	}
	defer file.Close()
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, limit+1))
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", label, err)
	}
	if written > limit || written != info.Size() {
		return "", fmt.Errorf("%s changed size while hashing", label)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func decodeStrictJSON(data []byte, target any) error {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("JSON contains multiple top-level values")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder, "$"); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON contains trailing token %v", token)
		}
		return err
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder, location string) error {
	token, err := decoder.Token()
	if err != nil {
		return fmt.Errorf("parse JSON at %s: %w", location, err)
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return fmt.Errorf("parse JSON object at %s: %w", location, err)
			}
			key, ok := keyToken.(string)
			if !ok {
				return fmt.Errorf("JSON object at %s has non-string key", location)
			}
			if _, duplicate := seen[key]; duplicate {
				return fmt.Errorf("JSON object at %s has duplicate key %q", location, key)
			}
			seen[key] = struct{}{}
			if err := consumeJSONValue(decoder, location+"."+key); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("parse JSON object end at %s: %w", location, err)
		}
		if end != json.Delim('}') {
			return fmt.Errorf("parse JSON object end at %s: got %v", location, end)
		}
	case '[':
		for index := 0; decoder.More(); index++ {
			if err := consumeJSONValue(decoder, fmt.Sprintf("%s[%d]", location, index)); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil {
			return fmt.Errorf("parse JSON array end at %s: %w", location, err)
		}
		if end != json.Delim(']') {
			return fmt.Errorf("parse JSON array end at %s: got %v", location, end)
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter %q at %s", delimiter, location)
	}
	return nil
}

func canonicalJSONObject(data []byte) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value map[string]any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("arguments must be a JSON object: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("arguments contain multiple JSON values")
	}
	if value == nil {
		return nil, errors.New("arguments must be a non-null JSON object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return canonical, nil
}

func canonicalJSONValue(data []byte) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON contains multiple values")
	}
	return json.Marshal(value)
}

// compactJSONValue removes insignificant JSON whitespace without decoding and
// re-encoding objects, so object member order remains exactly as emitted. This
// is required for Native raw-result hashes: the outer indented case artifact
// may add whitespace around an embedded json.RawMessage, while the recorded
// hash binds the original compact json.Marshal payload.
func compactJSONValue(data []byte) ([]byte, error) {
	if err := rejectDuplicateJSONKeys(data); err != nil {
		return nil, err
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, err
	}
	return compact.Bytes(), nil
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func isHexSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func artifactHandle(root string, ref ArtifactRef) VerifiedArtifact {
	return VerifiedArtifact{RelativePath: ref.Path, SHA256: ref.SHA256, root: root}
}
