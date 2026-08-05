//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package modelconfig

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadMiniSWEAgentYAMLGenericHeaders(t *testing.T) {
	path := filepath.Join(t.TempDir(), "model.yaml")
	content := []byte(`model:
  model_name: openai/example-model
  model_kwargs:
    api_base: https://api.example.com/v1
    api_key: sk-example
    extra_headers:
      Authorization: Bearer example
      X-Tenant: public-example
`)
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg["MODEL_NAME"], "example-model"; got != want {
		t.Fatalf("MODEL_NAME = %q, want %q", got, want)
	}
	wantHeaders := map[string]string{
		"Authorization": "Bearer example",
		"X-Tenant":      "public-example",
	}
	if got := HTTPHeaders(cfg); !reflect.DeepEqual(got, wantHeaders) {
		t.Fatalf("HTTPHeaders() = %#v, want %#v", got, wantHeaders)
	}
}

func TestRedactSecretsRedactsHeaderCredentials(t *testing.T) {
	cfg := EnvConfig{
		HTTPHeaderPrefix + "Authorization": "Bearer example",
		HTTPHeaderPrefix + "X-Tenant":      "public-example",
	}
	got := RedactSecrets(cfg)
	if got[HTTPHeaderPrefix+"Authorization"] != "REDACTED" {
		t.Fatalf("authorization header was not redacted: %#v", got)
	}
	if got[HTTPHeaderPrefix+"X-Tenant"] != "public-example" {
		t.Fatalf("non-secret header changed: %#v", got)
	}
}
