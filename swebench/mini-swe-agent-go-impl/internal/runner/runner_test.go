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
	"reflect"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/modelconfig"
)

func TestSelectCasesValidatesAndSortsFilteredCases(t *testing.T) {
	cases := []contract.Case{{InstanceID: "repo__repo-2"}, {InstanceID: "repo__repo-1"}}
	selected, err := selectCases(cases, `repo__repo-[12]`)
	if err != nil {
		t.Fatal(err)
	}
	want := []contract.Case{{InstanceID: "repo__repo-1"}, {InstanceID: "repo__repo-2"}}
	if !reflect.DeepEqual(selected, want) {
		t.Fatalf("selected = %#v, want %#v", selected, want)
	}
	if _, err := selectCases([]contract.Case{{InstanceID: "../unsafe"}}, ""); err == nil {
		t.Fatal("accepted unsafe instance id")
	}
}

func TestPublicModelConfigOmitsEndpointAndCredentials(t *testing.T) {
	cfg := modelconfig.EnvConfig{
		"MODEL_NAME":                    "example-model",
		"MODEL_TEMPERATURE":             "0",
		"OPENAI_BASE_URL":               "https://internal.invalid/v1",
		"OPENAI_API_KEY":                "secret",
		"HTTP_HEADER:X-Test-Request-ID": "request-id",
	}
	got := publicModelConfig(cfg)
	want := map[string]string{"MODEL_NAME": "example-model", "MODEL_TEMPERATURE": "0"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("public config = %#v, want %#v", got, want)
	}
}
