//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
)

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
	managed := &managedHTTPBin{Info: managedHTTPBinInfo{
		PublicHost: managedHTTPBinHost,
		CABundle:   "/tmp/ca.crt",
	}}
	env := verifyEnvironment("unix:///var/run/docker.sock", "/tmp/hf", managed)
	want := []string{
		"DOCKER_HOST=unix:///var/run/docker.sock",
		"SWEBENCH_HTTPBIN_URL=http://httpbin.org",
		"SWEBENCH_HTTPBIN_CA_BUNDLE=/tmp/ca.crt",
		"HF_HOME=/tmp/hf",
	}
	if len(env) != len(want) {
		t.Fatalf("verifyEnvironment() len = %d, want %d: %#v", len(env), len(want), env)
	}
	for i := range want {
		if env[i] != want[i] {
			t.Fatalf("verifyEnvironment()[%d] = %q, want %q", i, env[i], want[i])
		}
	}

	upstream := verifyEnvironment("sock", "", nil)
	wantUpstream := []string{"DOCKER_HOST=sock", "SWEBENCH_HTTPBIN_URL=", "SWEBENCH_HTTPBIN_CA_BUNDLE="}
	if len(upstream) != len(wantUpstream) {
		t.Fatalf("upstream verifyEnvironment() = %#v", upstream)
	}
	for i := range wantUpstream {
		if upstream[i] != wantUpstream[i] {
			t.Fatalf("upstream verifyEnvironment()[%d] = %q, want %q", i, upstream[i], wantUpstream[i])
		}
	}
}

func TestEnsureManagedHTTPBinCerts(t *testing.T) {
	dir := t.TempDir()
	certs, err := ensureManagedHTTPBinCerts(dir)
	if err != nil {
		t.Fatalf("ensureManagedHTTPBinCerts() error = %v", err)
	}
	for _, path := range []string{certs.CACert, certs.CAKey, certs.ServerCert, certs.ServerKey, certs.CABundle} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected cert file %s: %v", path, err)
		}
	}
	data, err := os.ReadFile(certs.ServerCert)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode(data)
	if block == nil {
		t.Fatal("server cert PEM block is nil")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	if err := cert.VerifyHostname(managedHTTPBinHost); err != nil {
		t.Fatalf("server cert does not verify for %s: %v", managedHTTPBinHost, err)
	}

	again, err := ensureManagedHTTPBinCerts(filepath.Clean(dir))
	if err != nil {
		t.Fatalf("ensureManagedHTTPBinCerts() second call error = %v", err)
	}
	if again.CABundle != certs.CABundle {
		t.Fatalf("CABundle = %q, want %q", again.CABundle, certs.CABundle)
	}
}
