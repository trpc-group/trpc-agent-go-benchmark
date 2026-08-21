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
	"hash/crc32"
	"math"
	"reflect"
	"testing"
)

func TestVectorCodecUsesLosslessFloat32WhenPossible(t *testing.T) {
	vector := []float64{
		float64(float32(0.125)),
		float64(float32(-2.5)),
		math.Copysign(0, -1),
	}
	encoded, err := encodeVector(vector)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Codec != vectorCodecFloat32 {
		t.Fatalf("codec = %d, want float32", encoded.Codec)
	}
	decoded, err := decodeVector(
		encoded.Dimensions,
		encoded.Codec,
		encoded.Data,
		encoded.Checksum,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !sameFloatBits(decoded, vector) {
		t.Fatalf("decoded = %#v, want %#v", decoded, vector)
	}
}

func TestVectorCodecFallsBackToFloat64AndRejectsCorruption(t *testing.T) {
	vector := []float64{math.Pi, math.SmallestNonzeroFloat64}
	encoded, err := encodeVector(vector)
	if err != nil {
		t.Fatal(err)
	}
	if encoded.Codec != vectorCodecFloat64 {
		t.Fatalf("codec = %d, want float64", encoded.Codec)
	}
	decoded, err := decodeVector(
		encoded.Dimensions,
		encoded.Codec,
		encoded.Data,
		encoded.Checksum,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, vector) {
		t.Fatalf("decoded = %#v, want %#v", decoded, vector)
	}

	corrupt := append([]byte(nil), encoded.Data...)
	corrupt[0] ^= 0xff
	if _, err := decodeVector(
		encoded.Dimensions,
		encoded.Codec,
		corrupt,
		encoded.Checksum,
	); err == nil {
		t.Fatal("corrupt vector decoded successfully")
	}
	if _, err := encodeVector([]float64{math.NaN()}); err == nil {
		t.Fatal("NaN vector encoded successfully")
	}
}

func TestVectorCodecRejectsInvalidMetadata(t *testing.T) {
	encoded, err := encodeVector([]float64{1, 2})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		dimensions int
		codec      int
		data       []byte
		checksum   uint32
	}{
		{
			name: "dimensions", dimensions: 0, codec: encoded.Codec,
			data: encoded.Data, checksum: encoded.Checksum,
		},
		{
			name: "codec", dimensions: encoded.Dimensions, codec: 99,
			data: encoded.Data, checksum: encoded.Checksum,
		},
		{
			name: "length", dimensions: encoded.Dimensions, codec: encoded.Codec,
			data: encoded.Data[:len(encoded.Data)-1],
			checksum: crc32.Checksum(
				encoded.Data[:len(encoded.Data)-1],
				vectorChecksumTable,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := decodeVector(
				test.dimensions,
				test.codec,
				test.data,
				test.checksum,
			); err == nil {
				t.Fatal("decodeVector() accepted invalid metadata")
			}
		})
	}
	if _, err := encodeVector(nil); err == nil {
		t.Fatal("encodeVector() accepted empty vector")
	}
}

func sameFloatBits(left, right []float64) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if math.Float64bits(left[index]) != math.Float64bits(right[index]) {
			return false
		}
	}
	return true
}
