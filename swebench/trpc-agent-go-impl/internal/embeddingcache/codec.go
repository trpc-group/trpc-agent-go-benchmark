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
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"math"
)

const (
	vectorCodecFloat32 = 1
	vectorCodecFloat64 = 2
)

var vectorChecksumTable = crc32.MakeTable(crc32.Castagnoli)

type encodedVector struct {
	Dimensions int
	Codec      int
	Data       []byte
	Checksum   uint32
}

func encodeVector(vector []float64) (encodedVector, error) {
	if len(vector) == 0 {
		return encodedVector{}, fmt.Errorf("embedding vector is empty")
	}
	float32Exact := true
	for index, value := range vector {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return encodedVector{}, fmt.Errorf("embedding vector contains non-finite value at index %d", index)
		}
		roundTrip := float64(float32(value))
		if math.Float64bits(roundTrip) != math.Float64bits(value) {
			float32Exact = false
		}
	}

	codec := vectorCodecFloat32
	elementBytes := 4
	if !float32Exact {
		codec = vectorCodecFloat64
		elementBytes = 8
	}
	data := make([]byte, len(vector)*elementBytes)
	for index, value := range vector {
		offset := index * elementBytes
		if codec == vectorCodecFloat32 {
			binary.LittleEndian.PutUint32(data[offset:offset+elementBytes], math.Float32bits(float32(value)))
			continue
		}
		binary.LittleEndian.PutUint64(data[offset:offset+elementBytes], math.Float64bits(value))
	}
	return encodedVector{
		Dimensions: len(vector),
		Codec:      codec,
		Data:       data,
		Checksum:   crc32.Checksum(data, vectorChecksumTable),
	}, nil
}

func decodeVector(dimensions, codec int, data []byte, checksum uint32) ([]float64, error) {
	if dimensions <= 0 {
		return nil, fmt.Errorf("cached embedding dimensions must be positive")
	}
	if crc32.Checksum(data, vectorChecksumTable) != checksum {
		return nil, fmt.Errorf("cached embedding checksum mismatch")
	}

	elementBytes := 0
	switch codec {
	case vectorCodecFloat32:
		elementBytes = 4
	case vectorCodecFloat64:
		elementBytes = 8
	default:
		return nil, fmt.Errorf("unsupported cached embedding codec %d", codec)
	}
	if len(data) != dimensions*elementBytes {
		return nil, fmt.Errorf(
			"cached embedding byte length mismatch: dimensions=%d codec=%d bytes=%d",
			dimensions,
			codec,
			len(data),
		)
	}

	vector := make([]float64, dimensions)
	for index := range vector {
		offset := index * elementBytes
		if codec == vectorCodecFloat32 {
			vector[index] = float64(math.Float32frombits(binary.LittleEndian.Uint32(data[offset : offset+elementBytes])))
		} else {
			vector[index] = math.Float64frombits(binary.LittleEndian.Uint64(data[offset : offset+elementBytes]))
		}
		if math.IsNaN(vector[index]) || math.IsInf(vector[index], 0) {
			return nil, fmt.Errorf("cached embedding contains non-finite value at index %d", index)
		}
	}
	return vector, nil
}
