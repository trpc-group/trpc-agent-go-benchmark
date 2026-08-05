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
	"regexp"
	"runtime/debug"
	"sort"
	"strings"
)

const frameworkModulePath = "trpc.group/trpc-go/trpc-agent-go"

var artifactNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type runIdentity struct {
	RunID                   string
	ObservationCodec        string
	SourceRevision          string
	SourceModified          bool
	BinarySHA256            string
	ModelConfigHash         string
	EnvironmentConfigSHA256 string
	CasesHash               string
	CommandTimeout          string
	CaseTimeout             string
	SelectedInstancesSHA256 string
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

type buildMetadata struct {
	SourceRevision   string
	SourceModified   bool
	BinarySHA256     string
	FrameworkModule  string
	FrameworkVersion string
}

func validateArtifactName(kind, value string) error {
	if len(value) == 0 || len(value) > 128 || value == "." || value == ".." || !artifactNamePattern.MatchString(value) {
		return fmt.Errorf("invalid %s %q: use 1-128 letters, digits, dots, underscores, or hyphens", kind, value)
	}
	return nil
}

func currentBuildMetadata() (buildMetadata, error) {
	metadata := buildMetadata{FrameworkModule: frameworkModulePath}
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, setting := range info.Settings {
			switch setting.Key {
			case "vcs.revision":
				metadata.SourceRevision = setting.Value
			case "vcs.modified":
				metadata.SourceModified = setting.Value == "true"
			}
		}
		metadata.FrameworkVersion = dependencyVersion(info, frameworkModulePath)
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

func dependencyVersion(info *debug.BuildInfo, modulePath string) string {
	if info == nil {
		return ""
	}
	if info.Main.Path == modulePath {
		return displayModuleVersion(&info.Main)
	}
	for _, dependency := range info.Deps {
		if dependency.Path == modulePath {
			return displayModuleVersion(dependency)
		}
	}
	return ""
}

func displayModuleVersion(module *debug.Module) string {
	if module == nil {
		return ""
	}
	if module.Replace != nil {
		if version := strings.TrimSpace(module.Replace.Version); version != "" {
			return version
		}
		if strings.TrimSpace(module.Replace.Path) != "" {
			return "local-replacement"
		}
	}
	return strings.TrimSpace(module.Version)
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
