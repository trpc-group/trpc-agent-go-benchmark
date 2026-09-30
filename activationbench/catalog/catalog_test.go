//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package catalog

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	trpcskill "trpc.group/trpc-go/trpc-agent-go/skill"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestDefaultCatalogShapeAndStableOrdering(t *testing.T) {
	c := Default()
	stats := c.Stats()
	if stats.Skills != 8 || stats.ToolSets != 8 || stats.Tools != 64 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if stats.DistractorTools != 16 {
		t.Fatalf("want 16 distractors, got %+v", stats)
	}
	for _, tool := range c.Tools() {
		if tool.Distractor && !tool.ReadOnly {
			t.Fatalf("default distractor is not read-only: %#v", tool)
		}
	}
	first := c.Manifest()
	second := c.Manifest()
	if len(first.Skills) != len(second.Skills) || len(first.Tools) != len(second.Tools) {
		t.Fatalf("manifest sizes changed")
	}
	for i := range first.Tools {
		if first.Tools[i].Name != second.Tools[i].Name {
			t.Fatalf("tool ordering changed at %d", i)
		}
	}
	for _, skill := range c.Skills() {
		if len(c.ToolsForSkill(skill.ID)) != 8 {
			t.Fatalf("skill %s does not have eight tools", skill.ID)
		}
	}
}

func TestModelFacingDistractorsDoNotLeakBenchmarkLabels(t *testing.T) {
	markers := []string{
		"benchmark", "fixture", "synthetic", "deterministic", "in-memory", "local",
		"distractor", "not required", "do not call", "disabled in lite",
		"not needed for", "decoy", "extra_skill", "activation-extra",
	}
	for _, skill := range Default().Skills() {
		text := strings.ToLower(skill.Name + "\n" + skill.Summary + "\n" + skill.Instructions)
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				t.Fatalf("default skill %q leaks harness marker %q in model-facing text %q", skill.ID, marker, text)
			}
		}
	}
	for _, spec := range Default().Tools() {
		text := strings.ToLower(spec.Description)
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				t.Fatalf("default tool %q leaks harness marker %q in description %q", spec.Name, marker, spec.Description)
			}
		}
	}

	scaled, err := Default().Scale(32, 127)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range scaled.Tools() {
		name := strings.ToLower(spec.Name)
		if strings.Contains(name, "decoy") || strings.Contains(name, "extra_skill") || strings.Contains(name, "activation-extra") {
			t.Fatalf("scaled tool name leaks benchmark construction: %q", spec.Name)
		}
		text := strings.ToLower(spec.Description)
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				t.Fatalf("scaled tool %q leaks harness marker %q in description %q", spec.Name, marker, spec.Description)
			}
		}
	}
	for _, skill := range scaled.Skills() {
		if strings.Contains(strings.ToLower(skill.ID), "activation-extra") || strings.Contains(strings.ToLower(skill.ID), "extra_skill") {
			t.Fatalf("scaled skill id leaks benchmark construction: %q", skill.ID)
		}
		text := strings.ToLower(skill.Summary + "\n" + skill.Instructions)
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				t.Fatalf("scaled skill %q leaks harness marker %q", skill.ID, marker)
			}
		}
	}

	// The default CLI opens the checked-in Skill files directly. Keep this
	// guard alongside the catalog checks so a future edit cannot reintroduce
	// harness terminology into the actual model-facing repository.
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate catalog test source")
	}
	moduleRoot := filepath.Dir(filepath.Dir(sourceFile))
	for _, skillID := range []string{"mail", "calendar", "documents", "spreadsheets", "inventory", "crm", "files", "research"} {
		path := filepath.Join(moduleRoot, "skills", skillID, "SKILL.md")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read fixed Skill %q: %v", skillID, err)
		}
		text := strings.ToLower(string(body))
		for _, marker := range markers {
			if strings.Contains(text, marker) {
				t.Fatalf("fixed Skill %q leaks harness marker %q", skillID, marker)
			}
		}
	}
}

func TestCatalogRejectsBrokenReferences(t *testing.T) {
	_, err := New(
		[]SkillSpec{{ID: "s", Name: "s", ToolSetID: "s-tools"}},
		[]ToolSetSpec{{ID: "s-tools", Name: "s-tools", SkillID: "s"}},
		[]ToolSpec{{Name: "bad", SkillID: "s", ToolSetID: "s-tools"}},
	)
	if err == nil {
		t.Fatal("expected missing input schema error")
	}

	_, err = New(
		[]SkillSpec{{ID: "s", Name: "s", ToolSetID: "missing"}},
		[]ToolSetSpec(nil), []ToolSpec(nil),
	)
	if err == nil {
		t.Fatal("expected unknown tool set error")
	}

	_, err = New(
		[]SkillSpec{{ID: "../escape", Name: "escape"}},
		[]ToolSetSpec(nil), []ToolSpec(nil),
	)
	if err == nil {
		t.Fatal("expected unsafe skill identifier error")
	}

	_, err = New(
		[]SkillSpec{{ID: "s", Name: "s", ToolSetID: "s-tools"}},
		[]ToolSetSpec{
			{ID: "s-tools", Name: "s-tools", SkillID: "s"},
			{ID: "s-extra", Name: "s-extra", SkillID: "s"},
		}, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "multiple tool sets") {
		t.Fatalf("error = %v, want multiple-tool-set invariant", err)
	}

	_, err = New(
		[]SkillSpec{{ID: "s", Name: "s"}},
		[]ToolSetSpec{{ID: "s-tools", Name: "s-tools", SkillID: "s"}}, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "ToolSetID is empty") {
		t.Fatalf("error = %v, want missing Skill ToolSetID invariant", err)
	}

	_, err = New(
		[]SkillSpec{
			{ID: "one", Name: "one", ToolSetID: "one-tools"},
			{ID: "two", Name: "two", ToolSetID: "two-tools"},
		},
		[]ToolSetSpec{
			{ID: "one-tools", Name: "shared display", SkillID: "one"},
			{ID: "two-tools", Name: "shared display", SkillID: "two"},
		}, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "alias") {
		t.Fatalf("error = %v, want duplicate ToolSet display alias error", err)
	}

	_, err = New(
		[]SkillSpec{
			{ID: "one", Name: "one", ToolSetID: "one-tools"},
			{ID: "two", Name: "two", ToolSetID: "two-tools"},
		},
		[]ToolSetSpec{
			{ID: "one-tools", Name: "one-tools", SkillID: "one"},
			{ID: "two-tools", Name: "two-tools", SkillID: "two"},
		}, []ToolSpec{
			{Name: "search", SkillID: "one", ToolSetID: "one-tools", InputSchema: &trpctool.Schema{Type: "object"}},
			{Name: "one-tools_search", SkillID: "two", ToolSetID: "two-tools", InputSchema: &trpctool.Schema{Type: "object"}},
		})
	if err == nil || !strings.Contains(err.Error(), "conflicts with qualified") {
		t.Fatalf("error = %v, want raw/qualified collision error", err)
	}

	_, err = New(
		[]SkillSpec{{ID: "one", Name: "one", ToolSetID: "one-tools"}},
		[]ToolSetSpec{{ID: "one-tools", Name: "one-tools", SkillID: "one"}},
		[]ToolSpec{{Name: "bad name", SkillID: "one", ToolSetID: "one-tools", InputSchema: &trpctool.Schema{Type: "object"}}},
	)
	if err == nil {
		t.Fatal("tool identifiers containing whitespace must be rejected")
	}
}

func TestCatalogUsesIDAsCanonicalToolSetRuntimeName(t *testing.T) {
	schema := &trpctool.Schema{Type: "object"}
	c, err := New(
		[]SkillSpec{{ID: "mail", Name: "Mail", ToolSetID: "mail-runtime"}},
		[]ToolSetSpec{{ID: "mail-runtime", Name: "Mail display name", SkillID: "mail"}},
		[]ToolSpec{{Name: "mail_search", SkillID: "mail", ToolSetID: "mail-runtime", InputSchema: schema}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.ToolSet("mail-runtime"); !ok || got.Name != "Mail display name" {
		t.Fatalf("display metadata was not preserved: %#v", got)
	}
}

func TestSkillRepositoryUsesFrameworkFilesystem(t *testing.T) {
	c := Default()
	root := t.TempDir()
	disk, err := NewSkillRepository(c, root)
	if err != nil {
		t.Fatal(err)
	}
	path, err := disk.Path("mail")
	if err != nil || path == "" {
		t.Fatalf("skill path: %v %q", err, path)
	}
	if _, err := os.Stat(filepath.Join(path, "SKILL.md")); err != nil {
		t.Fatalf("materialized SKILL.md: %v", err)
	}
	diskSkill, err := disk.Get(" mail ")
	if err != nil || diskSkill.Body == "" {
		t.Fatalf("framework-backed disk Get: %v %#v", err, diskSkill)
	}
	if diskSkill.Summary.Name != "mail" {
		t.Fatalf("framework-backed disk summary name = %q", diskSkill.Summary.Name)
	}
	parsed, err := trpcskill.NewFSRepository(root)
	if err != nil {
		t.Fatalf("parse materialized repository: %v", err)
	}
	if _, err := parsed.Get("mail"); err != nil {
		t.Fatalf("parse materialized mail skill: %v", err)
	}
}

func TestSkillRepositoryUsesFrameworkFSParsingForMultilineSummary(t *testing.T) {
	c, err := New(
		[]SkillSpec{{ID: "custom", Name: "Custom", Summary: "line one\nline two", Instructions: "instructions", ToolSetID: "custom-tools"}},
		[]ToolSetSpec{{ID: "custom-tools", Name: "Custom tools", SkillID: "custom"}},
		[]ToolSpec{{Name: "custom_read", SkillID: "custom", ToolSetID: "custom-tools", InputSchema: &trpctool.Schema{Type: "object"}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	disk, err := NewSkillRepository(c, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	skill, err := disk.Get("custom")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Summary.Description != "line one\nline two" {
		t.Fatalf("description = %q, want multiline summary", skill.Summary.Description)
	}
	if skill.Body != "instructions" {
		t.Fatalf("body = %q, want exact instructions", skill.Body)
	}
}

func TestTempSkillRepositoryFromFrameworkSkillsUsesLocalFiles(t *testing.T) {
	repository, err := NewTempSkillRepositoryFromSkills([]trpcskill.Skill{
		{Summary: trpcskill.Summary{Name: "local", Description: "local summary"}, Body: "local instructions"},
	})
	if err != nil {
		t.Fatal(err)
	}
	root := repository.Root()
	if root == "" {
		t.Fatal("temporary repository root is empty")
	}
	path, err := repository.Path("local")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(path, trpcskill.SkillFile)); err != nil {
		t.Fatalf("materialized local SKILL.md: %v", err)
	}
	loaded, err := repository.Get("local")
	if err != nil || loaded.Body != "local instructions" {
		t.Fatalf("framework FSRepository load = %#v, err=%v", loaded, err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("temporary repository root still exists after Close: %v", err)
	}
}

func TestOpenSkillRepositoryDoesNotOwnCallerRoot(t *testing.T) {
	root := t.TempDir()
	if _, err := NewSkillRepositoryFromSkills([]trpcskill.Skill{
		{Summary: trpcskill.Summary{Name: "fixed", Description: "fixed summary"}, Body: "fixed instructions"},
	}, root); err != nil {
		t.Fatal(err)
	}
	repository, err := OpenSkillRepository(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.Get("fixed"); err != nil {
		t.Fatal(err)
	}
	if err := repository.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "fixed", trpcskill.SkillFile)); err != nil {
		t.Fatalf("caller-owned Skill root was removed by Close: %v", err)
	}
}

func TestScaleDistractorsIsDeterministic(t *testing.T) {
	first, err := Default().ScaleDistractors(128)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Default().ScaleDistractors(128)
	if err != nil {
		t.Fatal(err)
	}
	if first.Stats().Tools != 128 || first.Stats().DistractorTools != 80 {
		t.Fatalf("unexpected scaled stats: %+v", first.Stats())
	}
	left, right := first.Tools(), second.Tools()
	for i := range left {
		if left[i].Name != right[i].Name || left[i].Description != right[i].Description {
			t.Fatalf("scale is not deterministic at %d: %#v %#v", i, left[i], right[i])
		}
	}
}

func TestScaleSkillsAndToolsIsDeterministicAndLocal(t *testing.T) {
	first, err := Default().Scale(32, 256)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Scale(32, 256)
	if err != nil {
		t.Fatal(err)
	}
	if got := first.Stats(); got.Skills != 32 || got.ToolSets != 32 || got.Tools != 256 {
		t.Fatalf("unexpected scaled stats: %+v", got)
	}
	if got := first.Stats(); got.DistractorTools != 208 || got.ReadOnlyTools < 208 {
		t.Fatalf("new capabilities must be read-only distractors: %+v", got)
	}
	for _, skill := range first.Skills() {
		set, ok := first.ToolSet(skill.ToolSetID)
		if !ok || set.SkillID != skill.ID {
			t.Fatalf("skill %q has invalid set %#v", skill.ID, set)
		}
		tools := first.ToolsForSkill(skill.ID)
		if len(tools) == 0 {
			t.Fatalf("skill %q has no tools", skill.ID)
		}
		if len(tools) == 1 {
			for _, tool := range tools {
				if !tool.ReadOnly || !tool.Distractor {
					t.Fatalf("generated tool is not a local read-only distractor: %#v", tool)
				}
			}
		}
	}
	// OpenAI-compatible function declarations commonly cap the model-facing
	// qualified name at 64 characters. Keep the generated 32/256 stress
	// catalog runnable through those adapters; the verbose Skill ids are not
	// used as the ToolSet prefix.
	for _, spec := range first.Tools() {
		qualified := spec.ToolSetID + "_" + spec.Name
		if len(qualified) > 64 {
			t.Fatalf("qualified generated tool name is too long (%d): %q", len(qualified), qualified)
		}
	}
	left, right := first.Manifest(), second.Manifest()
	if len(left.Skills) != len(right.Skills) || len(left.Tools) != len(right.Tools) {
		t.Fatal("scaled manifests have different sizes")
	}
	for i := range left.Skills {
		if left.Skills[i].ID != right.Skills[i].ID || left.Skills[i].ToolSetID != right.Skills[i].ToolSetID {
			t.Fatalf("skill scaling is not deterministic at %d", i)
		}
	}
	for i := range left.Tools {
		if left.Tools[i].Name != right.Tools[i].Name || left.Tools[i].ToolSetID != right.Tools[i].ToolSetID {
			t.Fatalf("tool scaling is not deterministic at %d", i)
		}
	}
	if _, err := Default().Scale(7, 64); err == nil {
		t.Fatal("expected lower target skill count to be rejected")
	}
	if _, err := Default().Scale(32, 80); err == nil {
		t.Fatal("expected insufficient tool budget to be rejected")
	}
}
