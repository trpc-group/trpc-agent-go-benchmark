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
	"strings"
)

var billingLabelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

type buildMetadata struct {
	SourceRevision string
	SourceModified bool
	BinarySHA256   string
}

func resolveBillingAgentName(base, tag, experimentID string) (string, error) {
	base = strings.TrimSpace(base)
	tag = strings.TrimSpace(tag)
	experimentID = strings.TrimSpace(experimentID)
	if tag == "" && experimentID == "" {
		return base, nil
	}
	if tag == "" || experimentID == "" {
		return "", fmt.Errorf("-billing-tag and -experiment-id must be set together")
	}
	if base == "" {
		return "", fmt.Errorf("model config has no X-SMG-Agent-Name for billing tag")
	}
	for name, value := range map[string]string{"billing tag": tag, "experiment id": experimentID} {
		if !billingLabelPattern.MatchString(value) {
			return "", fmt.Errorf("%s must contain only letters, digits, dot, underscore, or hyphen", name)
		}
	}
	resolved := base + "-" + tag
	if len(resolved) > 128 {
		return "", fmt.Errorf("resolved X-SMG-Agent-Name exceeds 128 bytes")
	}
	return resolved, nil
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

func stringSHA256(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func currentBuildMetadata() (buildMetadata, error) {
	metadata := buildMetadata{}
	if info, ok := debug.ReadBuildInfo(); ok {
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
