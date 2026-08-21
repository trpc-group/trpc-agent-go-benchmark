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
	"runtime/debug"
	"testing"
)

func TestValidateArtifactName(t *testing.T) {
	for _, value := range []string{"run-1", "codec_xml.01", "R2"} {
		if err := validateArtifactName("run id", value); err != nil {
			t.Fatalf("validate %q: %v", value, err)
		}
	}
	for _, value := range []string{"", ".", "..", "../run", "run/name", "run name"} {
		if err := validateArtifactName("run id", value); err == nil {
			t.Fatalf("accepted unsafe value %q", value)
		}
	}
}

func TestDependencyVersion(t *testing.T) {
	info := &debug.BuildInfo{Deps: []*debug.Module{
		{Path: "example.com/other", Version: "v1.0.0"},
		{Path: frameworkModulePath, Version: "v1.2.3"},
	}}
	if got := dependencyVersion(info, frameworkModulePath); got != "v1.2.3" {
		t.Fatalf("version = %q", got)
	}
	info.Deps[1].Replace = &debug.Module{Path: "../trpc-agent-go"}
	if got := dependencyVersion(info, frameworkModulePath); got != "local-replacement" {
		t.Fatalf("replacement = %q", got)
	}
}

func TestSelectedInstancesSHA256IsSortedAndNewlineDelimited(t *testing.T) {
	want := "a05fd83e68b9ff1fb91839ae9d71f07027701e6339ddb86fc233a4c167446151"
	for _, ids := range [][]string{
		{"repo__repo-1", "repo__repo-2"},
		{"repo__repo-2", "repo__repo-1"},
	} {
		got, err := selectedInstancesSHA256(ids)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("hash(%v) = %q, want %q", ids, got, want)
		}
	}
}

func TestSelectedInstancesSHA256RejectsInvalidLists(t *testing.T) {
	for _, ids := range [][]string{
		nil,
		{"repo__repo-1", "repo__repo-1"},
		{"../unsafe"},
	} {
		if _, err := selectedInstancesSHA256(ids); err == nil {
			t.Fatalf("accepted instance list %#v", ids)
		}
	}
}
