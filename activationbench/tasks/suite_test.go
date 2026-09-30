//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tasks

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/env"
)

func TestDefaultSuiteShape(t *testing.T) {
	suite, err := DefaultSuite()
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Skills) != 8 || len(suite.Tools) != 64 || len(suite.Tasks) != 18 {
		t.Fatalf("unexpected suite shape: skills=%d tools=%d tasks=%d", len(suite.Skills), len(suite.Tools), len(suite.Tasks))
	}
	for _, task := range suite.Tasks {
		if len(planFor(task.ID)) != len(task.RequiredTools) {
			t.Errorf("%s plan length=%d required=%d", task.ID, len(planFor(task.ID)), len(task.RequiredTools))
		}
	}
}

func TestDefaultSuiteUsesModelFacingToolReferences(t *testing.T) {
	suite := MustDefaultSuite()
	var mail bench.SkillSpec
	for _, skill := range suite.Skills {
		if skill.Name == "mail" {
			mail = skill
			break
		}
	}
	if mail.Name == "" {
		t.Fatal("default suite has no mail Skill")
	}
	if strings.Contains(mail.Body, "{{tool:") {
		t.Fatalf("default Skill body still contains an unresolved tool reference: %q", mail.Body)
	}
	if !strings.Contains(mail.Body, "mail-tools_mail_search") {
		t.Fatalf("default Skill body does not use the qualified search name: %q", mail.Body)
	}
	mailGet := toolByName(suite.Tools, "mail_get")
	if mailGet.Name == "" || mailGet.InputSchema == nil || mailGet.InputSchema.Properties["id"] == nil {
		t.Fatal("default mail_get schema is missing")
	}
	if got := mailGet.InputSchema.Properties["id"].Description; !strings.Contains(got, "mail-tools_mail_search") {
		t.Fatalf("default schema description does not use the qualified search name: %q", got)
	}
}

func TestScaledSuiteKeepsTasksAndAddsOnlyLocalDecoys(t *testing.T) {
	suite, err := ScaledSuite(128)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Skills) != 8 || len(suite.Tools) != 128 || len(suite.Tasks) != 18 {
		t.Fatalf("unexpected scaled shape: skills=%d tools=%d tasks=%d", len(suite.Skills), len(suite.Tools), len(suite.Tasks))
	}
	distractors := 0
	for _, spec := range suite.Tools {
		if spec.Distractor {
			distractors++
		}
	}
	if distractors != 80 {
		t.Fatalf("scaled distractor count = %d, want 80", distractors)
	}
	for _, task := range suite.Tasks {
		if len(planFor(task.ID)) != len(task.RequiredTools) {
			t.Fatalf("scaled suite changed plan for %s", task.ID)
		}
	}
	if _, err := ScaledSuite(63); err == nil {
		t.Fatal("expected a minimum tool-count error")
	}
}

func TestScaledSuiteWithSkillsAddsPrivateSkillMenus(t *testing.T) {
	suite, err := ScaledSuiteWithSkills(32, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Skills) != 32 || len(suite.Tools) != 256 || len(suite.Tasks) != 18 {
		t.Fatalf("unexpected skill-scaled shape: skills=%d tools=%d tasks=%d", len(suite.Skills), len(suite.Tools), len(suite.Tasks))
	}
	for _, skill := range suite.Skills[8:] {
		if len(skill.ToolSets) != 1 {
			t.Fatalf("generated skill %q tool sets = %v", skill.Name, skill.ToolSets)
		}
	}
}

func TestScaledSuiteWithSkillsAddsMappedLocalCapabilities(t *testing.T) {
	suite, err := ScaledSuiteWithSkills(32, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(suite.Skills) != 32 || len(suite.Tools) != 256 || len(suite.Tasks) != 18 {
		t.Fatalf("unexpected scaled shape: skills=%d tools=%d tasks=%d", len(suite.Skills), len(suite.Tools), len(suite.Tasks))
	}
	seenSkills := make(map[string]bool, len(suite.Skills))
	toolsBySkill := make(map[string]int, len(suite.Skills))
	for _, skill := range suite.Skills {
		seenSkills[skill.Name] = true
		if len(skill.ToolSets) == 0 {
			t.Fatalf("skill %q has no tool set", skill.Name)
		}
	}
	for _, spec := range suite.Tools {
		if !seenSkills[spec.Skill] {
			t.Fatalf("tool %q references unknown skill %q", spec.Name, spec.Skill)
		}
		toolsBySkill[spec.Skill]++
		if spec.Distractor && spec.Handler == nil {
			t.Fatalf("generated tool %q has no local handler", spec.Name)
		}
	}
	for _, skill := range suite.Skills {
		if toolsBySkill[skill.Name] == 0 {
			t.Fatalf("skill %q has no tools", skill.Name)
		}
	}
	if _, err := ScaledSuiteWithSkills(7, 64); err == nil {
		t.Fatal("expected lower target skill count to fail")
	}
	if _, err := ScaledSuiteWithSkills(32, 80); err == nil {
		t.Fatal("expected insufficient tool budget to fail")
	}
}

func TestEveryDefaultTaskPlanPassesStateEvaluator(t *testing.T) {
	suite := MustDefaultSuite()
	for _, task := range suite.Tasks {
		t.Run(task.ID, func(t *testing.T) {
			state := bench.NewTaskState(task.InitialState)
			for _, step := range planFor(task.ID) {
				raw, err := json.Marshal(step.Args)
				if err != nil {
					t.Fatal(err)
				}
				value, err := Handler(step.Tool)(context.Background(), raw, state)
				if err != nil {
					t.Fatalf("%s: %v", step.Tool, err)
				}
				if value == nil {
					t.Fatalf("%s returned nil", step.Tool)
				}
				state.RecordCall(bench.CallRecord{Name: bench.QualifiedToolName(toolByName(suite.Tools, step.Tool)), Arguments: string(raw), Succeeded: true})
			}
			result := task.Evaluate(state)
			if !result.Passed {
				t.Fatalf("evaluation failed: %+v", result)
			}
		})
	}
}

func TestEvaluatorTreatsFinalStateAsPrimaryQuality(t *testing.T) {
	world := env.InitialState()
	for index := range world.Emails {
		if world.Emails[index].ID != "mail-001" {
			continue
		}
		world.Emails[index].Read = true
		world.Emails[index].Labels = append(world.Emails[index].Labels, "priority")
	}
	state := bench.NewTaskState(map[string]any{WorldKey(): world})
	evaluation := evaluateMailTriage(state)
	if !evaluation.Passed {
		t.Fatalf("final state should pass without a prescribed trace: %+v", evaluation)
	}
	if evaluation.SatisfiedCount != 0 {
		t.Fatalf("trace count should remain diagnostic, got %d", evaluation.SatisfiedCount)
	}
	if evaluation.Score != 1 {
		t.Fatalf("final-state score = %v, want 1", evaluation.Score)
	}
}

func TestHandlerRejectsMalformedArgumentsWithoutStateChange(t *testing.T) {
	state := bench.NewTaskState(InitialValues())
	before, ok := state.Get(WorldKey())
	if !ok {
		t.Fatal("missing initial world")
	}
	if _, err := Handler("mail_mark_read")(context.Background(), []byte(`{"id":"mail-001"}`), state); err == nil {
		t.Fatal("expected missing argument error")
	}
	after, _ := state.Get(WorldKey())
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("malformed call changed state: before=%#v after=%#v", before, after)
	}
}

func toolByName(specs []bench.ToolSpec, name string) bench.ToolSpec {
	for _, spec := range specs {
		if spec.Name == name {
			return spec
		}
	}
	return bench.ToolSpec{Name: name}
}
