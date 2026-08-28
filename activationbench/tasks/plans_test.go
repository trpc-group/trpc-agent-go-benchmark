//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tasks

import "strings"

// PlanStep and planFor are test-only canonical fixtures used to prove that
// each state evaluator accepts one valid sequence. They are deliberately not
// part of the runtime Suite or ModelFactory input, so an LLM cannot receive an
// oracle action plan.
type PlanStep struct {
	Tool string         `json:"tool"`
	Args map[string]any `json:"args,omitempty"`
}

func planFor(taskID string) []PlanStep {
	return clonePlan(plans()[strings.TrimSpace(taskID)])
}

// plans contains valid, deterministic arguments for every required operation
// in the built-in suite. It is used only by evaluator tests.
func plans() map[string][]PlanStep {
	return map[string][]PlanStep{
		"mail-triage-atlas": {
			{Tool: "mail_search", Args: map[string]any{"query": "Project Atlas kickoff", "limit": 5}},
			{Tool: "mail_mark_read", Args: map[string]any{"id": "mail-001", "read": true}},
			{Tool: "mail_label", Args: map[string]any{"id": "mail-001", "label": "priority", "add": true}},
		},
		"mail-draft-stock": {
			{Tool: "mail_search", Args: map[string]any{"query": "Low stock: USB-C hub", "limit": 5}},
			{Tool: "mail_get", Args: map[string]any{"id": "mail-002"}},
			{Tool: "mail_create_draft", Args: map[string]any{"to": "ops@example.test", "subject": "Re: Low stock: USB-C hub", "body": "Confirmed; reserving two units for Acme."}},
		},
		"calendar-schedule-kickoff": {
			{Tool: "calendar_find_slots", Args: map[string]any{"date": "2026-09-03", "duration_minutes": 60}},
			{Tool: "calendar_create_event", Args: map[string]any{"title": "Project Atlas kickoff", "start": "2026-09-03T10:00Z", "end": "2026-09-03T11:00Z", "attendees": "alice@example.test,bob@example.test"}},
		},
		"calendar-update-design": {
			{Tool: "calendar_list_events", Args: map[string]any{"from": "2026-09-03T00:00Z", "to": "2026-09-04T00:00Z"}},
			{Tool: "calendar_update_event", Args: map[string]any{"id": "event-002", "start": "2026-09-03T15:00Z", "end": "2026-09-03T16:00Z"}},
			{Tool: "calendar_add_attendee", Args: map[string]any{"id": "event-002", "attendee": "alice@example.test"}},
		},
		"documents-append-risk": {
			{Tool: "docs_search", Args: map[string]any{"query": "Project Atlas brief", "limit": 5}},
			{Tool: "docs_read", Args: map[string]any{"id": "doc-atlas"}},
			{Tool: "docs_append", Args: map[string]any{"id": "doc-atlas", "text": "Risk: dependency on tool availability."}},
			{Tool: "docs_tag", Args: map[string]any{"id": "doc-atlas", "tag": "review", "add": true}},
		},
		"documents-create-handoff": {
			{Tool: "docs_create", Args: map[string]any{"title": "Atlas handoff", "body": "Handoff checklist."}},
			{Tool: "docs_append", Args: map[string]any{"id": "doc-001", "text": "Owner: Alice."}},
		},
		"spreadsheets-budget": {
			{Tool: "sheet_list", Args: map[string]any{}},
			{Tool: "sheet_read_rows", Args: map[string]any{"sheet_id": "sheet-budget", "limit": 10}},
			{Tool: "sheet_find_rows", Args: map[string]any{"sheet_id": "sheet-budget", "column": "status", "value": "pending"}},
			{Tool: "sheet_update_cell", Args: map[string]any{"sheet_id": "sheet-budget", "row": 1, "column": "amount", "value": "900"}},
			{Tool: "sheet_sum_column", Args: map[string]any{"sheet_id": "sheet-budget", "column": "amount"}},
		},
		"spreadsheets-orders": {
			{Tool: "sheet_append_row", Args: map[string]any{"sheet_id": "sheet-orders", "values": `{"order":"order-002","customer":"Acme","sku":"HUB-01","quantity":"1","status":"pending"}`}},
			{Tool: "sheet_read_rows", Args: map[string]any{"sheet_id": "sheet-orders", "limit": 10}},
		},
		"inventory-reserve": {
			{Tool: "inventory_search", Args: map[string]any{"query": "USB-C hub"}},
			{Tool: "inventory_get_stock", Args: map[string]any{"sku": "HUB-01"}},
			{Tool: "inventory_reserve", Args: map[string]any{"sku": "HUB-01", "quantity": 2, "customer": "Acme"}},
		},
		"inventory-reorder": {
			{Tool: "inventory_get_stock", Args: map[string]any{"sku": "HUB-01"}},
			{Tool: "inventory_set_reorder_point", Args: map[string]any{"sku": "HUB-01", "reorder_point": 6}},
			{Tool: "inventory_list_reservations", Args: map[string]any{"sku": "HUB-01"}},
		},
		"crm-followup": {
			{Tool: "crm_find_contact", Args: map[string]any{"query": "Bob Singh"}},
			{Tool: "crm_get_contact", Args: map[string]any{"id": "contact-bob"}},
			{Tool: "crm_log_activity", Args: map[string]any{"contact_id": "contact-bob", "kind": "call", "note": "Discussed Atlas review"}},
			{Tool: "crm_create_task", Args: map[string]any{"contact_id": "contact-bob", "title": "Send Atlas review", "due": "2026-09-10"}},
		},
		"crm-update-alice": {
			{Tool: "crm_find_contact", Args: map[string]any{"query": "Alice Chen"}},
			{Tool: "crm_update_contact", Args: map[string]any{"id": "contact-alice", "phone": "+1-555-0111", "notes": "Prefers email"}},
			{Tool: "crm_list_tasks", Args: map[string]any{"contact_id": "contact-alice"}},
		},
		"files-refresh-report": {
			{Tool: "files_search", Args: map[string]any{"query": "inventory report"}},
			{Tool: "files_read", Args: map[string]any{"path": "reports/inventory.txt"}},
			{Tool: "files_write", Args: map[string]any{"path": "reports/inventory.txt", "content": "HUB-01 available: 3"}},
		},
		"files-archive-meeting": {
			{Tool: "files_list", Args: map[string]any{"prefix": "notes/"}},
			{Tool: "files_move", Args: map[string]any{"path": "notes/meeting.txt", "destination": "archive/meeting.txt"}},
			{Tool: "files_archive", Args: map[string]any{"path": "archive/meeting.txt", "archived": true}},
		},
		"research-save-finding": {
			{Tool: "research_search_notes", Args: map[string]any{"query": "tool selection"}},
			{Tool: "research_get_source", Args: map[string]any{"id": "source-001"}},
			{Tool: "research_save_finding", Args: map[string]any{"topic": "tool-selection", "claim": "Narrow menus reduce schema context.", "source_id": "source-001"}},
			{Tool: "research_tag_finding", Args: map[string]any{"id": "finding-001", "tag": "validated", "add": true}},
		},
		"research-summarize": {
			{Tool: "research_search_notes", Args: map[string]any{"query": "skill loading"}},
			{Tool: "research_get_source", Args: map[string]any{"id": "source-003"}},
			{Tool: "research_save_finding", Args: map[string]any{"topic": "skill-loading", "claim": "Compact summaries defer detailed instructions until needed.", "source_id": "source-003"}},
			{Tool: "research_list_findings", Args: map[string]any{"topic": "skill-loading"}},
			{Tool: "research_summarize", Args: map[string]any{"topic": "skill-loading"}},
		},
		"cross-mail-calendar": {
			{Tool: "mail_search", Args: map[string]any{"query": "Project Atlas kickoff", "limit": 5}},
			{Tool: "calendar_create_event", Args: map[string]any{"title": "Atlas follow-up", "start": "2026-09-04T10:00Z", "end": "2026-09-04T10:30Z", "attendees": "alice@example.test"}},
		},
		"cross-inventory-orders": {
			{Tool: "inventory_get_stock", Args: map[string]any{"sku": "HUB-01"}},
			{Tool: "sheet_append_row", Args: map[string]any{"sheet_id": "sheet-orders", "values": `{"order":"restock-note-001","customer":"Acme","sku":"HUB-01","quantity":"2","status":"review"}`}},
		},
	}
}

func clonePlan(in []PlanStep) []PlanStep {
	if len(in) == 0 {
		return nil
	}
	out := make([]PlanStep, len(in))
	for i, step := range in {
		out[i] = PlanStep{Tool: step.Tool, Args: make(map[string]any, len(step.Args))}
		for key, value := range step.Args {
			out[i].Args[key] = value
		}
	}
	return out
}
