//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package embeddingcache

import (
	"sync/atomic"
	"time"
)

// Metrics captures persistent cache activity for one SWE-Bench case.
type Metrics struct {
	Requests        int64 `json:"requests"`
	BatchRequests   int64 `json:"batch_requests"`
	Inputs          int64 `json:"inputs"`
	Hits            int64 `json:"hits"`
	Misses          int64 `json:"misses"`
	Writes          int64 `json:"writes"`
	Corruptions     int64 `json:"corruptions"`
	Errors          int64 `json:"errors"`
	BytesRead       int64 `json:"bytes_read"`
	BytesWritten    int64 `json:"bytes_written"`
	ReadDurationMS  int64 `json:"read_duration_ms"`
	WriteDurationMS int64 `json:"write_duration_ms"`
}

// Add merges another snapshot into this aggregate.
func (m *Metrics) Add(other Metrics) {
	m.Requests += other.Requests
	m.BatchRequests += other.BatchRequests
	m.Inputs += other.Inputs
	m.Hits += other.Hits
	m.Misses += other.Misses
	m.Writes += other.Writes
	m.Corruptions += other.Corruptions
	m.Errors += other.Errors
	m.BytesRead += other.BytesRead
	m.BytesWritten += other.BytesWritten
	m.ReadDurationMS += other.ReadDurationMS
	m.WriteDurationMS += other.WriteDurationMS
}

type metricCounters struct {
	requests           atomic.Int64
	batchRequests      atomic.Int64
	inputs             atomic.Int64
	hits               atomic.Int64
	misses             atomic.Int64
	writes             atomic.Int64
	corruptions        atomic.Int64
	errors             atomic.Int64
	bytesRead          atomic.Int64
	bytesWritten       atomic.Int64
	readDurationNanos  atomic.Int64
	writeDurationNanos atomic.Int64
}

func (m *metricCounters) snapshot() Metrics {
	return Metrics{
		Requests:        m.requests.Load(),
		BatchRequests:   m.batchRequests.Load(),
		Inputs:          m.inputs.Load(),
		Hits:            m.hits.Load(),
		Misses:          m.misses.Load(),
		Writes:          m.writes.Load(),
		Corruptions:     m.corruptions.Load(),
		Errors:          m.errors.Load(),
		BytesRead:       m.bytesRead.Load(),
		BytesWritten:    m.bytesWritten.Load(),
		ReadDurationMS:  time.Duration(m.readDurationNanos.Load()).Milliseconds(),
		WriteDurationMS: time.Duration(m.writeDurationNanos.Load()).Milliseconds(),
	}
}
