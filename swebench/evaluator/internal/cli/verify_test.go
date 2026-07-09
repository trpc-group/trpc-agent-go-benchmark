//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import "testing"

func TestHarnessPredictionsArgKeepsGoldLiteral(t *testing.T) {
	if got := harnessPredictionsArg("gold"); got != "gold" {
		t.Fatalf("harnessPredictionsArg(\"gold\") = %q, want gold", got)
	}
}

func TestResolveVerifierMode(t *testing.T) {
	tests := []struct {
		name           string
		mode           string
		compatPatch    bool
		modeSet        bool
		compatPatchSet bool
		want           string
		wantErr        bool
	}{
		{name: "default", mode: "", compatPatch: true, want: verifierModeCalibrated},
		{name: "explicit upstream", mode: verifierModeUpstream, compatPatch: true, modeSet: true, want: verifierModeUpstream},
		{name: "legacy compat false", mode: verifierModeCalibrated, compatPatch: false, compatPatchSet: true, want: verifierModeUpstream},
		{name: "conflict", mode: verifierModeCalibrated, compatPatch: false, modeSet: true, compatPatchSet: true, wantErr: true},
		{name: "bad mode", mode: "clean", compatPatch: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := resolveVerifierMode(tt.mode, tt.compatPatch, tt.modeSet, tt.compatPatchSet)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveVerifierMode() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVerifierMode() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("resolveVerifierMode() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVerifyEnvironment(t *testing.T) {
	env := verifyEnvironment("unix:///var/run/docker.sock", "/tmp/hf", "http://host.docker.internal:18080", "/tmp/ca.crt", verifierModeCalibrated)
	want := []string{
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"HF_HOME=/tmp/hf",
		"SWEBENCH_HTTPBIN_URL=http://host.docker.internal:18080",
		"SWEBENCH_HTTPBIN_CA_BUNDLE=/tmp/ca.crt",
	}
	if len(env) != len(want) {
		t.Fatalf("verifyEnvironment() len = %d, want %d: %#v", len(env), len(want), env)
	}
	for i := range want {
		if env[i] != want[i] {
			t.Fatalf("verifyEnvironment()[%d] = %q, want %q", i, env[i], want[i])
		}
	}

	upstream := verifyEnvironment("sock", "", "http://ignored", "/tmp/ignored", verifierModeUpstream)
	if len(upstream) != 1 || upstream[0] != "DOCKER_HOST=sock" {
		t.Fatalf("upstream verifyEnvironment() = %#v", upstream)
	}
}
