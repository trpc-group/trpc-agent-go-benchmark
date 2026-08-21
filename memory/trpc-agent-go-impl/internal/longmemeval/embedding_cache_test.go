//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent. All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"bytes"
	"encoding/binary"
	"reflect"
	"strings"
	"testing"
)

func TestEmbeddingCacheFileRoundTrip(t *testing.T) {
	wantEmbedding := []float64{1.25, -2.5}
	wantUsage := map[string]any{"provider": "test"}
	data, err := encodeEmbeddingCacheFile(wantEmbedding, wantUsage)
	if err != nil {
		t.Fatalf("encodeEmbeddingCacheFile() error = %v", err)
	}
	gotEmbedding, gotUsage, err := decodeEmbeddingCacheFile(data)
	if err != nil {
		t.Fatalf("decodeEmbeddingCacheFile() error = %v", err)
	}
	if !reflect.DeepEqual(gotEmbedding, wantEmbedding) {
		t.Fatalf("embedding = %v, want %v", gotEmbedding, wantEmbedding)
	}
	if !reflect.DeepEqual(gotUsage, wantUsage) {
		t.Fatalf("usage = %v, want %v", gotUsage, wantUsage)
	}
}

func TestDecodeEmbeddingCacheFileRejectsInvalidLength(t *testing.T) {
	valid, err := encodeEmbeddingCacheFile([]float64{1}, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string][]byte{
		"oversized payload": embeddingCacheHeader(t, ^uint32(0), 0),
		"oversized vector":  embeddingCacheHeader(t, 0, ^uint32(0)),
		"trailing bytes":    append(valid, 0),
	}
	for name, data := range tests {
		t.Run(name, func(t *testing.T) {
			_, _, err := decodeEmbeddingCacheFile(data)
			if err == nil || !strings.Contains(err.Error(), "file length") {
				t.Fatalf("decodeEmbeddingCacheFile() error = %v", err)
			}
		})
	}
}

func embeddingCacheHeader(t *testing.T, payloadLen, embeddingLen uint32) []byte {
	t.Helper()
	var b bytes.Buffer
	b.WriteString(lmeEmbeddingCacheMagic)
	b.WriteByte('\n')
	if err := binary.Write(&b, binary.LittleEndian, payloadLen); err != nil {
		t.Fatal(err)
	}
	if err := binary.Write(&b, binary.LittleEndian, embeddingLen); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
