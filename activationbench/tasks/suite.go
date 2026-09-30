//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package tasks defines the deterministic ActivationBench-Lite task suite.
// The suite is intentionally small enough to run without containers while
// retaining stateful, multi-step workflows across multiple Skill/ToolSets.
package tasks

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/catalog"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/env"
)

const worldKey = "activationbench.world"

// DefaultSuite returns the local no-container benchmark suite.  It contains
// eight Skill summaries, eight activation ToolSets, 64 tool declarations, and
// 18 deterministic tasks (including two cross-skill tasks).
func DefaultSuite() (bench.Suite, error) {
	return suiteFromCatalog(catalog.Default())
}

// ScaledSuite returns the default local suite with at least targetTools
// declarations. Extra declarations are deterministic, read-only distractors
// distributed across the existing Skill ToolSets; task state and plans remain
// unchanged. This is the recommended way to run menu-size sweeps (64/128/256
// ...) without introducing a container or an external service.
func ScaledSuite(targetTools int) (bench.Suite, error) {
	if targetTools < 64 {
		return bench.Suite{}, fmt.Errorf("target tool count must be at least 64 (got %d)", targetTools)
	}
	c, err := catalog.Default().ScaleDistractors(targetTools)
	if err != nil {
		return bench.Suite{}, err
	}
	return suiteFromCatalog(c)
}

// ScaledSuiteWithSkills returns a deterministic local suite with the
// requested Skill and tool counts. New Skills own private ToolSets and
// read-only distractor tools; the 18 task plans and their stateful evaluators
// remain unchanged. targetSkills must be at least the eight built-in Skills,
// and targetTools must leave one tool for every newly added Skill.
func ScaledSuiteWithSkills(targetSkills, targetTools int) (bench.Suite, error) {
	if targetSkills < 8 {
		return bench.Suite{}, fmt.Errorf("target skill count must be at least 8 (got %d)", targetSkills)
	}
	if targetTools < 64 {
		return bench.Suite{}, fmt.Errorf("target tool count must be at least 64 (got %d)", targetTools)
	}
	c, err := catalog.Scale(targetSkills, targetTools)
	if err != nil {
		return bench.Suite{}, err
	}
	return suiteFromCatalog(c)
}

// MustScaledSuiteWithSkills is convenient for examples and tests.
func MustScaledSuiteWithSkills(targetSkills, targetTools int) bench.Suite {
	suite, err := ScaledSuiteWithSkills(targetSkills, targetTools)
	if err != nil {
		panic(err)
	}
	return suite
}

// MustScaledSuite is convenient for examples and tests.
func MustScaledSuite(targetTools int) bench.Suite {
	suite, err := ScaledSuite(targetTools)
	if err != nil {
		panic(err)
	}
	return suite
}

func suiteFromCatalog(c *catalog.Catalog) (bench.Suite, error) {
	if c == nil {
		return bench.Suite{}, fmt.Errorf("catalog must not be nil")
	}
	if stats := c.Stats(); stats.Skills < 8 || stats.Tools < 64 {
		// Scaled catalogs intentionally have more Skills/tools. Keep the
		// built-in minimum while allowing menu-size sweeps.
		return bench.Suite{}, fmt.Errorf("unexpected catalog size: %+v", stats)
	}
	for _, skill := range c.Skills() {
		if strings.TrimSpace(skill.ToolSetID) == "" {
			return bench.Suite{}, fmt.Errorf("skill %q has no ToolSet", skill.ID)
		}
		set, ok := c.ToolSet(skill.ToolSetID)
		if !ok || set.SkillID != skill.ID {
			return bench.Suite{}, fmt.Errorf("skill %q has invalid ToolSet %q", skill.ID, skill.ToolSetID)
		}
		if len(c.ToolsForSkill(skill.ID)) == 0 {
			return bench.Suite{}, fmt.Errorf("skill %q has no tools", skill.ID)
		}
	}
	suite := bench.Suite{Name: "activationbench-lite"}
	for _, spec := range c.Skills() {
		suite.Skills = append(suite.Skills, bench.SkillSpec{
			Name: spec.ID, Description: spec.Summary, Body: spec.Instructions,
			ToolSets: []string{spec.ToolSetID},
		})
	}
	for _, spec := range c.Tools() {
		suite.Tools = append(suite.Tools, bench.ToolSpec{
			Name: spec.Name, Description: spec.Description, Skill: spec.SkillID,
			ToolSet: spec.ToolSetID, InputSchema: spec.InputSchema,
			OutputSchema: spec.OutputSchema, ReadOnly: spec.ReadOnly,
			Distractor: spec.Distractor, Handler: HandlerWithCatalog(c, spec.Name),
		})
	}
	suite.Tasks = defaultTasks()
	if err := suite.Validate(); err != nil {
		return bench.Suite{}, err
	}
	// Catalog text is authored with raw-name references, while the public
	// Suite returned to callers should already be ready for a model request.
	// Resolve those references from ToolSpec metadata so custom scaling and
	// ToolSet names remain correct without hand-maintained prefixes.
	rendered, err := bench.RenderModelFacingSuite(suite)
	if err != nil {
		return bench.Suite{}, err
	}
	return rendered, nil
}

// MustDefaultSuite is convenient for examples and tests.
func MustDefaultSuite() bench.Suite {
	suite, err := DefaultSuite()
	if err != nil {
		panic(err)
	}
	return suite
}

// InitialValues returns a fresh generic TaskState payload.  The root runner's
// TaskState is deliberately generic; handlers clone this world before every
// operation, so sharing the fixture map between mode definitions is safe.
func InitialValues() map[string]any {
	return map[string]any{worldKey: env.InitialState()}
}

// WorldKey exposes the reserved state key for custom task evaluators.
func WorldKey() string { return worldKey }

// Handler returns a generic benchmark ToolHandler backed by the local world.
// Unknown tool names are rejected by env.Environment. Catalog distractors are
// safe read-only operations; their calls remain visible as wrong-tool attempts
// without exposing benchmark-only permission errors to the model.
func Handler(toolName string) bench.ToolHandler {
	return HandlerWithCatalog(catalog.Default(), toolName)
}

// HandlerWithCatalog is Handler with an explicit immutable catalog.  It is
// useful when constructing a suite once and avoids rebuilding catalog metadata
// for every tool closure.
func HandlerWithCatalog(capabilityCatalog *catalog.Catalog, toolName string) bench.ToolHandler {
	if capabilityCatalog == nil {
		capabilityCatalog = catalog.Default()
	}
	// Capture one immutable catalog per tool adapter.  Building the 64-schema
	// default catalog for every call would add avoidable overhead to large
	// token-sweep experiments.
	return func(ctx context.Context, raw []byte, state *bench.TaskState) (any, error) {
		world, ok := stateWorld(state)
		if !ok {
			return nil, fmt.Errorf("task state does not contain %q", worldKey)
		}
		var args map[string]any
		if len(strings.TrimSpace(string(raw))) > 0 && strings.TrimSpace(string(raw)) != "null" {
			if err := json.Unmarshal(raw, &args); err != nil {
				return nil, fmt.Errorf("decode %s arguments: %w", toolName, err)
			}
		}
		local := env.NewWithStateAndCatalog(world, capabilityCatalog)
		response, err := local.Execute(ctx, toolName, args)
		if err != nil {
			// A failed fixture call is transactional at the task boundary. The
			// local environment may have validated or logged the attempt, but
			// its world is discarded so an error cannot leak partial state into
			// the next model turn.
			return nil, err
		}
		state.Set(worldKey, local.State())
		return response, nil
	}
}

func stateWorld(state *bench.TaskState) (env.State, bool) {
	if state == nil {
		return env.State{}, false
	}
	value, ok := state.Get(worldKey)
	if !ok {
		return env.State{}, false
	}
	switch typed := value.(type) {
	case env.State:
		return typed.Clone(), true
	case *env.State:
		if typed == nil {
			return env.State{}, false
		}
		return typed.Clone(), true
	default:
		return env.State{}, false
	}
}

func defaultTasks() []bench.Task {
	return []bench.Task{
		task("mail-triage-atlas", "Find the Project Atlas kickoff email, mark it read, and add the priority label. Confirm the final message id.", []string{"mail"}, []string{"mail_search", "mail_mark_read", "mail_label"}, evaluateMailTriage),
		task("mail-draft-stock", "Find the low-stock USB-C hub email, read it, and draft a reply to ops@example.test with subject 'Re: Low stock: USB-C hub' and body 'Confirmed; reserving two units for Acme.'", []string{"mail"}, []string{"mail_search", "mail_get", "mail_create_draft"}, evaluateMailDraft),
		task("calendar-schedule-kickoff", "Find a free 60-minute slot on 2026-09-03 and create 'Project Atlas kickoff' from 10:00Z to 11:00Z with alice@example.test and bob@example.test.", []string{"calendar"}, []string{"calendar_find_slots", "calendar_create_event"}, evaluateCalendarKickoff),
		task("calendar-update-design", "Update the Design review event to 15:00Z–16:00Z on 2026-09-03 and add alice@example.test as an attendee without removing bob@example.test.", []string{"calendar"}, []string{"calendar_list_events", "calendar_update_event", "calendar_add_attendee"}, evaluateCalendarDesign),
		task("documents-append-risk", "Find and read the Project Atlas brief, append 'Risk: dependency on tool availability.' and add the review tag.", []string{"documents"}, []string{"docs_search", "docs_read", "docs_append", "docs_tag"}, evaluateDocumentRisk),
		task("documents-create-handoff", "Create a document titled 'Atlas handoff' with body 'Handoff checklist.' and append 'Owner: Alice.'.", []string{"documents"}, []string{"docs_create", "docs_append"}, evaluateDocumentHandoff),
		task("spreadsheets-budget", "Inspect the Budget sheet, find the pending design row, change its amount to 900, then compute the amount sum.", []string{"spreadsheets"}, []string{"sheet_list", "sheet_read_rows", "sheet_find_rows", "sheet_update_cell", "sheet_sum_column"}, evaluateBudget),
		task("spreadsheets-orders", "Append order-002 for Acme with SKU HUB-01, quantity 1, status pending to the Orders sheet, then read the rows.", []string{"spreadsheets"}, []string{"sheet_append_row", "sheet_read_rows"}, evaluateOrders),
		task("inventory-reserve", "Search for the USB-C hub, check its available stock, and reserve two units for Acme.", []string{"inventory"}, []string{"inventory_search", "inventory_get_stock", "inventory_reserve"}, evaluateReservation),
		task("inventory-reorder", "Check HUB-01 stock, set its reorder point to 6, and list its reservations.", []string{"inventory"}, []string{"inventory_get_stock", "inventory_set_reorder_point", "inventory_list_reservations"}, evaluateReorder),
		task("crm-followup", "Find Bob Singh, read his contact, log a call saying 'Discussed Atlas review', and create a follow-up task due 2026-09-10 titled 'Send Atlas review'.", []string{"crm"}, []string{"crm_find_contact", "crm_get_contact", "crm_log_activity", "crm_create_task"}, evaluateCRMFollowup),
		task("crm-update-alice", "Find Alice Chen, update her phone to +1-555-0111 and notes to 'Prefers email', then list her open tasks.", []string{"crm"}, []string{"crm_find_contact", "crm_update_contact", "crm_list_tasks"}, evaluateCRMUpdate),
		task("files-refresh-report", "Search for the inventory report, read it, and replace reports/inventory.txt with 'HUB-01 available: 3'.", []string{"files"}, []string{"files_search", "files_read", "files_write"}, evaluateFileReport),
		task("files-archive-meeting", "List the notes, move notes/meeting.txt to archive/meeting.txt, and archive the moved file.", []string{"files"}, []string{"files_list", "files_move", "files_archive"}, evaluateFileArchive),
		task("research-save-finding", "Search for the tool selection source, read source-001, save a finding under topic 'tool-selection' with claim 'Narrow menus reduce schema context.' and tag it validated.", []string{"research"}, []string{"research_search_notes", "research_get_source", "research_save_finding", "research_tag_finding"}, evaluateResearchFinding),
		task("research-summarize", "Search and read source-003, save a finding on topic 'skill-loading' with claim 'Compact summaries defer detailed instructions until needed.', list findings, and summarize the topic.", []string{"research"}, []string{"research_search_notes", "research_get_source", "research_save_finding", "research_list_findings", "research_summarize"}, evaluateResearchSummary),
		task("cross-mail-calendar", "Find the Project Atlas kickoff email, then create a calendar event 'Atlas follow-up' from 2026-09-04T10:00Z to 2026-09-04T10:30Z for alice@example.test.", []string{"mail", "calendar"}, []string{"mail_search", "calendar_create_event"}, evaluateCrossMailCalendar),
		task("cross-inventory-orders", "Check HUB-01 stock and append a restock note row to the Orders sheet for Acme with quantity 2 and status review.", []string{"inventory", "spreadsheets"}, []string{"inventory_get_stock", "sheet_append_row"}, evaluateCrossInventoryOrders),
	}
}

func task(id, prompt string, skills, tools []string, evaluator bench.Evaluator) bench.Task {
	return bench.Task{ID: id, Prompt: prompt, RequiredSkills: skills, RequiredTools: tools, SessionID: sessionFor(id), InitialState: InitialValues(), Evaluate: evaluator}
}

// sessionFor groups related tasks so a runner that reuses session objects can
// measure session-lifetime activation.  The default runner may still choose
// to isolate each task; the metadata remains useful for custom runners.
func sessionFor(taskID string) string {
	switch {
	case strings.HasPrefix(taskID, "mail-"), taskID == "cross-mail-calendar":
		return "mail-calendar-session"
	case strings.HasPrefix(taskID, "calendar-"):
		return "mail-calendar-session"
	case strings.HasPrefix(taskID, "documents-"):
		return "documents-session"
	case strings.HasPrefix(taskID, "spreadsheets-"), strings.HasPrefix(taskID, "inventory-"), taskID == "cross-inventory-orders":
		return "inventory-sheets-session"
	case strings.HasPrefix(taskID, "crm-"):
		return "crm-session"
	case strings.HasPrefix(taskID, "files-"):
		return "files-session"
	case strings.HasPrefix(taskID, "research-"):
		return "research-session"
	default:
		return taskID
	}
}
