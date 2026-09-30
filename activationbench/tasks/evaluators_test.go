//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License 2.0.
//

package tasks

import (
	"testing"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/env"
)

func TestCalendarEvaluatorsAcceptEquivalentISO8601Spelling(t *testing.T) {
	tests := []struct {
		name   string
		taskID string
		eval   func(*bench.TaskState) bench.Evaluation
		update func(*env.State)
	}{
		{
			name: "kickoff", taskID: "calendar-schedule-kickoff", eval: evaluateCalendarKickoff,
			update: func(state *env.State) {
				event := mustEvent(state, "event-003")
				event.Start = "2026-09-03T10:00:00Z"
				event.End = "2026-09-03T11:00:00Z"
			},
		},
		{
			name: "design", taskID: "calendar-update-design", eval: evaluateCalendarDesign,
			update: func(state *env.State) {
				event := mustEvent(state, "event-002")
				event.Start = "2026-09-03T15:00:00Z"
				event.End = "2026-09-03T16:00:00Z"
			},
		},
		{
			name: "cross-skill", taskID: "cross-mail-calendar", eval: evaluateCrossMailCalendar,
			update: func(state *env.State) {
				event := mustEvent(state, "event-003")
				event.Start = "2026-09-04T10:00:00Z"
				event.End = "2026-09-04T10:30:00Z"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			world := expectedWorld(test.taskID)
			test.update(&world)
			state := bench.NewTaskState(map[string]any{WorldKey(): world})
			if evaluation := test.eval(state); !evaluation.Passed {
				t.Fatalf("evaluation failed for equivalent timestamp spelling: %+v", evaluation)
			}
		})
	}
}

func TestMailEvaluatorRejectsCollateralStateChange(t *testing.T) {
	world := expectedWorld("mail-triage-atlas")
	mustEmail(&world, "mail-002").Archived = true
	state := bench.NewTaskState(map[string]any{WorldKey(): world})
	evaluation := evaluateMailTriage(state)
	if evaluation.Passed {
		t.Fatalf("collateral state change passed: %+v", evaluation)
	}
	if evaluation.CollateralCount != 1 {
		t.Fatalf("collateral count = %d, want 1", evaluation.CollateralCount)
	}
}

func TestMailEvaluatorChecksObservedFinalResponse(t *testing.T) {
	state := bench.NewTaskState(map[string]any{WorldKey(): expectedWorld("mail-triage-atlas")})
	state.SetFinalResponse("Project Atlas email updated.")
	if evaluation := evaluateMailTriage(state); evaluation.Passed {
		t.Fatalf("response without message id passed: %+v", evaluation)
	}
	state.SetFinalResponse("Project Atlas email updated; message id mail-001.")
	if evaluation := evaluateMailTriage(state); !evaluation.Passed {
		t.Fatalf("response with message id failed: %+v", evaluation)
	}
}

func TestBudgetEvaluatorAcceptsEquivalentComputedResponse(t *testing.T) {
	state := bench.NewTaskState(map[string]any{WorldKey(): expectedWorld("spreadsheets-budget")})
	state.SetFinalResponse("The updated amount sum is 2,100.")
	if evaluation := evaluateBudget(state); !evaluation.Passed {
		t.Fatalf("computed response failed: %+v", evaluation)
	}
}

func TestBudgetEvaluatorRejectsMissingComputedResult(t *testing.T) {
	state := bench.NewTaskState(map[string]any{WorldKey(): expectedWorld("spreadsheets-budget")})
	state.SetFinalResponse("The budget row was updated.")
	if evaluation := evaluateBudget(state); evaluation.Passed {
		t.Fatalf("missing computed result passed: %+v", evaluation)
	}
}

func TestSameInstantRejectsInvalidTimestamp(t *testing.T) {
	if sameInstant("not-a-timestamp", "2026-09-03T10:00Z") {
		t.Fatal("invalid timestamp must not compare equal")
	}
}
