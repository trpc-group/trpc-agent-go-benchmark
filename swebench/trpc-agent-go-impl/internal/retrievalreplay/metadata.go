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
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"runtime/debug"
)

type buildMetadata struct {
	SourceRevision string
	SourceModified bool
	BinarySHA256   string
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
		return metadata, fmt.Errorf("locate replay executable: %w", err)
	}
	metadata.BinarySHA256, err = streamingFileSHA256(executable)
	if err != nil {
		return metadata, fmt.Errorf("hash replay executable: %w", err)
	}
	return metadata, nil
}

func streamingFileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
