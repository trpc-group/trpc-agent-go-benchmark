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
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	offlineAssetManifest       = "SHA256SUMS"
	offlineTarpitBinary        = "bin/tag-swebench-tarpit"
	offlineTarpitContainerPath = "/tmp/tag-swebench-tarpit"
	offlineTarpitReadyPath     = "/tmp/tag-swebench-tarpit.ready"
	offlineWheelhouseRoot      = "/tmp/tag-swebench-offline-assets"
)

type offlineRequestAssetSpec struct {
	tarpit  bool
	profile string
}

func offlineRequestAssets(instanceID string) offlineRequestAssetSpec {
	switch instanceID {
	case "psf__requests-2317":
		return offlineRequestAssetSpec{tarpit: true}
	case "psf__requests-2931":
		return offlineRequestAssetSpec{tarpit: true, profile: "requests-2931"}
	case "psf__requests-5414", "psf__requests-6028":
		return offlineRequestAssetSpec{tarpit: true, profile: "requests-modern"}
	default:
		return offlineRequestAssetSpec{}
	}
}

func usesOfflineTarpit(instanceID string) bool {
	return offlineRequestAssets(instanceID).tarpit
}

// ValidateOfflineAssets verifies the prepared host assets needed by the
// selected requests cases. Cases without a local tarpit or dependency profile
// do not require an asset directory.
func ValidateOfflineAssets(directory string, instanceIDs []string) error {
	required := false
	for _, instanceID := range instanceIDs {
		spec := offlineRequestAssets(instanceID)
		if spec.tarpit || spec.profile != "" {
			required = true
			break
		}
	}
	if !required {
		return nil
	}
	if strings.TrimSpace(directory) == "" {
		return fmt.Errorf("-offline-assets-dir is required for selected requests cases")
	}
	if err := verifyOfflineAssetManifest(directory); err != nil {
		return err
	}
	for _, instanceID := range instanceIDs {
		if err := validateOfflineAssetPaths(directory, offlineRequestAssets(instanceID)); err != nil {
			return fmt.Errorf("offline assets for %s: %w", instanceID, err)
		}
	}
	return nil
}

func verifyOfflineAssetManifest(directory string) error {
	manifestPath := filepath.Join(directory, offlineAssetManifest)
	manifest, err := os.Open(manifestPath)
	if err != nil {
		return fmt.Errorf("open offline asset manifest %s: %w", manifestPath, err)
	}
	defer manifest.Close()

	entries := 0
	scanner := bufio.NewScanner(manifest)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) != 2 {
			return fmt.Errorf("invalid offline asset manifest line %q", scanner.Text())
		}
		expected, err := hex.DecodeString(fields[0])
		if err != nil || len(expected) != sha256.Size {
			return fmt.Errorf("invalid SHA-256 in offline asset manifest line %q", scanner.Text())
		}
		relative := strings.TrimPrefix(fields[1], "*")
		clean := filepath.Clean(relative)
		if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe offline asset manifest path %q", relative)
		}
		contents, err := os.ReadFile(filepath.Join(directory, clean))
		if err != nil {
			return fmt.Errorf("read offline asset %s: %w", clean, err)
		}
		actual := sha256.Sum256(contents)
		if !strings.EqualFold(hex.EncodeToString(actual[:]), fields[0]) {
			return fmt.Errorf("offline asset checksum mismatch: %s", clean)
		}
		entries++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read offline asset manifest: %w", err)
	}
	if entries == 0 {
		return fmt.Errorf("offline asset manifest %s is empty", manifestPath)
	}
	return nil
}

func validateOfflineAssetPaths(directory string, spec offlineRequestAssetSpec) error {
	if spec.tarpit {
		if err := requireRegularFile(filepath.Join(directory, offlineTarpitBinary)); err != nil {
			return err
		}
	}
	if spec.profile == "" {
		return nil
	}
	profileDir := filepath.Join(directory, spec.profile)
	if err := requireRegularFile(filepath.Join(profileDir, "requirements.txt")); err != nil {
		return err
	}
	wheels, err := os.ReadDir(filepath.Join(profileDir, "wheels"))
	if err != nil {
		return fmt.Errorf("read wheelhouse for %s: %w", spec.profile, err)
	}
	if len(wheels) == 0 {
		return fmt.Errorf("wheelhouse for %s is empty", spec.profile)
	}
	return nil
}

func requireRegularFile(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat %s: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	return nil
}

func (f DockerFactory) prepareOfflineRequestAssets(
	ctx context.Context,
	environment *dockerEnvironment,
	instanceID string,
) error {
	spec := offlineRequestAssets(instanceID)
	if !spec.tarpit && spec.profile == "" {
		return nil
	}
	if err := validateOfflineAssetPaths(f.OfflineAssetsDir, spec); err != nil {
		return fmt.Errorf("prepare offline assets for %s: %w", instanceID, err)
	}
	if spec.profile != "" {
		if err := f.installOfflineRequestDependencies(ctx, environment, spec.profile); err != nil {
			return err
		}
	}
	if spec.tarpit {
		if err := f.startOfflineTarpit(ctx, environment); err != nil {
			return err
		}
	}
	return nil
}

func (f DockerFactory) installOfflineRequestDependencies(
	ctx context.Context,
	environment *dockerEnvironment,
	profile string,
) error {
	hostProfile := filepath.Join(f.OfflineAssetsDir, profile)
	containerProfile := offlineWheelhouseRoot + "/" + profile
	out, err := environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", environment.name, "mkdir", "-p", offlineWheelhouseRoot,
	)
	if err != nil {
		return fmt.Errorf("create offline asset directory for %s: %w: %s", profile, err, strings.TrimSpace(string(out)))
	}
	if err := f.dockerCopy(
		ctx,
		hostProfile+string(filepath.Separator)+".",
		environment.name+":"+containerProfile,
	); err != nil {
		return fmt.Errorf("copy offline dependency profile for %s: %w", profile, err)
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

func (f DockerFactory) startOfflineTarpit(ctx context.Context, environment *dockerEnvironment) error {
	if err := f.dockerCopy(
		ctx,
		filepath.Join(f.OfflineAssetsDir, offlineTarpitBinary),
		environment.name+":"+offlineTarpitContainerPath,
	); err != nil {
		return fmt.Errorf("copy offline tarpit helper: %w", err)
	}
	out, err := environment.commander.Run(
		ctx,
		dockerEnv(f.DockerHost),
		"docker",
		"exec", "-d", environment.name, offlineTarpitContainerPath,
	)
	if err != nil {
		return fmt.Errorf("start offline tarpit helper: %w: %s", err, strings.TrimSpace(string(out)))
	}
	healthCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	for {
		_, err := environment.commander.Run(
			healthCtx,
			dockerEnv(f.DockerHost),
			"docker",
			"exec", environment.name, "test", "-s", offlineTarpitReadyPath,
		)
		if err == nil {
			return nil
		}
		select {
		case <-healthCtx.Done():
			return fmt.Errorf("offline tarpit helper not ready: %w", healthCtx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}
