//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package activationbench

import (
	"reflect"
	"strings"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/env"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestSuiteRejectsToolSetSharedAcrossSkills(t *testing.T) {
	suite := Suite{
		Name: "shared-set",
		Skills: []SkillSpec{
			{Name: "mail", ToolSets: []string{"shared-tools"}},
			{Name: "calendar", ToolSets: []string{"shared-tools"}},
		},
		Tools: []ToolSpec{
			{Name: "search_mail", Skill: "mail", ToolSet: "shared-tools"},
			{Name: "search_calendar", Skill: "calendar", ToolSet: "shared-tools"},
		},
	}
	if err := suite.Validate(); err == nil {
		t.Fatal("expected a shared ToolSet validation error")
	}
}

func TestSuiteRejectsFrameworkToolNameCollision(t *testing.T) {
	suite := Suite{
		Name:  "framework-name-collision",
		Tools: []ToolSpec{{Name: "skill_load"}},
		Tasks: []Task{{ID: "task-1"}},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "framework-provided") {
		t.Fatalf("Validate error = %v, want framework-provided collision", err)
	}
}

func TestFrameworkToolNamesCoverOptionalSkillAndWorkspaceSurfaces(t *testing.T) {
	for _, name := range []string{
		"skill_load", "skill_list_docs", "skill_select_docs", "skill_run",
		"skill_exec", "skill_write_stdin", "skill_poll_session",
		"skill_kill_session", "workspace_exec", "workspace_save_artifact",
		"workspace_write_stdin", "workspace_kill_session",
	} {
		if !IsFrameworkToolName(name) {
			t.Fatalf("framework tool %q is not reserved", name)
		}
	}
	if IsFrameworkToolName("evil_skill_load") {
		t.Fatal("framework name check accepted a suffix lookalike")
	}
}

func TestConventionalToolSetAliasIsStrict(t *testing.T) {
	tests := []struct {
		qualified, unqualified string
		want                   bool
	}{
		{"mail-tools_search", "search", true},
		{"research_ops_search", "search", false},
		{"evil_mail_search", "search", false},
		{"mail-tools_search", "", false},
	}
	for _, test := range tests {
		if got := ConventionalToolSetAlias(test.qualified, test.unqualified); got != test.want {
			t.Errorf("ConventionalToolSetAlias(%q, %q) = %t, want %t", test.qualified, test.unqualified, got, test.want)
		}
	}
}

func TestRenderModelFacingTextUsesQualifiedToolNames(t *testing.T) {
	specs := []ToolSpec{
		{Name: "search", Skill: "research", ToolSet: "research_ops"},
		{Name: "clock"},
	}
	got, err := RenderModelFacingText("Call {{tool:search}}, then check {{tool:clock}}.", specs)
	if err != nil {
		t.Fatalf("RenderModelFacingText: %v", err)
	}
	want := "Call research_ops_search, then check clock."
	if got != want {
		t.Fatalf("rendered text = %q, want %q", got, want)
	}
	framework, err := RenderModelFacingText("Load a Skill with {{tool:skill_load}}.", specs)
	if err != nil {
		t.Fatalf("RenderModelFacingText framework reference: %v", err)
	}
	if framework != "Load a Skill with skill_load." {
		t.Fatalf("framework reference rendered to %q", framework)
	}
	unchanged, err := RenderModelFacingText("No explicit references here.", specs)
	if err != nil {
		t.Fatalf("RenderModelFacingText without references: %v", err)
	}
	if unchanged != "No explicit references here." {
		t.Fatalf("text without references changed to %q", unchanged)
	}
}

func TestRenderModelFacingTextRejectsUnknownAndMalformedReferences(t *testing.T) {
	specs := []ToolSpec{{Name: "search", Skill: "research", ToolSet: "research_ops"}}
	for _, test := range []struct {
		name string
		text string
	}{
		{name: "unknown", text: "Use {{tool:save}}."},
		{name: "unterminated", text: "Use {{tool:search."},
		{name: "empty", text: "Use {{tool:}}."},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := RenderModelFacingText(test.text, specs); err == nil {
				t.Fatalf("RenderModelFacingText(%q) succeeded, want error", test.text)
			}
		})
	}
}

func TestRenderModelFacingSuitePreservesRawMetadata(t *testing.T) {
	suite := Suite{
		Name: "render",
		Skills: []SkillSpec{{
			Name: "research", Description: "Use {{tool:search}}.",
			Body: "Results come from {{tool:search}}.", ToolSets: []string{"research_ops"},
		}},
		Tools: []ToolSpec{{
			Name: "search", Description: "Search with {{tool:search}}.",
			Skill: "research", ToolSet: "research_ops",
			InputSchema: &tool.Schema{
				Type: "object",
				Properties: map[string]*tool.Schema{
					"id": {Type: "string", Description: "Id from {{tool:search}}."},
				},
			},
		}},
		Tasks: []Task{{ID: "task-1", Prompt: "Use {{tool:search}}."}},
	}
	rendered, err := RenderModelFacingSuite(suite)
	if err != nil {
		t.Fatalf("RenderModelFacingSuite: %v", err)
	}
	if suite.Skills[0].Body != "Results come from {{tool:search}}." || suite.Tools[0].Description != "Search with {{tool:search}}." {
		t.Fatal("RenderModelFacingSuite mutated the source suite")
	}
	if rendered.Skills[0].Description != "Use research_ops_search." || rendered.Skills[0].Body != "Results come from research_ops_search." {
		t.Fatalf("rendered skill text = %+v", rendered.Skills[0])
	}
	if rendered.Tools[0].Description != "Search with research_ops_search." {
		t.Fatalf("rendered tool description = %q", rendered.Tools[0].Description)
	}
	if got := rendered.Tools[0].InputSchema.Properties["id"].Description; got != "Id from research_ops_search." {
		t.Fatalf("rendered schema description = %q", got)
	}
	if rendered.Tasks[0].Prompt != "Use research_ops_search." {
		t.Fatalf("rendered task prompt = %q", rendered.Tasks[0].Prompt)
	}
	if rendered.Tools[0].Name != "search" || rendered.Skills[0].ToolSets[0] != "research_ops" {
		t.Fatalf("raw metadata changed during rendering: %+v %+v", rendered.Tools[0], rendered.Skills[0])
	}
}

func TestSuiteRejectsQualifiedFrameworkToolNameCollision(t *testing.T) {
	suite := Suite{
		Name:   "qualified-framework-name-collision",
		Skills: []SkillSpec{{Name: "skill", ToolSets: []string{"skill"}}},
		Tools:  []ToolSpec{{Name: "load", Skill: "skill", ToolSet: "skill"}},
		Tasks:  []Task{{ID: "task-1"}},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "qualified tool") || !strings.Contains(err.Error(), "framework-provided") {
		t.Fatalf("Validate error = %v, want qualified framework-provided collision", err)
	}
}

func TestSuiteRejectsBaseToolWithToolSet(t *testing.T) {
	suite := Suite{
		Name:  "base-tool-set",
		Tools: []ToolSpec{{Name: "clock", ToolSet: "misc"}},
		Tasks: []Task{{ID: "task-1", RequiredTools: []string{"clock"}}},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "base tool") {
		t.Fatalf("Validate error = %v, want base-tool ToolSet error", err)
	}
}

func TestSuiteRejectsToolNameSurroundingWhitespace(t *testing.T) {
	suite := Suite{
		Name:  "tool-name-whitespace",
		Tools: []ToolSpec{{Name: " clock "}},
		Tasks: []Task{{ID: "task-1", RequiredTools: []string{"clock"}}},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "surrounding whitespace") {
		t.Fatalf("Validate error = %v, want canonical tool-name error", err)
	}
}

func TestSuiteRejectsRawQualifiedToolNameCollision(t *testing.T) {
	suite := Suite{
		Name: "raw-qualified-collision",
		Skills: []SkillSpec{
			{Name: "research", ToolSets: []string{"research_ops"}},
			{Name: "other", ToolSets: []string{"other_ops"}},
		},
		Tools: []ToolSpec{
			{Name: "search", Skill: "research", ToolSet: "research_ops"},
			{Name: "research_ops_search", Skill: "other", ToolSet: "other_ops"},
		},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "ambiguous") {
		t.Fatalf("Validate error = %v, want raw/qualified ambiguity", err)
	}
}

func TestSuiteRejectsUnmappedSkillToolSets(t *testing.T) {
	base := Suite{
		Name:   "unmapped-skill-sets",
		Skills: []SkillSpec{{Name: "research"}},
		Tools: []ToolSpec{
			{Name: "search", Skill: "research", ToolSet: "research_read"},
			{Name: "save", Skill: "research", ToolSet: "research_write"},
		},
	}
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "multiple tool sets") {
		t.Fatalf("Validate error = %v, want missing multi-set mapping", err)
	}

	base.Skills[0].ToolSets = []string{"research_read"}
	if err := base.Validate(); err == nil || !strings.Contains(err.Error(), "does not map owned tool set") {
		t.Fatalf("Validate error = %v, want partial mapping error", err)
	}
}

func TestSuiteRejectsRequiredToolWithoutOwningSkill(t *testing.T) {
	suite := Suite{
		Name:   "required-skill-mismatch",
		Skills: []SkillSpec{{Name: "research", ToolSets: []string{"research-tools"}}},
		Tools:  []ToolSpec{{Name: "search", Skill: "research", ToolSet: "research-tools"}},
		Tasks:  []Task{{ID: "task-1", RequiredSkills: []string{}, RequiredTools: []string{"search"}}},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "does not declare that skill") {
		t.Fatalf("Validate error = %v, want required-skill mismatch", err)
	}
}

func TestToolAliasesKeepQualifiedNameAuthoritativeOnCollision(t *testing.T) {
	specs := []ToolSpec{
		{Name: "search", Skill: "research", ToolSet: "research_ops"},
		// This deliberately awkward input has an unqualified name equal to
		// another spec's qualified name. The helper must keep the qualified
		// spelling authoritative rather than overwriting it.
		{Name: "research_ops_search", Skill: "other", ToolSet: "other_ops"},
	}
	aliases := ToolNameAliases(specs)
	if got := aliases["research_ops_search"]; got != "research_ops_search" {
		t.Fatalf("qualified alias = %q, want research_ops_search", got)
	}
}

func TestSuiteRejectsDuplicateRequiredToolsAndSkills(t *testing.T) {
	suite := Suite{
		Name:   "duplicate-requirements",
		Skills: []SkillSpec{{Name: "research", ToolSets: []string{"research-tools"}}},
		Tools:  []ToolSpec{{Name: "search", Skill: "research", ToolSet: "research-tools"}},
		Tasks: []Task{{
			ID:             "task-1",
			RequiredSkills: []string{"research", "research"},
			RequiredTools:  []string{"search"},
		}},
	}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "required skill") {
		t.Fatalf("Validate error = %v, want duplicate required-skill error", err)
	}
	suite.Tasks[0].RequiredSkills = []string{"research"}
	suite.Tasks[0].RequiredTools = []string{"search", "research-tools_search"}
	if err := suite.Validate(); err == nil || !strings.Contains(err.Error(), "required tool") {
		t.Fatalf("Validate error = %v, want duplicate raw/qualified tool error", err)
	}
}

func TestDefaultEvaluationRejectsLookalikeToolName(t *testing.T) {
	state := NewTaskState(nil)
	state.RecordCall(CallRecord{Name: "evil_mail_search", Succeeded: true})
	evaluation := DefaultEvaluation(Task{RequiredTools: []string{"mail_search"}}, state)
	if evaluation.Passed || evaluation.SatisfiedCount != 0 {
		t.Fatalf("lookalike tool should not satisfy requirement: %+v", evaluation)
	}
	state.RecordCall(CallRecord{Name: "mail-tools_mail_search", Succeeded: true})
	evaluation = DefaultEvaluation(Task{RequiredTools: []string{"mail_search"}}, state)
	if !evaluation.Passed || evaluation.SatisfiedCount != 1 {
		t.Fatalf("conventional qualified tool should satisfy requirement: %+v", evaluation)
	}
}

func TestDefaultEvaluationWithSpecsResolvesCustomToolSetAlias(t *testing.T) {
	state := NewTaskState(nil)
	state.RecordCall(CallRecord{Name: "research_ops_search", Succeeded: true})
	specs := []ToolSpec{{
		Name:    "search",
		Skill:   "research",
		ToolSet: "research_ops",
	}}
	evaluation := DefaultEvaluationWithSpecs(
		Task{RequiredTools: []string{"search"}},
		state,
		specs,
	)
	if !evaluation.Passed || evaluation.SatisfiedCount != 1 {
		t.Fatalf("custom ToolSet alias should satisfy requirement: %+v", evaluation)
	}
}

func TestDefaultEvaluationWithSpecsRejectsLookalikeAlias(t *testing.T) {
	state := NewTaskState(nil)
	state.RecordCall(CallRecord{Name: "evil_research_ops_search", Succeeded: true})
	specs := []ToolSpec{{
		Name:    "search",
		Skill:   "research",
		ToolSet: "research_ops",
	}}
	evaluation := DefaultEvaluationWithSpecs(
		Task{RequiredTools: []string{"search"}},
		state,
		specs,
	)
	if evaluation.Passed || evaluation.SatisfiedCount != 0 {
		t.Fatalf("lookalike custom ToolSet call should not satisfy requirement: %+v", evaluation)
	}
}

func TestNewTaskStateDeepCopiesNestedValuesAndTypedFixtures(t *testing.T) {
	type fixture struct {
		Labels []string
		Meta   map[string][]int
	}
	original := fixture{
		Labels: []string{"one"},
		Meta:   map[string][]int{"values": {1, 2}},
	}
	world := env.InitialState()
	initial := map[string]any{
		"fixture": &original,
		"world":   world,
	}
	state := NewTaskState(initial)

	// Mutating caller-owned nested values after construction must not alter the
	// state consumed by a task run.
	original.Labels[0] = "caller-mutated"
	original.Meta["values"][0] = 99
	world.Emails[0].Labels[0] = "caller-mutated"

	fixtureValue, ok := state.Get("fixture")
	if !ok {
		t.Fatal("missing cloned fixture")
	}
	clonedFixture, ok := fixtureValue.(*fixture)
	if !ok {
		t.Fatalf("fixture type = %T, want *fixture", fixtureValue)
	}
	if clonedFixture == &original || clonedFixture.Labels[0] != "one" || clonedFixture.Meta["values"][0] != 1 {
		t.Fatalf("nested fixture was not copied: %#v", clonedFixture)
	}
	worldValue, ok := state.Get("world")
	if !ok {
		t.Fatal("missing cloned world")
	}
	clonedWorld, ok := worldValue.(env.State)
	if !ok {
		t.Fatalf("world type = %T, want env.State", worldValue)
	}
	if clonedWorld.Emails[0].Labels[0] == "caller-mutated" {
		t.Fatal("typed fixture slices were aliased")
	}

	// Mutating the task-owned copy must also leave the caller's values intact.
	clonedFixture.Labels[0] = "task-mutated"
	clonedFixture.Meta["values"][0] = 77
	if original.Labels[0] != "caller-mutated" || original.Meta["values"][0] != 99 {
		t.Fatalf("task mutation leaked back to caller: %#v", original)
	}
	if reflect.DeepEqual(clonedFixture, &original) {
		t.Fatal("cloned fixture unexpectedly aliases original")
	}
}

func TestNewTaskStateDeepCopyPreservesReferenceCycles(t *testing.T) {
	type node struct {
		Name string
		Next *node
	}
	root := &node{Name: "root"}
	root.Next = root
	state := NewTaskState(map[string]any{"node": root})
	value, ok := state.Get("node")
	if !ok {
		t.Fatal("missing cloned node")
	}
	clone, ok := value.(*node)
	if !ok || clone == root || clone.Next != clone || clone.Name != "root" {
		t.Fatalf("cycle was not preserved in an independent clone: %#v", value)
	}
}
