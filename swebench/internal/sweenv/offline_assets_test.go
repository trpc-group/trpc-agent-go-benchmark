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
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateOfflineAssetsNotRequired(t *testing.T) {
	if err := ValidateOfflineAssets("", []string{"django__django-15930", "psf__requests-1142"}); err != nil {
		t.Fatal(err)
	}
}

func TestValidateOfflineAssetsRequired(t *testing.T) {
	if err := ValidateOfflineAssets("", []string{"psf__requests-6028"}); err == nil ||
		!strings.Contains(err.Error(), "-offline-assets-dir is required") {
		t.Fatalf("ValidateOfflineAssets() error = %v", err)
	}

	root := t.TempDir()
	files := map[string]string{
		offlineTarpitBinary:                                          "binary",
		"requests-modern/requirements.txt":                           "pytest==6.2.5\n",
		"requests-modern/wheels/pytest-6.2.5-py3-none-any.whl":       "wheel",
		"requests-2931/requirements.txt":                             "pytest-httpbin==0.0.7\n",
		"requests-2931/wheels/pytest_httpbin-0.0.7-py2.py3-none.whl": "wheel",
	}
	var manifest strings.Builder
	for relative, contents := range files {
		path := filepath.Join(root, relative)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(contents))
		fmt.Fprintf(&manifest, "%x  ./%s\n", sum, filepath.ToSlash(relative))
	}
	if err := os.WriteFile(filepath.Join(root, offlineAssetManifest), []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOfflineAssets(root, []string{"psf__requests-2931", "psf__requests-6028"}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, offlineTarpitBinary), []byte("changed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ValidateOfflineAssets(root, []string{"psf__requests-6028"}); err == nil ||
		!strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("ValidateOfflineAssets() after mutation error = %v", err)
	}
}

func TestOfflineRequestAssets(t *testing.T) {
	tests := map[string]offlineRequestAssetSpec{
		"psf__requests-1142": {},
		"psf__requests-2317": {tarpit: true},
		"psf__requests-2931": {tarpit: true, profile: "requests-2931"},
		"psf__requests-5414": {tarpit: true, profile: "requests-modern"},
		"psf__requests-6028": {tarpit: true, profile: "requests-modern"},
	}
	for instanceID, want := range tests {
		if got := offlineRequestAssets(instanceID); got != want {
			t.Errorf("offlineRequestAssets(%q) = %+v, want %+v", instanceID, got, want)
		}
	}
}
