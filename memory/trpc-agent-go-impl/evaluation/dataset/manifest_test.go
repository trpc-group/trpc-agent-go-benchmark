//
// Tencent is pleased to support the open source community by making trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package dataset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestBuildLongMemEvalManifestIsIndependentOfDatasetOrder(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 10, "type-b": 8})
	shuffled := append([]*LongMemEvalInstance(nil), instances...)
	for left, right := 0, len(shuffled)-1; left < right; left, right = left+1, right-1 {
		shuffled[left], shuffled[right] = shuffled[right], shuffled[left]
	}
	selection := LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "stable-seed",
		QuestionTypes: []string{"type-a", "type-b"},
		TotalSize:     7,
	}
	first := mustBuildManifest(t, instances, selection)
	second := mustBuildManifest(t, shuffled, selection)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("manifests differ after dataset shuffle:\nfirst=%#v\nsecond=%#v", first, second)
	}
}

func TestWriteLongMemEvalManifestIsByteStableAcrossSeedsAndDatasetOrder(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 20, "type-b": 17})
	reordered := append([]*LongMemEvalInstance(nil), instances...)
	for left, right := 0, len(reordered)-1; left < right; left, right = left+1, right-1 {
		reordered[left], reordered[right] = reordered[right], reordered[left]
	}
	for i := 0; i < 32; i++ {
		selection := LongMemEvalManifestSelection{
			Method:        LongMemEvalManifestMethodStratifiedSHA256,
			Seed:          fmt.Sprintf("repeatability-seed-%02d", i),
			QuestionTypes: []string{"type-a", "type-b"},
			TotalSize:     13,
		}
		first := mustBuildManifest(t, instances, selection)
		second := mustBuildManifest(t, reordered, selection)
		firstBytes := mustWriteManifest(t, first, fmt.Sprintf("first-%02d.json", i))
		secondBytes := mustWriteManifest(t, second, fmt.Sprintf("second-%02d.json", i))
		if !bytes.Equal(firstBytes, secondBytes) {
			t.Fatalf("seed %q produced different bytes after dataset reorder", selection.Seed)
		}
	}
}

func TestBuildLongMemEvalManifestSeedChangesSelection(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 30})
	first := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "seed-one",
		QuestionTypes: []string{"type-a"},
		TotalSize:     5,
	})
	second := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "seed-two",
		QuestionTypes: []string{"type-a"},
		TotalSize:     5,
	})
	if reflect.DeepEqual(first.CaseIDs, second.CaseIDs) {
		t.Fatalf("different seeds selected the same ordered IDs: %v", first.CaseIDs)
	}
}

func TestBuildLongMemEvalManifestDoesNotSelectSourcePrefix(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 20})
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "not-source-prefix",
		QuestionTypes: []string{"type-a"},
		TotalSize:     5,
	})
	sourcePrefix := make([]string, 0, 5)
	for _, inst := range instances[:5] {
		sourcePrefix = append(sourcePrefix, inst.QuestionID)
	}
	if reflect.DeepEqual(manifest.CaseIDs, sourcePrefix) {
		t.Fatalf("seeded selection unexpectedly used source prefix %v", sourcePrefix)
	}
}

func TestAllocateLongMemEvalLargestRemainder(t *testing.T) {
	got, err := allocateLongMemEvalLargestRemainder(
		[]string{"type-c", "type-b", "type-a"},
		map[string]int{"type-a": 3, "type-b": 5, "type-c": 2},
		5,
	)
	if err != nil {
		t.Fatalf("allocateLongMemEvalLargestRemainder() error = %v", err)
	}
	want := map[string]int{"type-a": 2, "type-b": 2, "type-c": 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("quotas = %#v, want %#v", got, want)
	}
}

func TestBuildLongMemEvalManifestWithExplicitQuotas(t *testing.T) {
	manifest := mustBuildManifest(
		t,
		manifestTestInstances(map[string]int{"type-a": 5, "type-b": 6}),
		LongMemEvalManifestSelection{
			Method:        LongMemEvalManifestMethodStratifiedSHA256,
			Seed:          "quota-seed",
			QuestionTypes: []string{"type-a", "type-b"},
			Quotas:        map[string]int{"type-a": 2, "type-b": 4},
		},
	)
	if manifest.TotalSize != 6 {
		t.Fatalf("TotalSize = %d, want 6", manifest.TotalSize)
	}
	if !reflect.DeepEqual(manifest.Quotas, map[string]int{"type-a": 2, "type-b": 4}) {
		t.Fatalf("Quotas = %#v", manifest.Quotas)
	}
}

func TestBuildLongMemEvalManifestRejectsInvalidSelection(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 4, "type-b": 3})
	tests := []struct {
		name      string
		selection LongMemEvalManifestSelection
		want      string
	}{
		{
			name: "missing seed",
			selection: LongMemEvalManifestSelection{
				Method: LongMemEvalManifestMethodStratifiedSHA256, TotalSize: 2,
			},
			want: "seed is required",
		},
		{
			name: "seed edge whitespace",
			selection: LongMemEvalManifestSelection{
				Method: LongMemEvalManifestMethodStratifiedSHA256, Seed: " seed", TotalSize: 2,
			},
			want: "leading or trailing whitespace",
		},
		{
			name: "invalid UTF-8 seed",
			selection: LongMemEvalManifestSelection{
				Method:    LongMemEvalManifestMethodStratifiedSHA256,
				Seed:      string([]byte{0xff}),
				TotalSize: 2,
			},
			want: "valid UTF-8",
		},
		{
			name: "total and quotas",
			selection: LongMemEvalManifestSelection{
				Method:    LongMemEvalManifestMethodStratifiedSHA256,
				Seed:      "conflicting-allocation",
				TotalSize: 2,
				Quotas:    map[string]int{"type-a": 1, "type-b": 1},
			},
			want: "mutually exclusive",
		},
		{
			name: "quota shortage",
			selection: LongMemEvalManifestSelection{
				Method:        LongMemEvalManifestMethodStratifiedSHA256,
				Seed:          "shortage",
				QuestionTypes: []string{"type-a"},
				Quotas:        map[string]int{"type-a": 5},
			},
			want: "only 4 cases are available",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := BuildLongMemEvalManifest(instances, test.selection)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildLongMemEvalManifest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestBuildLongMemEvalManifestRejectsDuplicateDatasetID(t *testing.T) {
	instances := []*LongMemEvalInstance{
		{QuestionID: "duplicate", QuestionType: "type-a"},
		{QuestionID: "duplicate", QuestionType: "type-a"},
	}
	_, err := BuildLongMemEvalManifest(instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodFullCategory,
		QuestionTypes: []string{"type-a"},
	})
	if err == nil || !strings.Contains(err.Error(), "duplicate question_id") {
		t.Fatalf("BuildLongMemEvalManifest() error = %v, want duplicate question_id", err)
	}
}

func TestBuildLongMemEvalManifestFullCategoryIncludesEveryCase(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 3, "type-b": 2, "type-c": 4})
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodFullCategory,
		QuestionTypes: []string{"type-b", "type-a"},
	})
	want := []string{"type-b-00", "type-b-01", "type-a-00", "type-a-01", "type-a-02"}
	if !reflect.DeepEqual(manifest.CaseIDs, want) {
		t.Fatalf("CaseIDs = %v, want %v", manifest.CaseIDs, want)
	}
	if _, ok := manifest.Quotas["type-c"]; ok {
		t.Fatalf("full-category unexpectedly selected type-c: %#v", manifest.Quotas)
	}
}

func TestBuildLongMemEvalManifestFull70HasNoSamplingBias(t *testing.T) {
	instances := manifestTestInstances(map[string]int{
		"other":               9,
		"single-session-user": 70,
	})
	reordered := append([]*LongMemEvalInstance(nil), instances...)
	for left, right := 0, len(reordered)-1; left < right; left, right = left+1, right-1 {
		reordered[left], reordered[right] = reordered[right], reordered[left]
	}
	selection := LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodFullCategory,
		QuestionTypes: []string{"single-session-user"},
	}
	first := mustBuildManifest(t, instances, selection)
	second := mustBuildManifest(t, reordered, selection)
	if first.TotalSize != 70 || first.Quotas["single-session-user"] != 70 {
		t.Fatalf("Full-70 selection = total %d, quotas %#v", first.TotalSize, first.Quotas)
	}
	if !bytes.Equal(
		mustWriteManifest(t, first, "full-70-first.json"),
		mustWriteManifest(t, second, "full-70-second.json"),
	) {
		t.Fatal("Full-70 output changed with dataset order")
	}
}

func TestBuildAndVerifyLongMemEvalManifestSplit(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 10, "type-b": 10})
	selection := LongMemEvalManifestSplitSelection{
		Seed:          "split-seed",
		QuestionTypes: []string{"type-a", "type-b"},
		Dev:           LongMemEvalManifestAllocation{TotalSize: 7},
		Holdout:       LongMemEvalManifestAllocation{TotalSize: 6},
	}
	dev, holdout, err := BuildLongMemEvalManifestSplit(instances, selection)
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifestSplit() error = %v", err)
	}
	if err := VerifyLongMemEvalManifestSplit(instances, dev, holdout); err != nil {
		t.Fatalf("VerifyLongMemEvalManifestSplit() error = %v", err)
	}
	seen := make(map[string]struct{}, len(dev.CaseIDs))
	for _, id := range dev.CaseIDs {
		seen[id] = struct{}{}
	}
	for _, id := range holdout.CaseIDs {
		if _, ok := seen[id]; ok {
			t.Fatalf("dev and holdout overlap at %q", id)
		}
	}
}

func TestBuildLongMemEvalManifestSplitIsByteStableAcrossDatasetOrder(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 18, "type-b": 15})
	reordered := append([]*LongMemEvalInstance(nil), instances...)
	for left, right := 0, len(reordered)-1; left < right; left, right = left+1, right-1 {
		reordered[left], reordered[right] = reordered[right], reordered[left]
	}
	for i := 0; i < 16; i++ {
		selection := LongMemEvalManifestSplitSelection{
			Seed:          fmt.Sprintf("repeatable-split-%02d", i),
			QuestionTypes: []string{"type-a", "type-b"},
			Dev:           LongMemEvalManifestAllocation{TotalSize: 11},
			Holdout:       LongMemEvalManifestAllocation{TotalSize: 9},
		}
		firstDev, firstHoldout, err := BuildLongMemEvalManifestSplit(instances, selection)
		if err != nil {
			t.Fatalf("BuildLongMemEvalManifestSplit(first) error = %v", err)
		}
		secondDev, secondHoldout, err := BuildLongMemEvalManifestSplit(reordered, selection)
		if err != nil {
			t.Fatalf("BuildLongMemEvalManifestSplit(second) error = %v", err)
		}
		if !bytes.Equal(
			mustWriteManifest(t, firstDev, fmt.Sprintf("first-dev-%02d.json", i)),
			mustWriteManifest(t, secondDev, fmt.Sprintf("second-dev-%02d.json", i)),
		) {
			t.Fatalf("development bytes changed for seed %q", selection.Seed)
		}
		if !bytes.Equal(
			mustWriteManifest(t, firstHoldout, fmt.Sprintf("first-holdout-%02d.json", i)),
			mustWriteManifest(t, secondHoldout, fmt.Sprintf("second-holdout-%02d.json", i)),
		) {
			t.Fatalf("holdout bytes changed for seed %q", selection.Seed)
		}
		if err := VerifyLongMemEvalManifestSplit(instances, firstDev, firstHoldout); err != nil {
			t.Fatalf("VerifyLongMemEvalManifestSplit() error = %v", err)
		}
	}
}

func TestBuildLongMemEvalManifestSplitWithExplicitQuotas(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 8, "type-b": 8})
	dev, holdout, err := BuildLongMemEvalManifestSplit(instances, LongMemEvalManifestSplitSelection{
		Seed:          "explicit-split-seed",
		QuestionTypes: []string{"type-a", "type-b"},
		Dev: LongMemEvalManifestAllocation{
			Quotas: map[string]int{"type-a": 3, "type-b": 2},
		},
		Holdout: LongMemEvalManifestAllocation{
			Quotas: map[string]int{"type-a": 1, "type-b": 4},
		},
	})
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifestSplit() error = %v", err)
	}
	if !reflect.DeepEqual(dev.Quotas, map[string]int{"type-a": 3, "type-b": 2}) {
		t.Fatalf("dev.Quotas = %#v", dev.Quotas)
	}
	if !reflect.DeepEqual(holdout.Quotas, map[string]int{"type-a": 1, "type-b": 4}) {
		t.Fatalf("holdout.Quotas = %#v", holdout.Quotas)
	}
	if err := VerifyLongMemEvalManifestSplit(instances, dev, holdout); err != nil {
		t.Fatalf("VerifyLongMemEvalManifestSplit() error = %v", err)
	}
	if !reflect.DeepEqual(holdout.RankOffsets, dev.Quotas) {
		t.Fatalf("holdout.RankOffsets = %#v, want %#v", holdout.RankOffsets, dev.Quotas)
	}
}

func TestBuildLongMemEvalManifestSplitRejectsCombinedQuotaShortage(t *testing.T) {
	_, _, err := BuildLongMemEvalManifestSplit(
		manifestTestInstances(map[string]int{"type-a": 5}),
		LongMemEvalManifestSplitSelection{
			Seed:          "split-shortage",
			QuestionTypes: []string{"type-a"},
			Dev: LongMemEvalManifestAllocation{
				Quotas: map[string]int{"type-a": 3},
			},
			Holdout: LongMemEvalManifestAllocation{
				Quotas: map[string]int{"type-a": 3},
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "only 2 cases are available") {
		t.Fatalf("BuildLongMemEvalManifestSplit() error = %v, want quota shortage", err)
	}
}

func TestVerifyLongMemEvalHoldoutManifestRejectsWrongSeededOffset(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 12})
	_, holdout, err := BuildLongMemEvalManifestSplit(instances, LongMemEvalManifestSplitSelection{
		Seed:          "standalone-holdout",
		QuestionTypes: []string{"type-a"},
		Dev:           LongMemEvalManifestAllocation{TotalSize: 4},
		Holdout:       LongMemEvalManifestAllocation{TotalSize: 4},
	})
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifestSplit() error = %v", err)
	}
	selected := make(map[string]struct{}, len(holdout.CaseIDs))
	for _, id := range holdout.CaseIDs {
		selected[id] = struct{}{}
	}
	for _, inst := range instances {
		if _, ok := selected[inst.QuestionID]; ok {
			continue
		}
		holdout.CaseIDs[0] = inst.QuestionID
		holdout.Cases[0].CaseID = inst.QuestionID
		break
	}
	refreshManifestDigest(t, holdout)
	err = VerifyLongMemEvalManifest(instances, holdout)
	if err == nil || !strings.Contains(err.Error(), "case order") {
		t.Fatalf("VerifyLongMemEvalManifest() error = %v, want seeded offset mismatch", err)
	}
}

func TestVerifyLongMemEvalManifestSplitRejectsWrongRankOffsets(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 12})
	dev, holdout, err := BuildLongMemEvalManifestSplit(instances, LongMemEvalManifestSplitSelection{
		Seed:          "offset-mismatch",
		QuestionTypes: []string{"type-a"},
		Dev:           LongMemEvalManifestAllocation{TotalSize: 4},
		Holdout:       LongMemEvalManifestAllocation{TotalSize: 4},
	})
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifestSplit() error = %v", err)
	}
	holdout.RankOffsets["type-a"]++
	refreshManifestDigest(t, holdout)
	err = VerifyLongMemEvalManifestSplit(instances, dev, holdout)
	if err == nil || !strings.Contains(err.Error(), "rank offsets") {
		t.Fatalf("VerifyLongMemEvalManifestSplit() error = %v, want rank offset mismatch", err)
	}
}

func TestVerifyLongMemEvalManifestSplitRejectsOverlap(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 12})
	dev, holdout, err := BuildLongMemEvalManifestSplit(instances, LongMemEvalManifestSplitSelection{
		Seed:          "split-seed",
		QuestionTypes: []string{"type-a"},
		Dev:           LongMemEvalManifestAllocation{TotalSize: 4},
		Holdout:       LongMemEvalManifestAllocation{TotalSize: 4},
	})
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifestSplit() error = %v", err)
	}
	holdout.CaseIDs[0] = dev.CaseIDs[0]
	holdout.Cases[0].CaseID = dev.CaseIDs[0]
	refreshManifestDigest(t, holdout)
	err = VerifyLongMemEvalManifestSplit(instances, dev, holdout)
	if err == nil || !strings.Contains(err.Error(), "overlap") {
		t.Fatalf("VerifyLongMemEvalManifestSplit() error = %v, want overlap", err)
	}
}

func TestVerifyLongMemEvalManifestRejectsWrongQuestionType(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 4, "type-b": 4})
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "type-seed",
		QuestionTypes: []string{"type-a", "type-b"},
		Quotas:        map[string]int{"type-a": 2, "type-b": 2},
	})
	originalType := manifest.Cases[0].QuestionType
	wrongType := "type-a"
	if originalType == wrongType {
		wrongType = "type-b"
	}
	manifest.Cases[0].QuestionType = wrongType
	refreshManifestDigest(t, manifest)
	err := VerifyLongMemEvalManifest(instances, manifest)
	if err == nil || !strings.Contains(err.Error(), "dataset has") {
		t.Fatalf("VerifyLongMemEvalManifest() error = %v, want question type mismatch", err)
	}
}

func TestVerifyLongMemEvalManifestRejectsUnknownID(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 4})
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "unknown-seed",
		QuestionTypes: []string{"type-a"},
		TotalSize:     2,
	})
	manifest.CaseIDs[0] = "missing"
	manifest.Cases[0].CaseID = "missing"
	refreshManifestDigest(t, manifest)
	err := VerifyLongMemEvalManifest(instances, manifest)
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("VerifyLongMemEvalManifest() error = %v, want unknown ID", err)
	}
}

func TestVerifyLongMemEvalManifestRejectsDatasetDigestMismatch(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 4})
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "digest-seed",
		QuestionTypes: []string{"type-a"},
		TotalSize:     2,
	})
	manifest.DatasetDigest = "sha256:" + strings.Repeat("0", 64)
	refreshManifestDigest(t, manifest)
	err := VerifyLongMemEvalManifest(instances, manifest)
	if err == nil || !strings.Contains(err.Error(), "dataset digest mismatch") {
		t.Fatalf("VerifyLongMemEvalManifest() error = %v, want dataset digest mismatch", err)
	}
}

func TestParseLongMemEvalManifestRejectsMalformedManifests(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "empty", data: `{}`, want: "no case_ids"},
		{name: "duplicate", data: `{"case_ids":["a","a"]}`, want: "duplicate case_id"},
		{name: "case IDs only", data: `{"case_ids":["a"]}`, want: "schema_version 0"},
		{
			name: "partial rich schema",
			data: `{"schema_version":1,"method":"stratified-sha256","case_ids":["a"]}`,
			want: "seed is required",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseLongMemEvalManifest([]byte(test.data))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ParseLongMemEvalManifest() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestParseLongMemEvalManifestRejectsManifestDigestMismatch(t *testing.T) {
	manifest := mustBuildManifest(
		t,
		manifestTestInstances(map[string]int{"type-a": 4}),
		LongMemEvalManifestSelection{
			Method:        LongMemEvalManifestMethodStratifiedSHA256,
			Seed:          "manifest-digest-seed",
			QuestionTypes: []string{"type-a"},
			TotalSize:     2,
		},
	)
	manifest.ManifestDigest = "sha256:" + strings.Repeat("f", 64)
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	_, err = ParseLongMemEvalManifest(data)
	if err == nil || !strings.Contains(err.Error(), "manifest digest mismatch") {
		t.Fatalf("ParseLongMemEvalManifest() error = %v, want manifest digest mismatch", err)
	}
}

func TestLongMemEvalDigestsAreStable(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 5, "type-b": 5})
	reordered := append([]*LongMemEvalInstance(nil), instances...)
	reordered[0], reordered[len(reordered)-1] = reordered[len(reordered)-1], reordered[0]
	firstDatasetDigest, err := LongMemEvalDatasetDigest(instances)
	if err != nil {
		t.Fatalf("LongMemEvalDatasetDigest() error = %v", err)
	}
	secondDatasetDigest, err := LongMemEvalDatasetDigest(reordered)
	if err != nil {
		t.Fatalf("LongMemEvalDatasetDigest(reordered) error = %v", err)
	}
	if firstDatasetDigest != secondDatasetDigest {
		t.Fatalf("dataset digests differ: %s != %s", firstDatasetDigest, secondDatasetDigest)
	}
	selection := LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodStratifiedSHA256,
		Seed:          "stable-digest-seed",
		QuestionTypes: []string{"type-a", "type-b"},
		TotalSize:     4,
	}
	first := mustBuildManifest(t, instances, selection)
	second := mustBuildManifest(t, reordered, selection)
	if first.ManifestDigest != second.ManifestDigest {
		t.Fatalf("manifest digests differ: %s != %s", first.ManifestDigest, second.ManifestDigest)
	}
}

func TestVerifyLongMemEvalManifestAcceptsQuestionTypePrefilter(t *testing.T) {
	instances := manifestTestInstances(map[string]int{"type-a": 5, "type-b": 5})
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodFullCategory,
		QuestionTypes: []string{"type-a"},
	})
	filtered := FilterLongMemEval(instances, []string{"type-a"})
	if err := VerifyLongMemEvalManifest(filtered, manifest); err != nil {
		t.Fatalf("VerifyLongMemEvalManifest(prefiltered) error = %v", err)
	}
}

func TestFilterLongMemEvalByManifestRejectsMissingID(t *testing.T) {
	instances := []*LongMemEvalInstance{
		{QuestionID: "a", QuestionType: "type-a"},
		{QuestionID: "missing", QuestionType: "type-a"},
	}
	manifest := mustBuildManifest(t, instances, LongMemEvalManifestSelection{
		Method:        LongMemEvalManifestMethodFullCategory,
		QuestionTypes: []string{"type-a"},
	})
	_, err := FilterLongMemEvalByManifest(
		instances[:1],
		manifest,
	)
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("FilterLongMemEvalByManifest() error = %v, want missing ID", err)
	}
}

func manifestTestInstances(counts map[string]int) []*LongMemEvalInstance {
	questionTypes := make([]string, 0, len(counts))
	for questionType := range counts {
		questionTypes = append(questionTypes, questionType)
	}
	sort.Strings(questionTypes)
	// Deliberately use reverse lexical type order to catch source-order coupling.
	for left, right := 0, len(questionTypes)-1; left < right; left, right = left+1, right-1 {
		questionTypes[left], questionTypes[right] = questionTypes[right], questionTypes[left]
	}
	instances := make([]*LongMemEvalInstance, 0)
	for _, questionType := range questionTypes {
		for i := counts[questionType] - 1; i >= 0; i-- {
			instances = append(instances, &LongMemEvalInstance{
				QuestionID:   questionType + "-" + twoDigits(i),
				QuestionType: questionType,
				Question:     "Question " + questionType,
				Answer:       "Answer " + questionType,
			})
		}
	}
	return instances
}

func twoDigits(value int) string {
	return fmt.Sprintf("%02d", value)
}

func mustBuildManifest(
	t *testing.T,
	instances []*LongMemEvalInstance,
	selection LongMemEvalManifestSelection,
) *LongMemEvalManifest {
	t.Helper()
	manifest, err := BuildLongMemEvalManifest(instances, selection)
	if err != nil {
		t.Fatalf("BuildLongMemEvalManifest() error = %v", err)
	}
	return manifest
}

func mustWriteManifest(t *testing.T, manifest *LongMemEvalManifest, name string) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := WriteLongMemEvalManifest(path, manifest); err != nil {
		t.Fatalf("WriteLongMemEvalManifest() error = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile() error = %v", err)
	}
	return data
}

func refreshManifestDigest(t *testing.T, manifest *LongMemEvalManifest) {
	t.Helper()
	digest, err := LongMemEvalManifestDigest(manifest)
	if err != nil {
		t.Fatalf("LongMemEvalManifestDigest() error = %v", err)
	}
	manifest.ManifestDigest = digest
}
