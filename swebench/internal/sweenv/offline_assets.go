//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweenv

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	offlineAssetSchema         = "swebench-offline-assets-v1"
	offlineAssetManifest       = "SHA256SUMS"
	offlineSourceImages        = "SOURCE_IMAGES"
	offlineTarpitBinary        = "bin/swebench-tarpit"
	offlineTarpitContainerPath = "/tmp/swebench-tarpit"
	offlineTarpitReadyPath     = "/tmp/swebench-tarpit.ready"
	offlineWheelhouseRoot      = "/tmp/swebench-offline-assets"
)

type offlineRequestAssetSpec struct {
	httpbin bool
	tarpit  bool
	profile string
}

// OfflineAssetIdentity is independent of the host directory used to mount or
// copy an asset bundle.
type OfflineAssetIdentity struct {
	Schema         string `json:"schema,omitempty"`
	SHA256         string `json:"sha256,omitempty"`
	ManifestSHA256 string `json:"manifest_sha256,omitempty"`
	FileCount      int    `json:"file_count,omitempty"`
}

type offlineAssetEntry struct {
	path   string
	sha256 string
}

func offlineRequestAssets(instanceID string) offlineRequestAssetSpec {
	spec := offlineRequestAssetSpec{httpbin: usesOfflineHTTPBin(instanceID)}
	switch instanceID {
	case "psf__requests-2317":
		spec.tarpit = true
	case "psf__requests-2931":
		spec.tarpit = true
		spec.profile = "requests-2931"
	case "psf__requests-5414", "psf__requests-6028":
		spec.tarpit = true
		spec.profile = "requests-modern"
	}
	return spec
}

func usesOfflineTarpit(instanceID string) bool {
	return offlineRequestAssets(instanceID).tarpit
}

// InspectOfflineAssets validates a closed-world asset directory and returns
// its path-independent identity. If no selected case requires assets and the
// directory is empty, it returns a zero identity.
func InspectOfflineAssets(directory string, instanceIDs []string) (OfflineAssetIdentity, error) {
	required := false
	for _, instanceID := range instanceIDs {
		spec := offlineRequestAssets(instanceID)
		if spec.httpbin || spec.tarpit || spec.profile != "" {
			required = true
			break
		}
	}
	if strings.TrimSpace(directory) == "" {
		if required {
			return OfflineAssetIdentity{}, fmt.Errorf("-offline-assets-dir is required for selected requests cases")
		}
		return OfflineAssetIdentity{}, nil
	}
	entries, manifestBytes, err := loadOfflineAssetManifest(directory)
	if err != nil {
		return OfflineAssetIdentity{}, err
	}
	if err := validateOfflineAssetTree(directory, entries); err != nil {
		return OfflineAssetIdentity{}, err
	}
	for _, instanceID := range instanceIDs {
		if err := validateOfflineAssetPaths(directory, entries, offlineRequestAssets(instanceID)); err != nil {
			return OfflineAssetIdentity{}, fmt.Errorf("offline assets for %s: %w", instanceID, err)
		}
	}
	return offlineAssetIdentity(entries, manifestBytes), nil
}

func offlineAssetIdentity(entries []offlineAssetEntry, manifestBytes []byte) OfflineAssetIdentity {
	manifestSum := sha256.Sum256(manifestBytes)
	tree := sha256.New()
	_, _ = fmt.Fprintln(tree, offlineAssetSchema)
	for _, entry := range entries {
		_, _ = io.WriteString(tree, entry.path)
		_, _ = io.WriteString(tree, "\x00")
		_, _ = fmt.Fprintln(tree, entry.sha256)
	}
	return OfflineAssetIdentity{
		Schema:         offlineAssetSchema,
		SHA256:         hex.EncodeToString(tree.Sum(nil)),
		ManifestSHA256: hex.EncodeToString(manifestSum[:]),
		FileCount:      len(entries),
	}
}

func loadOfflineAssetManifest(directory string) ([]offlineAssetEntry, []byte, error) {
	if err := requireDirectoryNoSymlink(directory); err != nil {
		return nil, nil, err
	}
	manifestPath := filepath.Join(directory, offlineAssetManifest)
	if err := requireRegularFileNoSymlink(manifestPath); err != nil {
		return nil, nil, fmt.Errorf("offline asset manifest: %w", err)
	}
	manifestBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, nil, fmt.Errorf("read offline asset manifest: %w", err)
	}
	var entries []offlineAssetEntry
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(strings.NewReader(string(manifestBytes)))
	for scanner.Scan() {
		line := scanner.Text()
		separator := strings.Index(line, "  ")
		if separator != 64 || len(line) <= separator+2 {
			return nil, nil, fmt.Errorf("invalid offline asset manifest line %q", line)
		}
		digest := strings.ToLower(line[:separator])
		decoded, decodeErr := hex.DecodeString(digest)
		if decodeErr != nil || len(decoded) != sha256.Size {
			return nil, nil, fmt.Errorf("invalid SHA-256 in offline asset manifest line %q", line)
		}
		relative := line[separator+2:]
		if err := validateAssetRelativePath(relative); err != nil {
			return nil, nil, err
		}
		if relative == offlineAssetManifest {
			return nil, nil, fmt.Errorf("offline asset manifest must not declare itself")
		}
		if _, ok := seen[relative]; ok {
			return nil, nil, fmt.Errorf("duplicate offline asset manifest path %q", relative)
		}
		seen[relative] = struct{}{}
		entries = append(entries, offlineAssetEntry{path: relative, sha256: digest})
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, fmt.Errorf("read offline asset manifest: %w", err)
	}
	if len(entries) == 0 {
		return nil, nil, fmt.Errorf("offline asset manifest %s is empty", manifestPath)
	}
	if !sort.SliceIsSorted(entries, func(i, j int) bool { return entries[i].path < entries[j].path }) {
		return nil, nil, fmt.Errorf("offline asset manifest paths are not sorted")
	}
	return entries, manifestBytes, nil
}

func validateOfflineAssetTree(directory string, entries []offlineAssetEntry) error {
	declared := make(map[string]string, len(entries))
	for _, entry := range entries {
		declared[entry.path] = entry.sha256
	}
	seen := map[string]struct{}{}
	err := filepath.WalkDir(directory, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == directory {
			return nil
		}
		relative, err := filepath.Rel(directory, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("offline asset tree contains symlink %s", relative)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("offline asset tree contains non-regular file %s", relative)
		}
		if relative == offlineAssetManifest {
			return nil
		}
		expected, ok := declared[relative]
		if !ok {
			return fmt.Errorf("offline asset tree contains undeclared file %s", relative)
		}
		actual, err := regularFileSHA256(current)
		if err != nil {
			return err
		}
		if actual != expected {
			return fmt.Errorf("offline asset checksum mismatch: %s", relative)
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return fmt.Errorf("validate offline asset tree: %w", err)
	}
	for relative := range declared {
		if _, ok := seen[relative]; !ok {
			return fmt.Errorf("offline asset manifest declares missing file %s", relative)
		}
	}
	return nil
}

func validateAssetRelativePath(relative string) error {
	if relative == "" || relative != strings.TrimSpace(relative) || strings.ContainsRune(relative, '\x00') ||
		strings.Contains(relative, "\\") || strings.HasPrefix(relative, "/") ||
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative))) != relative ||
		relative == "." || relative == ".." || strings.HasPrefix(relative, "../") {
		return fmt.Errorf("unsafe or non-canonical offline asset manifest path %q", relative)
	}
	return nil
}

func validateOfflineAssetPaths(
	directory string,
	entries []offlineAssetEntry,
	spec offlineRequestAssetSpec,
) error {
	declared := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		declared[entry.path] = struct{}{}
	}
	if _, ok := declared[offlineSourceImages]; !ok {
		return fmt.Errorf("offline asset bundle has no %s", offlineSourceImages)
	}
	if spec.httpbin {
		for _, relative := range []string{
			offlineHTTPBinCAAsset,
			offlineHTTPBinCertAsset,
			offlineHTTPBinKeyAsset,
		} {
			if _, ok := declared[relative]; !ok {
				return fmt.Errorf("offline asset bundle has no %s", relative)
			}
		}
	}
	if spec.tarpit {
		if _, ok := declared[offlineTarpitBinary]; !ok {
			return fmt.Errorf("offline asset bundle has no %s", offlineTarpitBinary)
		}
		info, err := os.Lstat(filepath.Join(directory, filepath.FromSlash(offlineTarpitBinary)))
		if err != nil {
			return err
		}
		if info.Mode().Perm()&0o111 == 0 {
			return fmt.Errorf("offline tarpit helper is not executable")
		}
	}
	if spec.profile == "" {
		return nil
	}
	requirements := spec.profile + "/requirements.txt"
	if _, ok := declared[requirements]; !ok {
		return fmt.Errorf("offline asset bundle has no %s", requirements)
	}
	wheelPrefix := spec.profile + "/wheels/"
	wheels := 0
	for relative := range declared {
		if strings.HasPrefix(relative, wheelPrefix) {
			wheels++
		}
	}
	if wheels == 0 {
		return fmt.Errorf("wheelhouse for %s is empty", spec.profile)
	}
	return nil
}

func requireDirectoryNoSymlink(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("stat offline asset directory %s: %w", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("offline asset root %s is not a real directory", directory)
	}
	return nil
}

func requireRegularFileNoSymlink(file string) error {
	info, err := os.Lstat(file)
	if err != nil {
		return fmt.Errorf("stat %s: %w", file, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular non-symlink file", file)
	}
	return nil
}

func regularFileSHA256(file string) (string, error) {
	if err := requireRegularFileNoSymlink(file); err != nil {
		return "", err
	}
	f, err := os.Open(file)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func (f DockerFactory) prepareOfflineRequestAssets(
	ctx context.Context,
	environment *dockerEnvironment,
	instanceID string,
) ([]offlineAssetEntry, error) {
	spec := offlineRequestAssets(instanceID)
	if !spec.httpbin && !spec.tarpit && spec.profile == "" {
		return nil, nil
	}
	entries, manifestBytes, err := loadOfflineAssetManifest(f.OfflineAssetsDir)
	if err != nil {
		return nil, err
	}
	if err := validateOfflineAssetTree(f.OfflineAssetsDir, entries); err != nil {
		return nil, err
	}
	if err := validateOfflineAssetPaths(f.OfflineAssetsDir, entries, spec); err != nil {
		return nil, fmt.Errorf("prepare offline assets for %s: %w", instanceID, err)
	}
	if actual := offlineAssetIdentity(entries, manifestBytes); actual != f.OfflineAssets {
		return nil, fmt.Errorf("offline asset bundle identity changed before case setup")
	}
	if spec.profile != "" {
		if err := f.installOfflineRequestDependencies(ctx, environment, spec.profile, entries); err != nil {
			return nil, err
		}
	}
	if spec.tarpit {
		if err := f.startOfflineTarpitHelper(ctx, environment, entries); err != nil {
			return nil, err
		}
	}
	return entries, nil
}

func (f DockerFactory) installOfflineRequestDependencies(
	ctx context.Context,
	environment *dockerEnvironment,
	profile string,
	entries []offlineAssetEntry,
) error {
	containerProfile := offlineWheelhouseRoot + "/" + profile
	out, err := environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", environment.name, "mkdir", "-p", containerProfile+"/wheels",
	)
	if err != nil {
		return fmt.Errorf("create offline asset directory for %s: %w: %s", profile, err, strings.TrimSpace(string(out)))
	}
	requiredPrefix := profile + "/"
	for _, entry := range entries {
		if !strings.HasPrefix(entry.path, requiredPrefix) {
			continue
		}
		source := filepath.Join(f.OfflineAssetsDir, filepath.FromSlash(entry.path))
		actual, err := regularFileSHA256(source)
		if err != nil {
			return err
		}
		if actual != entry.sha256 {
			return fmt.Errorf("offline asset changed before copy: %s", entry.path)
		}
		destination := environment.name + ":" + offlineWheelhouseRoot + "/" + entry.path
		if err := f.dockerCopy(ctx, source, destination); err != nil {
			return fmt.Errorf("copy offline dependency %s: %w", entry.path, err)
		}
		containerPath := offlineWheelhouseRoot + "/" + entry.path
		if err := f.verifyContainerFileSHA256(ctx, environment.name, containerPath, entry.sha256); err != nil {
			return fmt.Errorf("verify copied offline dependency %s: %w", entry.path, err)
		}
	}
	command := "source /opt/miniconda3/bin/activate testbed && " +
		"python -m pip install --disable-pip-version-check --no-input --no-index --no-cache-dir " +
		"--find-links " + containerProfile + "/wheels -r " + containerProfile + "/requirements.txt"
	out, err = environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", "-w", "/testbed", environment.name, "bash", "-lc", command,
	)
	if err != nil {
		return fmt.Errorf("install offline dependencies for %s: %w: %s", profile, err, strings.TrimSpace(string(out)))
	}
	environment.setExtraEnv(map[string]string{
		"PIP_FIND_LINKS": containerProfile + "/wheels",
		"PIP_NO_INDEX":   "1",
	})
	return nil
}

func (f DockerFactory) dockerCopy(ctx context.Context, source, destination string) error {
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	out, err := commander.Run(ctx, dockerEnv(f.DockerHost), "docker", "cp", source, destination)
	if err != nil {
		return fmt.Errorf("docker cp %s: %w: %s", destination, err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (f DockerFactory) verifyContainerFileSHA256(
	ctx context.Context,
	container string,
	path string,
	expected string,
) error {
	if decoded, err := hex.DecodeString(expected); err != nil || len(decoded) != sha256.Size {
		return fmt.Errorf("invalid expected SHA-256 for %s", path)
	}
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	out, err := commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", container, "sha256sum", "--", path,
	)
	if err != nil {
		return fmt.Errorf("compute container SHA-256 for %s: %w: %s", path, err, strings.TrimSpace(string(out)))
	}
	fields := strings.Fields(strings.TrimSpace(string(out)))
	if len(fields) != 2 || strings.ToLower(fields[0]) != strings.ToLower(expected) || fields[1] != path {
		return fmt.Errorf("container asset checksum mismatch: %s", path)
	}
	return nil
}
