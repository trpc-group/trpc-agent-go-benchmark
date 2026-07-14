//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package runner

import "testing"

func TestResolveBillingAgentName(t *testing.T) {
	got, err := resolveBillingAgentName("BenchSWE", "codec-json-e1", "e1")
	if err != nil {
		t.Fatal(err)
	}
	if got != "BenchSWE-codec-json-e1" {
		t.Fatalf("agent name = %q", got)
	}
	plain, err := resolveBillingAgentName("BenchSWE", "", "")
	if err != nil || plain != "BenchSWE" {
		t.Fatalf("plain agent name = %q, err = %v", plain, err)
	}
}

func TestResolveBillingAgentNameRejectsIncompleteOrUnsafeTags(t *testing.T) {
	for _, test := range []struct {
		tag        string
		experiment string
	}{
		{tag: "codec-json-e1"},
		{experiment: "e1"},
		{tag: "codec json", experiment: "e1"},
	} {
		if _, err := resolveBillingAgentName("BenchSWE", test.tag, test.experiment); err == nil {
			t.Fatalf("accepted tag=%q experiment=%q", test.tag, test.experiment)
		}
	}
}
