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
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestInspectOfflineAssetsValidClosedWorldAndPortableIdentity(t *testing.T) {
	files := withOfflineHTTPBinTestAssets(map[string]assetTestFile{
		offlineSourceImages:                 {content: "compiler=sha256:abc\n"},
		offlineTarpitBinary:                 {content: "binary", mode: 0o755},
		"requests-2931/requirements.txt":    {content: "requests==2.4.3\n"},
		"requests-2931/wheels/requests.whl": {content: "wheel"},
	})
	first := writeAssetTestBundle(t, files)
	second := writeAssetTestBundle(t, files)
	selected := []string{"psf__requests-2931"}

	firstIdentity, err := InspectOfflineAssets(first, selected)
	if err != nil {
		t.Fatal(err)
	}
	secondIdentity, err := InspectOfflineAssets(second, selected)
	if err != nil {
		t.Fatal(err)
	}
	if firstIdentity != secondIdentity {
		t.Fatalf("identities differ by host directory: %#v != %#v", firstIdentity, secondIdentity)
	}
	if firstIdentity.Schema != offlineAssetSchema || firstIdentity.FileCount != len(files) ||
		len(firstIdentity.SHA256) != 64 || len(firstIdentity.ManifestSHA256) != 64 {
		t.Fatalf("unexpected identity: %#v", firstIdentity)
	}
}

func TestInspectOfflineAssetsOptionalAndRequiredDirectory(t *testing.T) {
	identity, err := InspectOfflineAssets("", []string{"django__django-10000"})
	if err != nil {
		t.Fatal(err)
	}
	if identity != (OfflineAssetIdentity{}) {
		t.Fatalf("identity = %#v, want zero", identity)
	}
	if _, err := InspectOfflineAssets("", []string{"psf__requests-2317"}); err == nil ||
		!strings.Contains(err.Error(), "-offline-assets-dir is required") {
		t.Fatalf("required directory error = %v", err)
	}
	if _, err := InspectOfflineAssets("", []string{"psf__requests-9999"}); err == nil ||
		!strings.Contains(err.Error(), "-offline-assets-dir is required") {
		t.Fatalf("httpbin-only directory error = %v", err)
	}
}

func TestPrepareOfflineAssetsRejectsManifestReplacementAfterFreeze(t *testing.T) {
	root := writeAssetTestBundle(t, withOfflineHTTPBinTestAssets(map[string]assetTestFile{
		offlineSourceImages: {content: "source-v1\n"},
		offlineTarpitBinary: {content: "binary", mode: 0o755},
	}))
	frozen, err := InspectOfflineAssets(root, []string{"psf__requests-2317"})
	if err != nil {
		t.Fatal(err)
	}
	changed := []byte("source-v2\n")
	if err := os.WriteFile(filepath.Join(root, offlineSourceImages), changed, 0o644); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(root, offlineAssetManifest)
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	oldDigest := sha256.Sum256([]byte("source-v1\n"))
	newDigest := sha256.Sum256(changed)
	manifest = []byte(strings.Replace(
		string(manifest),
		hex.EncodeToString(oldDigest[:]),
		hex.EncodeToString(newDigest[:]),
		1,
	))
	if err := os.WriteFile(manifestPath, manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	factory := DockerFactory{OfflineAssetsDir: root, OfflineAssets: frozen}
	_, err = factory.prepareOfflineRequestAssets(
		context.Background(),
		&dockerEnvironment{},
		"psf__requests-2317",
	)
	if err == nil || !strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("prepareOfflineRequestAssets() error = %v", err)
	}
}

func TestInspectOfflineAssetsRejectsClosedWorldViolations(t *testing.T) {
	base := func() map[string]assetTestFile {
		return withOfflineHTTPBinTestAssets(map[string]assetTestFile{
			offlineSourceImages: {content: "source\n"},
			offlineTarpitBinary: {content: "binary", mode: 0o755},
		})
	}
	tests := []struct {
		name    string
		mutate  func(*testing.T, string)
		wantErr string
	}{
		{
			name: "checksum mutation",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, offlineSourceImages), []byte("changed"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "checksum mismatch",
		},
		{
			name: "undeclared file",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(root, "extra"), []byte("extra"), 0o644); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "undeclared file extra",
		},
		{
			name: "declared missing file",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				if err := os.Remove(filepath.Join(root, offlineSourceImages)); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "declares missing file",
		},
		{
			name: "file symlink",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				target := filepath.Join(root, "outside")
				if err := os.WriteFile(target, []byte("source\n"), 0o644); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(filepath.Join(root, offlineSourceImages)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, offlineSourceImages)); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "contains symlink",
		},
		{
			name: "directory symlink",
			mutate: func(t *testing.T, root string) {
				t.Helper()
				target := t.TempDir()
				if err := os.Symlink(target, filepath.Join(root, "linked-dir")); err != nil {
					t.Fatal(err)
				}
			},
			wantErr: "contains symlink linked-dir",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := writeAssetTestBundle(t, base())
			test.mutate(t, root)
			_, err := InspectOfflineAssets(root, []string{"psf__requests-2317"})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestInspectOfflineAssetsRejectsUnsafeManifest(t *testing.T) {
	digest := strings.Repeat("0", 64)
	tests := []struct {
		name     string
		manifest string
		wantErr  string
	}{
		{name: "duplicate", manifest: digest + "  duplicate\n" + digest + "  duplicate\n", wantErr: "duplicate"},
		{name: "traversal", manifest: digest + "  ../outside\n", wantErr: "unsafe or non-canonical"},
		{name: "absolute", manifest: digest + "  /outside\n", wantErr: "unsafe or non-canonical"},
		{name: "backslash", manifest: digest + "  dir\\file\n", wantErr: "unsafe or non-canonical"},
		{name: "self declaration", manifest: digest + "  SHA256SUMS\n", wantErr: "must not declare itself"},
		{name: "unsorted", manifest: digest + "  z\n" + digest + "  a\n", wantErr: "not sorted"},
		{name: "invalid digest", manifest: "invalid  file\n", wantErr: "invalid offline asset manifest line"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, offlineAssetManifest), []byte(test.manifest), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := InspectOfflineAssets(root, nil)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
		})
	}
}

func TestInspectOfflineAssetsRejectsRootAndManifestSymlinks(t *testing.T) {
	realRoot := writeAssetTestBundle(t, map[string]assetTestFile{
		offlineSourceImages: {content: "source\n"},
	})
	symlinkRoot := filepath.Join(t.TempDir(), "assets")
	if err := os.Symlink(realRoot, symlinkRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectOfflineAssets(symlinkRoot, nil); err == nil || !strings.Contains(err.Error(), "not a real directory") {
		t.Fatalf("root symlink error = %v", err)
	}

	realManifest := filepath.Join(realRoot, offlineAssetManifest)
	backupManifest := filepath.Join(realRoot, "manifest-copy")
	if err := os.Rename(realManifest, backupManifest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(backupManifest, realManifest); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectOfflineAssets(realRoot, nil); err == nil || !strings.Contains(err.Error(), "regular non-symlink") {
		t.Fatalf("manifest symlink error = %v", err)
	}
}

func TestInspectOfflineAssetsRejectsSpecialFile(t *testing.T) {
	root := writeAssetTestBundle(t, map[string]assetTestFile{
		offlineSourceImages: {content: "source\n"},
	})
	pipe := filepath.Join(root, "pipe")
	if err := os.Remove(filepath.Join(root, offlineAssetManifest)); err != nil {
		t.Fatal(err)
	}
	if err := exec.Command("mkfifo", pipe).Run(); err != nil {
		t.Skipf("mkfifo unavailable: %v", err)
	}
	digest := sha256.Sum256(nil)
	manifest := assetTestManifest(t, root, offlineSourceImages) + hex.EncodeToString(digest[:]) + "  pipe\n"
	if err := os.WriteFile(filepath.Join(root, offlineAssetManifest), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := InspectOfflineAssets(root, nil)
	if err == nil || !strings.Contains(err.Error(), "non-regular file pipe") {
		t.Fatalf("special file error = %v", err)
	}
}

func TestInspectOfflineAssetsValidatesPerCaseRequirements(t *testing.T) {
	t.Run("httpbin certificates are part of the frozen bundle", func(t *testing.T) {
		files := withOfflineHTTPBinTestAssets(map[string]assetTestFile{
			offlineSourceImages: {content: "source\n"},
			offlineTarpitBinary: {content: "binary", mode: 0o755},
		})
		delete(files, offlineHTTPBinKeyAsset)
		root := writeAssetTestBundle(t, files)
		_, err := InspectOfflineAssets(root, []string{"psf__requests-2317"})
		if err == nil || !strings.Contains(err.Error(), "has no "+offlineHTTPBinKeyAsset) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("tarpit must be executable", func(t *testing.T) {
		root := writeAssetTestBundle(t, withOfflineHTTPBinTestAssets(map[string]assetTestFile{
			offlineSourceImages: {content: "source\n"},
			offlineTarpitBinary: {content: "binary", mode: 0o644},
		}))
		_, err := InspectOfflineAssets(root, []string{"psf__requests-2317"})
		if err == nil || !strings.Contains(err.Error(), "not executable") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("profile requires requirements", func(t *testing.T) {
		root := writeAssetTestBundle(t, withOfflineHTTPBinTestAssets(map[string]assetTestFile{
			offlineSourceImages:          {content: "source\n"},
			offlineTarpitBinary:          {content: "binary", mode: 0o755},
			"requests-2931/wheels/a.whl": {content: "wheel"},
		}))
		_, err := InspectOfflineAssets(root, []string{"psf__requests-2931"})
		if err == nil || !strings.Contains(err.Error(), "has no requests-2931/requirements.txt") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("profile requires wheel", func(t *testing.T) {
		root := writeAssetTestBundle(t, withOfflineHTTPBinTestAssets(map[string]assetTestFile{
			offlineSourceImages:              {content: "source\n"},
			offlineTarpitBinary:              {content: "binary", mode: 0o755},
			"requests-2931/requirements.txt": {content: "requests==2.4.3\n"},
		}))
		_, err := InspectOfflineAssets(root, []string{"psf__requests-2931"})
		if err == nil || !strings.Contains(err.Error(), "wheelhouse for requests-2931 is empty") {
			t.Fatalf("error = %v", err)
		}
	})
}

func withOfflineHTTPBinTestAssets(files map[string]assetTestFile) map[string]assetTestFile {
	files[offlineHTTPBinCAAsset] = assetTestFile{content: "test CA"}
	files[offlineHTTPBinCertAsset] = assetTestFile{content: "test certificate"}
	files[offlineHTTPBinKeyAsset] = assetTestFile{content: "test key", mode: 0o600}
	return files
}

type assetTestFile struct {
	content string
	mode    os.FileMode
}

func writeAssetTestBundle(t *testing.T, files map[string]assetTestFile) string {
	t.Helper()
	root := t.TempDir()
	paths := make([]string, 0, len(files))
	for relative, file := range files {
		paths = append(paths, relative)
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		mode := file.mode
		if mode == 0 {
			mode = 0o644
		}
		if err := os.WriteFile(path, []byte(file.content), mode); err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(paths)
	var manifest strings.Builder
	for _, relative := range paths {
		manifest.WriteString(assetTestManifest(t, root, relative))
	}
	if err := os.WriteFile(filepath.Join(root, offlineAssetManifest), []byte(manifest.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func assetTestManifest(t *testing.T, root, relative string) string {
	t.Helper()
	digest, err := regularFileSHA256(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	return digest + "  " + relative + "\n"
}
