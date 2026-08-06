//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"sort"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
)

const frameworkModulePath = "trpc.group/trpc-go/trpc-agent-go"

type buildMetadata struct {
	SourceRevision   string
	SourceModified   bool
	BinarySHA256     string
	FrameworkModule  string
	FrameworkVersion string
}

type runIdentity struct {
	RunID                   string
	ObservationCodec        string
	SourceRevision          string
	SourceModified          bool
	BinarySHA256            string
	ModelConfigSHA256       string
	EnvironmentConfigSHA256 string
	CasesSHA256             string
	CommandTimeout          string
	CaseTimeout             string
	SelectedInstancesSHA256 string
	CleanRoom               bool
	ToolLoopWarning         bool
	CleanRoomPolicySHA256   string
	OfflineAssetsSHA256     string
	ImageSetSHA256          string
	DockerImages            map[string]sweenv.ImageIdentity
	Workers                 int
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
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

func selectedInstancesSHA256(instanceIDs []string) (string, error) {
	if len(instanceIDs) == 0 {
		return "", fmt.Errorf("selected instance list is empty")
	}
	ids := append([]string(nil), instanceIDs...)
	seen := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		if err := validateArtifactName("selected instance id", id); err != nil {
			return "", err
		}
		if _, ok := seen[id]; ok {
			return "", fmt.Errorf("duplicate selected instance id %q", id)
		}
		seen[id] = struct{}{}
	}
	sort.Strings(ids)
	h := sha256.New()
	for _, id := range ids {
		_, _ = io.WriteString(h, id)
		_, _ = io.WriteString(h, "\n")
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func currentBuildMetadata() (buildMetadata, error) {
	metadata := buildMetadata{FrameworkModule: frameworkModulePath, FrameworkVersion: "unknown"}
	if info, ok := debug.ReadBuildInfo(); ok {
		metadata.FrameworkVersion = frameworkVersionFromBuildInfo(info)
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				metadata.SourceRevision = setting.Value
			case "vcs.modified":
				metadata.SourceModified = setting.Value == "true"
			}
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return metadata, fmt.Errorf("locate runner executable: %w", err)
	}
	metadata.BinarySHA256, err = fileSHA256(executable)
	if err != nil {
		return metadata, fmt.Errorf("hash runner executable: %w", err)
	}
	return metadata, nil
}

func frameworkVersionFromBuildInfo(info *debug.BuildInfo) string {
	if info == nil {
		return "unknown"
	}
	for _, dependency := range info.Deps {
		if dependency.Path != frameworkModulePath {
			continue
		}
		if dependency.Replace != nil && dependency.Replace.Version != "" {
			return dependency.Replace.Version
		}
		if dependency.Version != "" {
			return dependency.Version
		}
		return "unknown"
	}
	return "unknown"
}
