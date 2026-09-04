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
		name  string
		eval  func(*bench.TaskState) bench.Evaluation
		state env.State
	}{
		{
			name: "kickoff",
			eval: evaluateCalendarKickoff,
			state: env.State{Events: []env.CalendarEvent{{
				Title: "Project Atlas kickoff", Start: "2026-09-03T10:00:00Z", End: "2026-09-03T11:00:00Z",
				Attendees: []string{"alice@example.test", "bob@example.test"},
			}}},
		},
		{
			name: "design",
			eval: evaluateCalendarDesign,
			state: env.State{Events: []env.CalendarEvent{{
				ID: "event-002", Title: "Design review", Start: "2026-09-03T15:00:00Z", End: "2026-09-03T16:00:00Z",
				Attendees: []string{"alice@example.test", "bob@example.test"},
			}}},
		},
		{
			name: "cross-skill",
			eval: evaluateCrossMailCalendar,
			state: env.State{Events: []env.CalendarEvent{{
				Title: "Atlas follow-up", Start: "2026-09-04T10:00:00Z", End: "2026-09-04T10:30:00Z",
				Attendees: []string{"alice@example.test"},
			}}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			state := bench.NewTaskState(map[string]any{WorldKey(): test.state})
			if evaluation := test.eval(state); !evaluation.Passed {
				t.Fatalf("evaluation failed for equivalent timestamp spelling: %+v", evaluation)
			}
		})
	}
}

func TestSameInstantRejectsInvalidTimestamp(t *testing.T) {
	if sameInstant("not-a-timestamp", "2026-09-03T10:00Z") {
		t.Fatal("invalid timestamp must not compare equal")
	}
}
