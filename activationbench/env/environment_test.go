//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package env

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/catalog"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
)

func TestEnvironmentMailMutationAndReset(t *testing.T) {
	e := New()
	if _, err := e.Execute(context.Background(), "mail_mark_read", map[string]any{"id": "mail-001", "read": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), "mail_label", map[string]any{"id": "mail-001", "label": "priority", "add": true}); err != nil {
		t.Fatal(err)
	}
	snapshot := e.Snapshot()
	if !snapshot.State.Emails[0].Read {
		t.Fatalf("mail was not marked read: %+v", snapshot.State.Emails[0])
	}
	if len(snapshot.Calls) != 2 || !snapshot.Calls[1].Succeeded {
		t.Fatalf("unexpected call log: %+v", snapshot.Calls)
	}
	e.Reset()
	reset := e.Snapshot()
	if reset.State.Emails[0].Read || len(reset.State.Emails[0].Labels) != 2 {
		t.Fatalf("reset did not restore fixture: %+v", reset.State.Emails[0])
	}
	if len(reset.Calls) != 0 {
		t.Fatalf("reset retained calls: %+v", reset.Calls)
	}
}

func TestEnvironmentInventoryRejectsOverReservation(t *testing.T) {
	e := New()
	if _, err := e.Execute(context.Background(), "inventory_reserve", map[string]any{"sku": "HUB-01", "quantity": 3, "customer": "Acme"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), "inventory_reserve", map[string]any{"sku": "HUB-01", "quantity": 1, "customer": "Other"}); err == nil {
		t.Fatal("expected insufficient stock error")
	}
	snapshot := e.Snapshot()
	if len(snapshot.State.Reservations) != 1 || len(snapshot.Calls) != 2 || snapshot.Calls[1].Succeeded {
		t.Fatalf("reservation/error log mismatch: %+v / %+v", snapshot.State.Reservations, snapshot.Calls)
	}
}

func TestEnvironmentAcceptsPrefixedToolAndKeepsDistractorReadOnly(t *testing.T) {
	e := New()
	if _, err := e.Execute(context.Background(), "mail-tools_mail_get", map[string]any{"id": "mail-001"}); err != nil {
		t.Fatalf("prefixed name: %v", err)
	}
	before := e.State()
	if _, err := e.Execute(context.Background(), "mail_export", map[string]any{"format": "csv"}); err != nil {
		t.Fatalf("read-only distractor: %v", err)
	}
	after := e.State()
	if len(after.Emails) != len(before.Emails) || len(after.Documents) != len(before.Documents) {
		t.Fatal("distractor changed state")
	}
	if calls := e.Calls(); len(calls) != 2 || !calls[1].Succeeded {
		t.Fatalf("distractor call was not recorded as successful: %+v", calls)
	}
}

func TestEnvironmentRejectsPrefixFromAnotherToolSet(t *testing.T) {
	c, err := catalog.New(
		[]catalog.SkillSpec{
			{ID: "mail", Name: "Mail", ToolSetID: "mail-tools"},
			{ID: "calendar", Name: "Calendar", ToolSetID: "calendar-tools"},
		},
		[]catalog.ToolSetSpec{
			{ID: "mail-tools", Name: "mail-tools", SkillID: "mail"},
			{ID: "calendar-tools", Name: "calendar-tools", SkillID: "calendar"},
		},
		[]catalog.ToolSpec{
			{Name: "mail_search", SkillID: "mail", ToolSetID: "mail-tools", InputSchema: &trpctool.Schema{Type: "object"}},
			{Name: "calendar_list_events", SkillID: "calendar", ToolSetID: "calendar-tools", InputSchema: &trpctool.Schema{Type: "object"}},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	e := New(c)
	if _, err := e.Execute(context.Background(), "calendar-tools_mail_search", map[string]any{"query": "Atlas"}); err == nil {
		t.Fatal("a prefix from another ToolSet must not alias mail_search")
	}
	if calls := e.Calls(); len(calls) != 1 || calls[0].Succeeded {
		t.Fatalf("unexpected cross-set call record: %+v", calls)
	}
}

func TestEnvironmentUpdatesAreTransactional(t *testing.T) {
	e := New()
	before := e.State()
	if _, err := e.Execute(context.Background(), "calendar_update_event", map[string]any{
		"id": "event-002", "start": "2026-09-03T16:00Z", "end": 123,
	}); err == nil {
		t.Fatal("expected invalid calendar end argument")
	}
	if after := e.State(); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed calendar update changed state: before=%+v after=%+v", before, after)
	}

	before = e.State()
	if _, err := e.Execute(context.Background(), "crm_update_contact", map[string]any{
		"id": "contact-alice", "phone": "+1-555-0111", "notes": 123,
	}); err == nil {
		t.Fatal("expected invalid CRM notes argument")
	}
	if after := e.State(); !reflect.DeepEqual(after, before) {
		t.Fatalf("failed CRM update changed state: before=%+v after=%+v", before, after)
	}
}

func TestEnvironmentUpdatesNilSheetRowsWithoutPanic(t *testing.T) {
	e := New()
	if err := e.Mutate(func(state *State) error {
		state.Sheets[0].Rows[0] = nil
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Execute(context.Background(), "sheet_update_cell", map[string]any{
		"sheet_id": "sheet-budget", "row": 0, "column": "amount", "value": "123",
	}); err != nil {
		t.Fatalf("nil row update: %v", err)
	}
	if got := e.State().Sheets[0].Rows[0]["amount"]; got != "123" {
		t.Fatalf("updated nil row value = %q, want 123", got)
	}
}

func TestEnvironmentSheetAppendRowNormalizesNumericCells(t *testing.T) {
	e := New()
	_, err := e.Execute(context.Background(), "sheet_append_row", map[string]any{
		"sheet_id": "sheet-orders",
		"values":   `{"order":"order-002","customer":"Acme","sku":"HUB-01","quantity":1,"status":"pending"}`,
	})
	if err != nil {
		t.Fatalf("numeric cell value should be accepted: %v", err)
	}
	sheet, ok := findSheet(e.State().Sheets, "sheet-orders")
	if !ok || len(sheet.Rows) == 0 {
		t.Fatalf("order row was not appended: %+v", e.State().Sheets)
	}
	row := sheet.Rows[len(sheet.Rows)-1]
	if row["quantity"] != "1" {
		t.Fatalf("normalized quantity = %q, want 1", row["quantity"])
	}
}

func TestEnvironmentCallArgumentsAreDeepCopied(t *testing.T) {
	e := New()
	args := map[string]any{
		"nested": map[string]any{
			"items": []any{"before", map[string]any{"value": "original"}},
		},
	}
	if _, err := e.Execute(context.Background(), "unknown_tool", args); err == nil {
		t.Fatal("expected unknown tool error")
	}
	args["nested"].(map[string]any)["items"].([]any)[0] = "caller-mutated"
	args["nested"].(map[string]any)["items"].([]any)[1].(map[string]any)["value"] = "caller-mutated"

	calls := e.Calls()
	got := calls[0].Arguments["nested"].(map[string]any)
	items := got["items"].([]any)
	if items[0] != "before" || items[1].(map[string]any)["value"] != "original" {
		t.Fatalf("call log aliased caller arguments: %#v", calls[0].Arguments)
	}
	items[0] = "snapshot-mutated"
	items[1].(map[string]any)["value"] = "snapshot-mutated"
	again := e.Calls()[0].Arguments["nested"].(map[string]any)["items"].([]any)
	if again[0] != "before" || again[1].(map[string]any)["value"] != "original" {
		t.Fatalf("Calls returned an aliased nested value: %#v", e.Calls()[0].Arguments)
	}
}

func TestEnvironmentGeneratedIDsContinueFromRestoredState(t *testing.T) {
	e := New()
	if err := e.Mutate(func(state *State) error {
		state.Documents = append(state.Documents, Document{ID: "doc-007", Title: "existing"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	response, err := e.Execute(context.Background(), "docs_create", map[string]any{
		"title": "new", "body": "body",
	})
	if err != nil {
		t.Fatal(err)
	}
	document, ok := response.Data.(Document)
	if !ok || document.ID != "doc-008" {
		t.Fatalf("generated document = %#v, want doc-008", response.Data)
	}
}

func TestEnvironmentContextCancellation(t *testing.T) {
	e := New()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Execute(ctx, "mail_search", map[string]any{"query": "Atlas"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("want context canceled, got %v", err)
	}
}

func TestEnvironmentAcceptsCanonicalAndDisplayPrefixes(t *testing.T) {
	c, err := catalog.New(
		[]catalog.SkillSpec{{ID: "mail", Name: "Mail", ToolSetID: "mail-runtime"}},
		[]catalog.ToolSetSpec{{ID: "mail-runtime", Name: "Mail display name", SkillID: "mail"}},
		[]catalog.ToolSpec{{
			Name: "mail_search", SkillID: "mail", ToolSetID: "mail-runtime",
			InputSchema: &trpctool.Schema{Type: "object"},
		}},
	)
	if err != nil {
		t.Fatal(err)
	}
	e := New(c)
	response, err := e.Execute(context.Background(), "mail-runtime_mail_search", map[string]any{"query": "Atlas"})
	if err != nil || response.Tool != "mail_search" {
		t.Fatalf("canonical prefixed execute = %#v, err=%v", response, err)
	}
	response, err = e.Execute(context.Background(), "Mail display name_mail_search", map[string]any{"query": "Atlas"})
	if err != nil || response.Tool != "mail_search" {
		t.Fatalf("display-name prefixed compatibility execute = %#v, err=%v", response, err)
	}
}
