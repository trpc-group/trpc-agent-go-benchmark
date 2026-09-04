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
	"fmt"
	"strings"
	"time"

	bench "trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/env"
)

func evaluateMailTriage(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	mail, found := emailByID(world, "mail-001")
	return checked(state, []string{"mail_search", "mail_mark_read", "mail_label"}, []check{
		{"mail-001 exists", found},
		{"kickoff marked read", found && mail.Read},
		{"priority label added", found && has(mail.Labels, "priority")},
	}, "Project Atlas email triaged")
}

func evaluateMailDraft(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	found := false
	for _, mail := range world.Emails {
		if has(mail.Labels, "draft") && mail.To == "ops@example.test" && mail.Subject == "Re: Low stock: USB-C hub" && mail.Body == "Confirmed; reserving two units for Acme." {
			found = true
			break
		}
	}
	return checked(state, []string{"mail_search", "mail_get", "mail_create_draft"}, []check{{"expected draft exists", found}}, "Stock reply drafted")
}

func evaluateCalendarKickoff(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	found := false
	for _, event := range world.Events {
		if event.Title == "Project Atlas kickoff" && sameInstant(event.Start, "2026-09-03T10:00Z") && sameInstant(event.End, "2026-09-03T11:00Z") && has(event.Attendees, "alice@example.test") && has(event.Attendees, "bob@example.test") && !event.Cancelled {
			found = true
		}
	}
	return checked(state, []string{"calendar_find_slots", "calendar_create_event"}, []check{{"kickoff event exists", found}}, "Kickoff scheduled")
}

func evaluateCalendarDesign(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	event, found := eventByID(world, "event-002")
	return checked(state, []string{"calendar_list_events", "calendar_update_event", "calendar_add_attendee"}, []check{
		{"design review exists", found},
		{"time updated", found && sameInstant(event.Start, "2026-09-03T15:00Z") && sameInstant(event.End, "2026-09-03T16:00Z")},
		{"original attendee preserved", found && has(event.Attendees, "bob@example.test")},
		{"Alice added", found && has(event.Attendees, "alice@example.test")},
	}, "Design review updated")
}

func evaluateDocumentRisk(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	doc, found := documentByID(world, "doc-atlas")
	return checked(state, []string{"docs_search", "docs_read", "docs_append", "docs_tag"}, []check{
		{"brief exists", found},
		{"risk appended", found && strings.Contains(doc.Body, "Risk: dependency on tool availability.")},
		{"review tag added", found && has(doc.Tags, "review")},
	}, "Atlas brief updated")
}

func evaluateDocumentHandoff(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	found := false
	for _, doc := range world.Documents {
		if doc.Title == "Atlas handoff" && doc.Body == "Handoff checklist.\nOwner: Alice." {
			found = true
		}
	}
	return checked(state, []string{"docs_create", "docs_append"}, []check{{"handoff document exists", found}}, "Handoff document created")
}

func evaluateBudget(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	sheet, found := sheetByID(world, "sheet-budget")
	updated := false
	if found && len(sheet.Rows) > 1 {
		updated = sheet.Rows[1]["amount"] == "900"
	}
	return checked(state, []string{"sheet_list", "sheet_read_rows", "sheet_find_rows", "sheet_update_cell", "sheet_sum_column"}, []check{{"budget sheet exists", found}, {"pending design amount updated", updated}}, "Budget row updated")
}

func evaluateOrders(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	sheet, found := sheetByID(world, "sheet-orders")
	rowFound := false
	if found {
		for _, row := range sheet.Rows {
			if row["order"] == "order-002" && row["customer"] == "Acme" && row["sku"] == "HUB-01" && row["quantity"] == "1" && row["status"] == "pending" {
				rowFound = true
			}
		}
	}
	return checked(state, []string{"sheet_append_row", "sheet_read_rows"}, []check{{"order row appended", rowFound}}, "Order row appended")
}

func evaluateReservation(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	found := false
	for _, reservation := range world.Reservations {
		if reservation.SKU == "HUB-01" && reservation.Customer == "Acme" && reservation.Quantity == 2 && !reservation.Released {
			found = true
		}
	}
	return checked(state, []string{"inventory_search", "inventory_get_stock", "inventory_reserve"}, []check{{"Acme reservation exists", found}}, "Inventory reserved")
}

func evaluateReorder(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	product, found := productBySKU(world, "HUB-01")
	return checked(state, []string{"inventory_get_stock", "inventory_set_reorder_point", "inventory_list_reservations"}, []check{{"HUB-01 exists", found}, {"reorder point set", found && product.ReorderPoint == 6}}, "Reorder point updated")
}

func evaluateCRMFollowup(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	contact, found := contactByID(world, "contact-bob")
	activity := false
	for _, entry := range contact.Activities {
		if entry.Kind == "call" && entry.Note == "Discussed Atlas review" {
			activity = true
		}
	}
	taskFound := false
	for _, task := range world.CRMTasks {
		if task.ContactID == "contact-bob" && task.Title == "Send Atlas review" && task.Due == "2026-09-10" && !task.Done {
			taskFound = true
		}
	}
	return checked(state, []string{"crm_find_contact", "crm_get_contact", "crm_log_activity", "crm_create_task"}, []check{{"Bob exists", found}, {"activity logged", activity}, {"follow-up task exists", taskFound}}, "CRM follow-up created")
}

func evaluateCRMUpdate(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	contact, found := contactByID(world, "contact-alice")
	return checked(state, []string{"crm_find_contact", "crm_update_contact", "crm_list_tasks"}, []check{{"Alice exists", found}, {"phone updated", found && contact.Phone == "+1-555-0111"}, {"notes updated", found && contact.Notes == "Prefers email"}}, "Alice contact updated")
}

func evaluateFileReport(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	file, found := fileByPath(world, "reports/inventory.txt")
	return checked(state, []string{"files_search", "files_read", "files_write"}, []check{{"inventory report exists", found}, {"report content replaced", found && file.Content == "HUB-01 available: 3" && !file.Archived}}, "Inventory report refreshed")
}

func evaluateFileArchive(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	file, found := fileByPath(world, "archive/meeting.txt")
	return checked(state, []string{"files_list", "files_move", "files_archive"}, []check{{"meeting file moved", found}, {"moved file archived", found && file.Archived}}, "Meeting notes archived")
}

func evaluateResearchFinding(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	found := false
	for _, finding := range world.Findings {
		if finding.Topic == "tool-selection" && finding.Claim == "Narrow menus reduce schema context." && finding.SourceID == "source-001" && has(finding.Tags, "validated") {
			found = true
		}
	}
	return checked(state, []string{"research_search_notes", "research_get_source", "research_save_finding", "research_tag_finding"}, []check{{"validated finding exists", found}}, "Research finding saved")
}

func evaluateResearchSummary(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	found := false
	for _, finding := range world.Findings {
		if finding.Topic == "skill-loading" && finding.Claim == "Compact summaries defer detailed instructions until needed." && finding.SourceID == "source-003" {
			found = true
		}
	}
	return checked(state, []string{"research_search_notes", "research_get_source", "research_save_finding", "research_list_findings", "research_summarize"}, []check{{"skill-loading finding exists", found}}, "Research topic summarized")
}

func evaluateCrossMailCalendar(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	eventFound := false
	for _, event := range world.Events {
		if event.Title == "Atlas follow-up" && sameInstant(event.Start, "2026-09-04T10:00Z") && sameInstant(event.End, "2026-09-04T10:30Z") && has(event.Attendees, "alice@example.test") {
			eventFound = true
		}
	}
	return checked(state, []string{"mail_search", "calendar_create_event"}, []check{{"follow-up event exists", eventFound}}, "Cross-skill follow-up created")
}

func evaluateCrossInventoryOrders(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	sheet, sheetFound := sheetByID(world, "sheet-orders")
	rowFound := false
	if sheetFound {
		for _, row := range sheet.Rows {
			if row["customer"] == "Acme" && row["sku"] == "HUB-01" && row["quantity"] == "2" && row["status"] == "review" {
				rowFound = true
			}
		}
	}
	return checked(state, []string{"inventory_get_stock", "sheet_append_row"}, []check{{"restock note appended", rowFound}}, "Cross-skill inventory note appended")
}

type check struct {
	name string
	pass bool
}

func checked(state *bench.TaskState, required []string, checks []check, message string) bench.Evaluation {
	callPass := 0
	for _, requiredTool := range required {
		if successfulCall(state, requiredTool) {
			callPass++
		}
	}
	statePass := 0
	failedNames := make([]string, 0)
	for _, item := range checks {
		if item.pass {
			statePass++
		} else {
			failedNames = append(failedNames, item.name)
		}
	}
	callRatio := ratio(callPass, len(required))
	stateRatio := ratio(statePass, len(checks))
	// Final-state predicates define task success. Required tool names describe
	// the expected trajectory and remain useful for recall/precision analysis,
	// but a model should not fail a task merely because it used an equivalent
	// valid route (for example, it already knew a stable id and skipped search).
	// For a task with no state predicates, fall back to the trace contract.
	score := stateRatio
	passed := statePass == len(checks)
	if len(checks) == 0 {
		score = callRatio
		passed = callPass == len(required)
	}
	if len(failedNames) > 0 {
		message += "; failed checks: " + strings.Join(failedNames, ", ")
	}
	if !traceComplete(callPass, len(required)) {
		message += "; expected tool trace incomplete"
	}
	return bench.Evaluation{
		Passed: passed, Score: score, Message: message,
		RequiredCount: len(required), SatisfiedCount: callPass,
		CollateralCount: len(failedNames),
	}
}

func traceComplete(satisfied, required int) bool {
	return satisfied >= required
}

func failed(message string) bench.Evaluation {
	return bench.Evaluation{Message: message}
}

// sameInstant compares timestamps by their represented instant instead of
// their source spelling. ISO-8601 permits both minute precision ("10:00Z")
// and second precision ("10:00:00Z"); a final-state evaluator should not
// turn that harmless formatting choice into a task failure.
func sameInstant(left, right string) bool {
	parse := func(value string) (time.Time, error) {
		value = strings.TrimSpace(value)
		for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
			if parsed, err := time.Parse(layout, value); err == nil {
				return parsed, nil
			}
		}
		return time.Time{}, fmt.Errorf("invalid ISO-8601 timestamp %q", value)
	}
	leftTime, leftErr := parse(left)
	rightTime, rightErr := parse(right)
	return leftErr == nil && rightErr == nil && leftTime.Equal(rightTime)
}

func ratio(n, d int) float64 {
	if d == 0 {
		return 1
	}
	return float64(n) / float64(d)
}

func successfulCall(state *bench.TaskState, target string) bool {
	if state == nil {
		return false
	}
	for _, call := range state.SnapshotCalls() {
		if !call.Succeeded {
			continue
		}
		if call.Name == target || bench.ConventionalToolSetAlias(call.Name, target) {
			return true
		}
	}
	return false
}

func emailByID(state env.State, id string) (env.Email, bool) {
	for _, item := range state.Emails {
		if item.ID == id {
			return item, true
		}
	}
	return env.Email{}, false
}
func eventByID(state env.State, id string) (env.CalendarEvent, bool) {
	for _, item := range state.Events {
		if item.ID == id {
			return item, true
		}
	}
	return env.CalendarEvent{}, false
}
func documentByID(state env.State, id string) (env.Document, bool) {
	for _, item := range state.Documents {
		if item.ID == id {
			return item, true
		}
	}
	return env.Document{}, false
}
func sheetByID(state env.State, id string) (env.Sheet, bool) {
	for _, item := range state.Sheets {
		if item.ID == id {
			return item, true
		}
	}
	return env.Sheet{}, false
}
func productBySKU(state env.State, sku string) (env.Product, bool) {
	for _, item := range state.Products {
		if item.SKU == sku {
			return item, true
		}
	}
	return env.Product{}, false
}
func contactByID(state env.State, id string) (env.Contact, bool) {
	for _, item := range state.Contacts {
		if item.ID == id {
			return item, true
		}
	}
	return env.Contact{}, false
}
func fileByPath(state env.State, path string) (env.FileEntry, bool) {
	for _, item := range state.Files {
		if item.Path == path {
			return item, true
		}
	}
	return env.FileEntry{}, false
}
func has(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
