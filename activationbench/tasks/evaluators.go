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
	"reflect"
	"sort"
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
	checks := []check{
		{"mail-001 exists", found},
		{"kickoff marked read", found && mail.Read},
		{"priority label added", found && has(mail.Labels, "priority")},
	}
	checks = addObservedResponseContains(state, checks, "final message id confirmed", "mail-001")
	return checked(state, world, expectedWorld("mail-triage-atlas"), []string{"mail_search", "mail_mark_read", "mail_label"}, checks, "Project Atlas email triaged")
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
	return checked(state, world, expectedWorld("mail-draft-stock"), []string{"mail_search", "mail_get", "mail_create_draft"}, []check{{"expected draft exists", found}}, "Stock reply drafted")
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
	return checked(state, world, expectedWorld("calendar-schedule-kickoff"), []string{"calendar_find_slots", "calendar_create_event"}, []check{{"kickoff event exists", found}}, "Kickoff scheduled")
}

func evaluateCalendarDesign(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	event, found := eventByID(world, "event-002")
	return checked(state, world, expectedWorld("calendar-update-design"), []string{"calendar_list_events", "calendar_update_event", "calendar_add_attendee"}, []check{
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
	return checked(state, world, expectedWorld("documents-append-risk"), []string{"docs_search", "docs_read", "docs_append", "docs_tag"}, []check{
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
	return checked(state, world, expectedWorld("documents-create-handoff"), []string{"docs_create", "docs_append"}, []check{{"handoff document exists", found}}, "Handoff document created")
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
	checks := []check{{"budget sheet exists", found}, {"pending design amount updated", updated}}
	checks = append(checks, check{"amount sum computed", successfulCall(state, "sheet_sum_column") || responseContainsNumber(state, "2100")})
	return checked(state, world, expectedWorld("spreadsheets-budget"), []string{"sheet_list", "sheet_read_rows", "sheet_find_rows", "sheet_update_cell", "sheet_sum_column"}, checks, "Budget row updated")
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
	checks := []check{{"order row appended", rowFound}, {"order rows inspected", successfulCall(state, "sheet_read_rows") || responseContains(state, "order-002")}}
	return checked(state, world, expectedWorld("spreadsheets-orders"), []string{"sheet_append_row", "sheet_read_rows"}, checks, "Order row appended")
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
	return checked(state, world, expectedWorld("inventory-reserve"), []string{"inventory_search", "inventory_get_stock", "inventory_reserve"}, []check{{"Acme reservation exists", found}}, "Inventory reserved")
}

func evaluateReorder(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	product, found := productBySKU(world, "HUB-01")
	checks := []check{{"HUB-01 exists", found}, {"reorder point set", found && product.ReorderPoint == 6}, {"reservations inspected", successfulCall(state, "inventory_list_reservations") || responseShowsNone(state, "reservation")}}
	return checked(state, world, expectedWorld("inventory-reorder"), []string{"inventory_get_stock", "inventory_set_reorder_point", "inventory_list_reservations"}, checks, "Reorder point updated")
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
	return checked(state, world, expectedWorld("crm-followup"), []string{"crm_find_contact", "crm_get_contact", "crm_log_activity", "crm_create_task"}, []check{{"Bob exists", found}, {"activity logged", activity}, {"follow-up task exists", taskFound}}, "CRM follow-up created")
}

func evaluateCRMUpdate(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	contact, found := contactByID(world, "contact-alice")
	checks := []check{{"Alice exists", found}, {"phone updated", found && contact.Phone == "+1-555-0111"}, {"notes updated", found && contact.Notes == "Prefers email"}, {"open tasks inspected", successfulCall(state, "crm_list_tasks") || responseShowsNone(state, "task")}}
	return checked(state, world, expectedWorld("crm-update-alice"), []string{"crm_find_contact", "crm_update_contact", "crm_list_tasks"}, checks, "Alice contact updated")
}

func evaluateFileReport(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	file, found := fileByPath(world, "reports/inventory.txt")
	return checked(state, world, expectedWorld("files-refresh-report"), []string{"files_search", "files_read", "files_write"}, []check{{"inventory report exists", found}, {"report content replaced", found && file.Content == "HUB-01 available: 3" && !file.Archived}}, "Inventory report refreshed")
}

func evaluateFileArchive(state *bench.TaskState) bench.Evaluation {
	world, ok := stateWorld(state)
	if !ok {
		return failed("missing benchmark world")
	}
	file, found := fileByPath(world, "archive/meeting.txt")
	return checked(state, world, expectedWorld("files-archive-meeting"), []string{"files_list", "files_move", "files_archive"}, []check{{"meeting file moved", found}, {"moved file archived", found && file.Archived}}, "Meeting notes archived")
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
	return checked(state, world, expectedWorld("research-save-finding"), []string{"research_search_notes", "research_get_source", "research_save_finding", "research_tag_finding"}, []check{{"validated finding exists", found}}, "Research finding saved")
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
	checks := []check{{"skill-loading finding exists", found}, {"topic summarized", successfulCall(state, "research_summarize") || responseContains(state, "Compact summaries defer detailed instructions until needed.")}}
	return checked(state, world, expectedWorld("research-summarize"), []string{"research_search_notes", "research_get_source", "research_save_finding", "research_list_findings", "research_summarize"}, checks, "Research topic summarized")
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
	return checked(state, world, expectedWorld("cross-mail-calendar"), []string{"mail_search", "calendar_create_event"}, []check{{"follow-up event exists", eventFound}}, "Cross-skill follow-up created")
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
	return checked(state, world, expectedCrossInventoryWorld(world), []string{"inventory_get_stock", "sheet_append_row"}, []check{{"restock note appended", rowFound}}, "Cross-skill inventory note appended")
}

type check struct {
	name string
	pass bool
}

func expectedWorld(taskID string) env.State {
	world := env.InitialState()
	switch taskID {
	case "mail-triage-atlas":
		mail := mustEmail(&world, "mail-001")
		mail.Read = true
		mail.Labels = append(mail.Labels, "priority")
	case "mail-draft-stock":
		world.Emails = append(world.Emails, env.Email{
			ID: "mail-draft-001", From: "agent@example.test", To: "ops@example.test",
			Subject: "Re: Low stock: USB-C hub", Body: "Confirmed; reserving two units for Acme.",
			Labels: []string{"draft"},
		})
	case "calendar-schedule-kickoff":
		world.Events = append(world.Events, env.CalendarEvent{
			ID: "event-003", Title: "Project Atlas kickoff",
			Start: "2026-09-03T10:00Z", End: "2026-09-03T11:00Z",
			Attendees: []string{"alice@example.test", "bob@example.test"},
		})
	case "calendar-update-design":
		event := mustEvent(&world, "event-002")
		event.Start = "2026-09-03T15:00Z"
		event.End = "2026-09-03T16:00Z"
		event.Attendees = []string{"alice@example.test", "bob@example.test"}
	case "documents-append-risk":
		doc := mustDocument(&world, "doc-atlas")
		doc.Body += "Risk: dependency on tool availability."
		doc.Tags = append(doc.Tags, "review")
		doc.Version = 2
	case "documents-create-handoff":
		world.Documents = append(world.Documents, env.Document{
			ID: "doc-001", Title: "Atlas handoff",
			Body: "Handoff checklist.\nOwner: Alice.", Version: 2,
		})
	case "spreadsheets-budget":
		mustSheet(&world, "sheet-budget").Rows[1]["amount"] = "900"
	case "spreadsheets-orders":
		sheet := mustSheet(&world, "sheet-orders")
		sheet.Rows = append(sheet.Rows, map[string]string{
			"order": "order-002", "customer": "Acme", "sku": "HUB-01",
			"quantity": "1", "status": "pending",
		})
	case "inventory-reserve":
		world.Reservations = append(world.Reservations, env.Reservation{
			ID: "reservation-001", SKU: "HUB-01", Customer: "Acme", Quantity: 2,
		})
	case "inventory-reorder":
		mustProduct(&world, "HUB-01").ReorderPoint = 6
	case "crm-followup":
		contact := mustContact(&world, "contact-bob")
		contact.Activities = append(contact.Activities, env.Activity{Kind: "call", Note: "Discussed Atlas review"})
		world.CRMTasks = append(world.CRMTasks, env.CRMTask{
			ID: "crm-task-001", ContactID: "contact-bob", Title: "Send Atlas review", Due: "2026-09-10",
		})
	case "crm-update-alice":
		contact := mustContact(&world, "contact-alice")
		contact.Phone = "+1-555-0111"
		contact.Notes = "Prefers email"
	case "files-refresh-report":
		file := mustFile(&world, "reports/inventory.txt")
		file.Content = "HUB-01 available: 3"
		file.Archived = false
	case "files-archive-meeting":
		file := mustFile(&world, "notes/meeting.txt")
		file.Path = "archive/meeting.txt"
		file.Archived = true
	case "research-save-finding":
		world.Findings = append(world.Findings, env.Finding{
			ID: "finding-001", Topic: "tool-selection",
			Claim: "Narrow menus reduce schema context.", SourceID: "source-001",
			Tags: []string{"validated"},
		})
	case "research-summarize":
		world.Findings = append(world.Findings, env.Finding{
			ID: "finding-001", Topic: "skill-loading",
			Claim: "Compact summaries defer detailed instructions until needed.", SourceID: "source-003",
		})
	case "cross-mail-calendar":
		world.Events = append(world.Events, env.CalendarEvent{
			ID: "event-003", Title: "Atlas follow-up",
			Start: "2026-09-04T10:00Z", End: "2026-09-04T10:30Z",
			Attendees: []string{"alice@example.test"},
		})
	case "cross-inventory-orders":
		sheet := mustSheet(&world, "sheet-orders")
		sheet.Rows = append(sheet.Rows, map[string]string{
			"customer": "Acme", "sku": "HUB-01", "quantity": "2", "status": "review",
		})
	default:
		panic(fmt.Sprintf("unknown built-in task %q", taskID))
	}
	return world
}

func expectedCrossInventoryWorld(actual env.State) env.State {
	expected := expectedWorld("cross-inventory-orders")
	expectedSheet := mustSheet(&expected, "sheet-orders")
	expectedRow := expectedSheet.Rows[len(expectedSheet.Rows)-1]
	for _, sheet := range actual.Sheets {
		if sheet.ID != "sheet-orders" {
			continue
		}
		for _, row := range sheet.Rows {
			if row["customer"] == "Acme" && row["sku"] == "HUB-01" && row["quantity"] == "2" && row["status"] == "review" {
				if order, ok := row["order"]; ok {
					expectedRow["order"] = order
				}
				return expected
			}
		}
	}
	return expected
}

func mustEmail(world *env.State, id string) *env.Email {
	for index := range world.Emails {
		if world.Emails[index].ID == id {
			return &world.Emails[index]
		}
	}
	panic(fmt.Sprintf("missing fixture email %q", id))
}

func mustEvent(world *env.State, id string) *env.CalendarEvent {
	for index := range world.Events {
		if world.Events[index].ID == id {
			return &world.Events[index]
		}
	}
	panic(fmt.Sprintf("missing fixture event %q", id))
}

func mustDocument(world *env.State, id string) *env.Document {
	for index := range world.Documents {
		if world.Documents[index].ID == id {
			return &world.Documents[index]
		}
	}
	panic(fmt.Sprintf("missing fixture document %q", id))
}

func mustSheet(world *env.State, id string) *env.Sheet {
	for index := range world.Sheets {
		if world.Sheets[index].ID == id {
			return &world.Sheets[index]
		}
	}
	panic(fmt.Sprintf("missing fixture sheet %q", id))
}

func mustProduct(world *env.State, sku string) *env.Product {
	for index := range world.Products {
		if world.Products[index].SKU == sku {
			return &world.Products[index]
		}
	}
	panic(fmt.Sprintf("missing fixture product %q", sku))
}

func mustContact(world *env.State, id string) *env.Contact {
	for index := range world.Contacts {
		if world.Contacts[index].ID == id {
			return &world.Contacts[index]
		}
	}
	panic(fmt.Sprintf("missing fixture contact %q", id))
}

func mustFile(world *env.State, path string) *env.FileEntry {
	for index := range world.Files {
		if world.Files[index].Path == path {
			return &world.Files[index]
		}
	}
	panic(fmt.Sprintf("missing fixture file %q", path))
}

func addObservedResponseContains(state *bench.TaskState, checks []check, name string, value string) []check {
	if _, observed := state.FinalResponse(); !observed {
		return checks
	}
	return append(checks, check{name: name, pass: responseContains(state, value)})
}

func responseContains(state *bench.TaskState, value string) bool {
	response, observed := state.FinalResponse()
	return observed && strings.Contains(strings.ToLower(response), strings.ToLower(value))
}

func responseContainsNumber(state *bench.TaskState, value string) bool {
	response, observed := state.FinalResponse()
	if !observed {
		return false
	}
	normalized := strings.NewReplacer(",", "", "_", "").Replace(response)
	return strings.Contains(normalized, value)
}

func responseShowsNone(state *bench.TaskState, item string) bool {
	response, observed := state.FinalResponse()
	if !observed {
		return false
	}
	response = strings.ToLower(response)
	item = strings.ToLower(item)
	for _, marker := range []string{"no " + item, "no open " + item, "0 " + item, "zero " + item, "none"} {
		if strings.Contains(response, marker) {
			return true
		}
	}
	return false
}

func collateralChanges(initial, expected, actual env.State) int {
	initial = canonicalWorld(initial)
	expected = canonicalWorld(expected)
	actual = canonicalWorld(actual)
	return collateralRecords(initial.Emails, expected.Emails, actual.Emails, func(item env.Email) string { return item.ID }) +
		collateralRecords(initial.Events, expected.Events, actual.Events, func(item env.CalendarEvent) string { return item.ID }) +
		collateralRecords(initial.Documents, expected.Documents, actual.Documents, func(item env.Document) string { return item.ID }) +
		collateralRecords(initial.Sheets, expected.Sheets, actual.Sheets, func(item env.Sheet) string { return item.ID }) +
		collateralRecords(initial.Products, expected.Products, actual.Products, func(item env.Product) string { return item.SKU }) +
		collateralRecords(initial.Reservations, expected.Reservations, actual.Reservations, func(item env.Reservation) string { return item.ID }) +
		collateralRecords(initial.Contacts, expected.Contacts, actual.Contacts, func(item env.Contact) string { return item.ID }) +
		collateralRecords(initial.CRMTasks, expected.CRMTasks, actual.CRMTasks, func(item env.CRMTask) string { return item.ID }) +
		collateralRecords(initial.Files, expected.Files, actual.Files, func(item env.FileEntry) string { return item.Path }) +
		collateralRecords(initial.Sources, expected.Sources, actual.Sources, func(item env.Source) string { return item.ID }) +
		collateralRecords(initial.Findings, expected.Findings, actual.Findings, func(item env.Finding) string { return item.ID })
}

func collateralRecords[T any](initial, expected, actual []T, key func(T) string) int {
	initialByKey, initialDuplicates := recordsByKey(initial, key)
	expectedByKey, expectedDuplicates := recordsByKey(expected, key)
	actualByKey, actualDuplicates := recordsByKey(actual, key)
	keys := make(map[string]struct{}, len(initialByKey)+len(expectedByKey)+len(actualByKey))
	for name := range initialByKey {
		keys[name] = struct{}{}
	}
	for name := range expectedByKey {
		keys[name] = struct{}{}
	}
	for name := range actualByKey {
		keys[name] = struct{}{}
	}
	count := initialDuplicates + expectedDuplicates + actualDuplicates
	for name := range keys {
		actualValue, actualOK := actualByKey[name]
		expectedValue, expectedOK := expectedByKey[name]
		initialValue, initialOK := initialByKey[name]
		if optionalRecordEqual(actualValue, actualOK, expectedValue, expectedOK) ||
			optionalRecordEqual(actualValue, actualOK, initialValue, initialOK) {
			continue
		}
		count++
	}
	return count
}

func recordsByKey[T any](records []T, key func(T) string) (map[string]T, int) {
	byKey := make(map[string]T, len(records))
	duplicates := 0
	for _, record := range records {
		name := key(record)
		if _, exists := byKey[name]; exists {
			duplicates++
		}
		byKey[name] = record
	}
	return byKey, duplicates
}

func optionalRecordEqual[T any](left T, leftOK bool, right T, rightOK bool) bool {
	return leftOK == rightOK && (!leftOK || reflect.DeepEqual(left, right))
}

func canonicalWorld(world env.State) env.State {
	world = world.Clone()
	for index := range world.Emails {
		sort.Strings(world.Emails[index].Labels)
	}
	for index := range world.Events {
		world.Events[index].Start = canonicalInstant(world.Events[index].Start)
		world.Events[index].End = canonicalInstant(world.Events[index].End)
		sort.Strings(world.Events[index].Attendees)
	}
	for index := range world.Documents {
		sort.Strings(world.Documents[index].Tags)
	}
	for index := range world.Sources {
		sort.Strings(world.Sources[index].Tags)
	}
	for index := range world.Findings {
		sort.Strings(world.Findings[index].Tags)
	}
	world.Stable()
	return world
}

func canonicalInstant(value string) string {
	value = strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed.UTC().Format(time.RFC3339Nano)
		}
	}
	return value
}

func checked(state *bench.TaskState, world, expected env.State, required []string, checks []check, message string) bench.Evaluation {
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
	collateralCount := collateralChanges(env.InitialState(), expected, world)
	// 任务通过需要满足目标、最终回复和状态变化范围。
	// Required tool 记录仅用于 recall/precision 分析，允许等价调用路径。
	score := stateRatio
	if collateralCount > 0 {
		score = ratio(statePass, len(checks)+collateralCount)
	}
	passed := statePass == len(checks) && collateralCount == 0
	if len(checks) == 0 {
		score = callRatio
		passed = callPass == len(required) && collateralCount == 0
	}
	if len(failedNames) > 0 {
		message += "; failed checks: " + strings.Join(failedNames, ", ")
	}
	if !traceComplete(callPass, len(required)) {
		message += "; expected tool trace incomplete"
	}
	if collateralCount > 0 {
		message += fmt.Sprintf("; unexpected state changes: %d", collateralCount)
	}
	return bench.Evaluation{
		Passed: passed, Score: score, Message: message,
		RequiredCount: len(required), SatisfiedCount: callPass,
		CollateralCount: collateralCount,
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
