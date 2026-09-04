//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package env implements the deterministic, in-memory world used by
// ActivationBench-Lite.  It intentionally has no network, shell, clock, or
// random-number dependency.
package env

import "sort"

// Email is a local mailbox message.
type Email struct {
	ID       string   `json:"id"`
	From     string   `json:"from"`
	To       string   `json:"to"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body"`
	Labels   []string `json:"labels,omitempty"`
	Read     bool     `json:"read"`
	Archived bool     `json:"archived"`
}

// CalendarEvent is a local calendar event.
type CalendarEvent struct {
	ID        string   `json:"id"`
	Title     string   `json:"title"`
	Start     string   `json:"start"`
	End       string   `json:"end"`
	Attendees []string `json:"attendees,omitempty"`
	Cancelled bool     `json:"cancelled"`
}

// Document is a local document.
type Document struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags,omitempty"`
	Version int      `json:"version"`
}

// Sheet is a small local table.  Rows are keyed by column name.
type Sheet struct {
	ID      string              `json:"id"`
	Name    string              `json:"name"`
	Columns []string            `json:"columns"`
	Rows    []map[string]string `json:"rows"`
}

// Product is an inventory item.
type Product struct {
	SKU          string `json:"sku"`
	Name         string `json:"name"`
	Stock        int    `json:"stock"`
	ReorderPoint int    `json:"reorder_point"`
}

// Reservation is an inventory reservation.
type Reservation struct {
	ID       string `json:"id"`
	SKU      string `json:"sku"`
	Customer string `json:"customer"`
	Quantity int    `json:"quantity"`
	Released bool   `json:"released"`
}

// Contact is a CRM contact.
type Contact struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	Email      string     `json:"email"`
	Company    string     `json:"company"`
	Phone      string     `json:"phone"`
	Notes      string     `json:"notes"`
	Activities []Activity `json:"activities,omitempty"`
}

// Activity is an immutable CRM activity entry.
type Activity struct {
	Kind string `json:"kind"`
	Note string `json:"note"`
}

// CRMTask is a local follow-up task.
type CRMTask struct {
	ID        string `json:"id"`
	ContactID string `json:"contact_id"`
	Title     string `json:"title"`
	Due       string `json:"due"`
	Done      bool   `json:"done"`
}

// FileEntry is a file in the in-memory workspace.
type FileEntry struct {
	Path     string `json:"path"`
	Content  string `json:"content"`
	Archived bool   `json:"archived"`
}

// Source is a local research source.
type Source struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Text  string   `json:"text"`
	Tags  []string `json:"tags,omitempty"`
}

// Finding is a saved, source-grounded research claim.
type Finding struct {
	ID       string   `json:"id"`
	Topic    string   `json:"topic"`
	Claim    string   `json:"claim"`
	SourceID string   `json:"source_id"`
	Tags     []string `json:"tags,omitempty"`
}

// State is the complete benchmark world.  All slices are sorted by their
// stable identifiers in snapshots, making evaluator output reproducible.
type State struct {
	Emails       []Email         `json:"emails"`
	Events       []CalendarEvent `json:"events"`
	Documents    []Document      `json:"documents"`
	Sheets       []Sheet         `json:"sheets"`
	Products     []Product       `json:"products"`
	Reservations []Reservation   `json:"reservations"`
	Contacts     []Contact       `json:"contacts"`
	CRMTasks     []CRMTask       `json:"crm_tasks"`
	Files        []FileEntry     `json:"files"`
	Sources      []Source        `json:"sources"`
	Findings     []Finding       `json:"findings"`
}

// InitialState returns a fresh copy of the deterministic fixture.
func InitialState() State {
	return State{
		Emails: []Email{
			{ID: "mail-001", From: "alice@example.test", To: "team@example.test", Subject: "Project Atlas kickoff", Body: "Please schedule the Project Atlas kickoff for 2026-09-03T10:00Z with Bob and Alice.", Labels: []string{"inbox", "project-atlas"}},
			{ID: "mail-002", From: "ops@example.test", To: "team@example.test", Subject: "Low stock: USB-C hub", Body: "The USB-C hub (SKU HUB-01) has only three units left; customer Acme needs two.", Labels: []string{"inbox", "inventory"}},
			{ID: "mail-003", From: "bob@example.test", To: "alice@example.test", Subject: "Atlas brief edits", Body: "Please append the risk note to the Project Atlas brief and tag it review.", Labels: []string{"inbox"}},
			{ID: "mail-004", From: "research@example.test", To: "team@example.test", Subject: "Research source index", Body: "The source cards in the research notebook are ready for synthesis.", Labels: []string{"inbox", "research"}},
		},
		Events: []CalendarEvent{
			{ID: "event-001", Title: "Weekly planning", Start: "2026-09-03T09:00Z", End: "2026-09-03T09:30Z", Attendees: []string{"alice@example.test"}},
			{ID: "event-002", Title: "Design review", Start: "2026-09-03T14:00Z", End: "2026-09-03T15:00Z", Attendees: []string{"bob@example.test"}},
		},
		Documents: []Document{
			{ID: "doc-atlas", Title: "Project Atlas brief", Body: "Project Atlas delivers an internal workflow update.\nOwner: Alice.\n", Tags: []string{"project-atlas"}, Version: 1},
			{ID: "doc-runbook", Title: "Operations runbook", Body: "Check stock before promising shipment.\n", Tags: []string{"operations"}, Version: 2},
			{ID: "doc-research", Title: "Research notebook", Body: "Open questions are tracked next to each source.\n", Tags: []string{"research"}, Version: 1},
		},
		Sheets: []Sheet{
			{ID: "sheet-budget", Name: "Budget", Columns: []string{"item", "owner", "amount", "status"}, Rows: []map[string]string{
				{"item": "research", "owner": "alice", "amount": "1200", "status": "approved"},
				{"item": "design", "owner": "bob", "amount": "800", "status": "pending"},
			}},
			{ID: "sheet-orders", Name: "Orders", Columns: []string{"order", "customer", "sku", "quantity", "status"}, Rows: []map[string]string{
				{"order": "order-001", "customer": "Acme", "sku": "HUB-01", "quantity": "2", "status": "pending"},
			}},
		},
		Products: []Product{
			{SKU: "HUB-01", Name: "USB-C hub", Stock: 3, ReorderPoint: 5},
			{SKU: "STAND-02", Name: "Laptop stand", Stock: 12, ReorderPoint: 4},
			{SKU: "CAM-03", Name: "Web camera", Stock: 6, ReorderPoint: 3},
		},
		Reservations: nil,
		Contacts: []Contact{
			{ID: "contact-alice", Name: "Alice Chen", Email: "alice@example.test", Company: "Atlas Labs", Phone: "+1-555-0101"},
			{ID: "contact-bob", Name: "Bob Singh", Email: "bob@example.test", Company: "Atlas Labs", Phone: "+1-555-0102"},
			{ID: "contact-acme", Name: "Acme Procurement", Email: "buying@acme.example", Company: "Acme", Phone: "+1-555-0199"},
		},
		CRMTasks: nil,
		Files: []FileEntry{
			{Path: "briefs/atlas.txt", Content: "Project Atlas brief\nOwner: Alice\nStatus: draft\n"},
			{Path: "notes/meeting.txt", Content: "Kickoff notes: confirm attendees and budget.\n"},
			{Path: "reports/inventory.txt", Content: "Inventory report pending.\n"},
		},
		Sources: []Source{
			{ID: "source-001", Title: "Tool selection study", Text: "Narrow tool menus reduce schema context while preserving the task-relevant operations.", Tags: []string{"tools", "context"}},
			{ID: "source-002", Title: "Stateful evaluation note", Text: "Final state checks catch collateral changes that trajectory matching misses.", Tags: []string{"evaluation"}},
			{ID: "source-003", Title: "Skill loading note", Text: "A compact skill summary can defer detailed instructions until they are needed.", Tags: []string{"skills", "context"}},
		},
		Findings: nil,
	}
}

// Clone returns a deep copy of State.
func (s State) Clone() State {
	out := State{
		Emails:       append([]Email(nil), s.Emails...),
		Events:       append([]CalendarEvent(nil), s.Events...),
		Documents:    append([]Document(nil), s.Documents...),
		Sheets:       make([]Sheet, len(s.Sheets)),
		Products:     append([]Product(nil), s.Products...),
		Reservations: append([]Reservation(nil), s.Reservations...),
		Contacts:     make([]Contact, len(s.Contacts)),
		CRMTasks:     append([]CRMTask(nil), s.CRMTasks...),
		Files:        append([]FileEntry(nil), s.Files...),
		Sources:      append([]Source(nil), s.Sources...),
		Findings:     append([]Finding(nil), s.Findings...),
	}
	for i, sheet := range s.Sheets {
		out.Sheets[i] = Sheet{ID: sheet.ID, Name: sheet.Name, Columns: append([]string(nil), sheet.Columns...), Rows: make([]map[string]string, len(sheet.Rows))}
		for j, row := range sheet.Rows {
			out.Sheets[i].Rows[j] = cloneStringMap(row)
		}
	}
	for i, contact := range s.Contacts {
		out.Contacts[i] = contact
		out.Contacts[i].Activities = append([]Activity(nil), contact.Activities...)
	}
	for i := range out.Emails {
		out.Emails[i].Labels = append([]string(nil), out.Emails[i].Labels...)
	}
	for i := range out.Events {
		out.Events[i].Attendees = append([]string(nil), out.Events[i].Attendees...)
	}
	for i := range out.Documents {
		out.Documents[i].Tags = append([]string(nil), out.Documents[i].Tags...)
	}
	for i := range out.Sources {
		out.Sources[i].Tags = append([]string(nil), out.Sources[i].Tags...)
	}
	for i := range out.Findings {
		out.Findings[i].Tags = append([]string(nil), out.Findings[i].Tags...)
	}
	return out
}

// Stable sorts all state collections by their canonical identifier.  It is
// exported for custom task setup code that appends records.
func (s *State) Stable() {
	if s == nil {
		return
	}
	sort.Slice(s.Emails, func(i, j int) bool { return s.Emails[i].ID < s.Emails[j].ID })
	sort.Slice(s.Events, func(i, j int) bool { return s.Events[i].ID < s.Events[j].ID })
	sort.Slice(s.Documents, func(i, j int) bool { return s.Documents[i].ID < s.Documents[j].ID })
	sort.Slice(s.Sheets, func(i, j int) bool { return s.Sheets[i].ID < s.Sheets[j].ID })
	sort.Slice(s.Products, func(i, j int) bool { return s.Products[i].SKU < s.Products[j].SKU })
	sort.Slice(s.Reservations, func(i, j int) bool { return s.Reservations[i].ID < s.Reservations[j].ID })
	sort.Slice(s.Contacts, func(i, j int) bool { return s.Contacts[i].ID < s.Contacts[j].ID })
	sort.Slice(s.CRMTasks, func(i, j int) bool { return s.CRMTasks[i].ID < s.CRMTasks[j].ID })
	sort.Slice(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
	sort.Slice(s.Sources, func(i, j int) bool { return s.Sources[i].ID < s.Sources[j].ID })
	sort.Slice(s.Findings, func(i, j int) bool { return s.Findings[i].ID < s.Findings[j].ID })
}

func cloneStringMap(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}
