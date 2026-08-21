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
	"strings"
	"testing"
)

func TestSafeEnvRedactsGenericCredentialNames(t *testing.T) {
	got := safeEnv([]string{
		"HTTP_HEADER:X-Api-Key=secret-value",
		"PASSWORD=secret-password",
		"MODEL_PROVIDER=openai-compatible",
	})
	joined := strings.Join(got, "\n")
	if strings.Contains(joined, "secret-value") || strings.Contains(joined, "secret-password") {
		t.Fatalf("safeEnv() leaked credentials: %s", joined)
	}
	if !strings.Contains(joined, "MODEL_PROVIDER=openai-compatible") {
		t.Fatalf("safeEnv() redacted a non-secret provider label: %s", joined)
	}
}

func TestRedactJSONBytesRedactsCredentialValueEverywhere(t *testing.T) {
	input := []byte(`{
  "x-api-key":"secret-value",
  "nested":{"message":"request used secret-value"}
}`)
	got := string(redactJSONBytes(input))
	if strings.Contains(got, "secret-value") {
		t.Fatalf("redactJSONBytes() leaked a credential: %s", got)
	}
}
