//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingcache

import "testing"

func TestMetricsAdd(t *testing.T) {
	metrics := Metrics{
		Requests: 1, BatchRequests: 2, Inputs: 3, Hits: 4, Misses: 5,
		Writes: 6, Corruptions: 7, Errors: 8, BytesRead: 9, BytesWritten: 10,
		ReadDurationMS: 11, WriteDurationMS: 12,
	}
	metrics.Add(Metrics{
		Requests: 10, BatchRequests: 20, Inputs: 30, Hits: 40, Misses: 50,
		Writes: 60, Corruptions: 70, Errors: 80, BytesRead: 90, BytesWritten: 100,
		ReadDurationMS: 110, WriteDurationMS: 120,
	})
	want := Metrics{
		Requests: 11, BatchRequests: 22, Inputs: 33, Hits: 44, Misses: 55,
		Writes: 66, Corruptions: 77, Errors: 88, BytesRead: 99, BytesWritten: 110,
		ReadDurationMS: 121, WriteDurationMS: 132,
	}
	if metrics != want {
		t.Fatalf("Metrics.Add() = %+v, want %+v", metrics, want)
	}
}
