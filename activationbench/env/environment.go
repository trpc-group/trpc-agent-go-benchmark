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
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/activationbench/catalog"
)

// CallRecord captures every attempted tool call.  It is intentionally small
// enough to serialize as JSONL and rich enough for wrong-tool and activation
// diagnostics.
type CallRecord struct {
	Sequence  int            `json:"sequence"`
	ToolName  string         `json:"tool_name"`
	Arguments map[string]any `json:"arguments,omitempty"`
	Succeeded bool           `json:"succeeded"`
	Error     string         `json:"error,omitempty"`
}

// Snapshot is an immutable-by-convention view used by task evaluators.
type Snapshot struct {
	State State        `json:"state"`
	Calls []CallRecord `json:"calls"`
}

// ToolResponse is the stable model-visible shape returned by successful
// fixture tools.
type ToolResponse struct {
	Tool string `json:"tool"`
	Data any    `json:"data,omitempty"`
}

// Environment is a deterministic, thread-safe in-memory benchmark world.
// Every Reset restores exactly InitialState and clears call history.
type Environment struct {
	mu      sync.Mutex
	catalog *catalog.Catalog
	initial State
	state   State
	calls   []CallRecord
	nextIDs map[string]int
}

// New creates an environment with the default 8-skill/64-tool catalog.  A
// custom catalog may be supplied for scale experiments; state semantics stay
// unchanged.
func New(custom ...*catalog.Catalog) *Environment {
	c := catalog.Default()
	if len(custom) > 0 && custom[0] != nil {
		c = custom[0]
	}
	initial := InitialState()
	return NewWithStateAndCatalog(initial, c)
}

// NewWithState creates an environment whose initial state is a caller-owned
// fixture.  The state is cloned immediately, so subsequent caller mutation is
// safe.  It is useful for task handlers that keep a world inside a generic
// benchmark state map.
func NewWithState(initial State, custom ...*catalog.Catalog) *Environment {
	c := catalog.Default()
	if len(custom) > 0 && custom[0] != nil {
		c = custom[0]
	}
	return NewWithStateAndCatalog(initial, c)
}

// NewWithStateAndCatalog is the explicit constructor used when both a custom
// catalog and a custom state are supplied.
func NewWithStateAndCatalog(initial State, c *catalog.Catalog) *Environment {
	if c == nil {
		c = catalog.Default()
	}
	initial = initial.Clone()
	return &Environment{
		catalog: c,
		initial: initial.Clone(),
		state:   initial.Clone(),
		nextIDs: make(map[string]int),
	}
}

// NewWithCatalog is kept as a compatibility spelling for callers that
// construct scaled catalogs. New(c) is the preferred form.
//
// Deprecated: use New(c).
func NewWithCatalog(c *catalog.Catalog) *Environment { return New(c) }

// Catalog returns the environment's immutable capability catalog.
func (e *Environment) Catalog() *catalog.Catalog {
	if e == nil {
		return nil
	}
	return e.catalog
}

// State returns a deep copy of the current world without call history.
func (e *Environment) State() State {
	if e == nil {
		return State{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state.Clone()
}

// SetState replaces the current world with a deep copy and clears generated
// ids.  It is primarily useful for deterministic task setup and replay.
func (e *Environment) SetState(state State) {
	if e == nil {
		return
	}
	e.mu.Lock()
	e.state = state.Clone()
	e.nextIDs = make(map[string]int)
	e.mu.Unlock()
}

// Reset restores the deterministic initial state and clears call history.
func (e *Environment) Reset() {
	if e == nil {
		return
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.state = e.initial.Clone()
	e.calls = nil
	e.nextIDs = make(map[string]int)
}

// Mutate applies a caller-owned setup function while holding the environment
// lock.  It is intended for task setup only; setup code should not call
// Execute recursively.  State is normalized after the callback.
func (e *Environment) Mutate(fn func(*State) error) error {
	if e == nil {
		return errors.New("activationbench: nil environment")
	}
	if fn == nil {
		return errors.New("activationbench: nil state mutation")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := fn(&e.state); err != nil {
		return err
	}
	e.state.Stable()
	return nil
}

// Snapshot returns a deep copy of current state and call history.
func (e *Environment) Snapshot() Snapshot {
	if e == nil {
		return Snapshot{}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	out := Snapshot{State: e.state.Clone(), Calls: make([]CallRecord, len(e.calls))}
	for i, call := range e.calls {
		out.Calls[i] = call
		out.Calls[i].Arguments = cloneArgs(call.Arguments)
	}
	return out
}

// Calls returns a deep copy of the call log.
func (e *Environment) Calls() []CallRecord { return append([]CallRecord(nil), e.Snapshot().Calls...) }

// Execute calls a local tool by its canonical name.  ToolSet-prefixed names
// (for example "mail-tools_mail_search") are accepted as well, which makes
// direct replay of llmagent transcripts convenient.
func (e *Environment) Execute(ctx context.Context, name string, args map[string]any) (ToolResponse, error) {
	if e == nil {
		return ToolResponse{}, errors.New("activationbench: nil environment")
	}
	if err := contextErr(ctx); err != nil {
		return ToolResponse{}, err
	}
	canonical := e.canonicalToolName(name)
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := contextErr(ctx); err != nil {
		e.recordLocked(canonical, args, err)
		return ToolResponse{}, err
	}
	spec, ok := e.catalog.Tool(canonical)
	if !ok {
		err := fmt.Errorf("unknown tool %q", name)
		e.recordLocked(canonical, args, err)
		return ToolResponse{}, err
	}
	if spec.Distractor {
		// A distractor is an ordinary, safe read-only capability that is not
		// required by the current task. Returning a deterministic observation
		// keeps the model-facing surface realistic; the benchmark still counts
		// the call as a wrong-tool attempt through its metadata.
		result := response(canonical, map[string]any{
			"status":    "ok",
			"operation": canonical,
		})
		e.recordLocked(canonical, args, nil)
		return result, nil
	}
	response, err := e.executeLocked(ctx, canonical, args)
	e.recordLocked(canonical, args, err)
	return response, err
}

// Call is a small compatibility alias with the conventional any return type
// used by generic tool adapters.
//
// Deprecated: use Execute when the concrete ToolResponse is useful.
func (e *Environment) Call(ctx context.Context, name string, args map[string]any) (any, error) {
	return e.Execute(ctx, name, args)
}

// CallJSON decodes a model/tool JSON argument object and executes it.
func (e *Environment) CallJSON(ctx context.Context, name string, raw []byte) (any, error) {
	var args map[string]any
	if len(strings.TrimSpace(string(raw))) != 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &args); err != nil {
			return nil, fmt.Errorf("decode arguments for %q: %w", name, err)
		}
	}
	return e.Call(ctx, name, args)
}

func (e *Environment) canonicalToolName(name string) string {
	name = strings.TrimSpace(name)
	if _, ok := e.catalog.Tool(name); ok {
		return name
	}
	for _, set := range e.catalog.ToolSets() {
		// IDs are the canonical runtime names used by ToolSet.Name and the
		// activation-rule helper.  ToolSetSpec.Name is display metadata and
		// may intentionally differ. Accept the display spelling as a legacy
		// direct-replay alias, but never expose it as the ToolSet runtime name.
		prefixes := []string{set.ID}
		if display := strings.TrimSpace(set.Name); display != "" && display != set.ID {
			prefixes = append(prefixes, display)
		}
		for _, prefixName := range prefixes {
			prefix := prefixName + "_"
			if !strings.HasPrefix(name, prefix) {
				continue
			}
			candidate := strings.TrimPrefix(name, prefix)
			// A raw tool name can exist in a different ToolSet. Only strip a
			// prefix when the candidate belongs to the set that supplied it;
			// otherwise a malformed qualified call could be recorded as an
			// unrelated successful tool call.
			if spec, ok := e.catalog.Tool(candidate); ok && spec.ToolSetID == set.ID {
				return candidate
			}
		}
	}
	return name
}

func (e *Environment) recordLocked(name string, args map[string]any, err error) {
	e.calls = append(e.calls, CallRecord{
		Sequence:  len(e.calls) + 1,
		ToolName:  name,
		Arguments: cloneArgs(args),
		Succeeded: err == nil,
		Error:     errorString(err),
	})
}

func (e *Environment) executeLocked(ctx context.Context, name string, args map[string]any) (ToolResponse, error) {
	if err := contextErr(ctx); err != nil {
		return ToolResponse{}, err
	}
	switch name {
	case "mail_search":
		return e.mailSearch(args)
	case "mail_get":
		return e.mailGet(args)
	case "mail_mark_read":
		return e.mailMarkRead(args)
	case "mail_label":
		return e.mailLabel(args)
	case "mail_archive":
		return e.mailArchive(args)
	case "mail_create_draft":
		return e.mailCreateDraft(args)
	case "calendar_list_events":
		return e.calendarList(args)
	case "calendar_find_slots":
		return e.calendarFindSlots(args)
	case "calendar_create_event":
		return e.calendarCreate(args)
	case "calendar_update_event":
		return e.calendarUpdate(args)
	case "calendar_cancel_event":
		return e.calendarCancel(args)
	case "calendar_add_attendee":
		return e.calendarAddAttendee(args)
	case "docs_search":
		return e.docsSearch(args)
	case "docs_read":
		return e.docsRead(args)
	case "docs_create":
		return e.docsCreate(args)
	case "docs_append":
		return e.docsAppend(args)
	case "docs_replace":
		return e.docsReplace(args)
	case "docs_tag":
		return e.docsTag(args)
	case "sheet_list":
		return e.sheetList(args)
	case "sheet_read_rows":
		return e.sheetReadRows(args)
	case "sheet_find_rows":
		return e.sheetFindRows(args)
	case "sheet_update_cell":
		return e.sheetUpdateCell(args)
	case "sheet_append_row":
		return e.sheetAppendRow(args)
	case "sheet_sum_column":
		return e.sheetSumColumn(args)
	case "inventory_search":
		return e.inventorySearch(args)
	case "inventory_get_stock":
		return e.inventoryGetStock(args)
	case "inventory_reserve":
		return e.inventoryReserve(args)
	case "inventory_release":
		return e.inventoryRelease(args)
	case "inventory_set_reorder_point":
		return e.inventorySetReorder(args)
	case "inventory_list_reservations":
		return e.inventoryListReservations(args)
	case "crm_find_contact":
		return e.crmFindContact(args)
	case "crm_get_contact":
		return e.crmGetContact(args)
	case "crm_update_contact":
		return e.crmUpdateContact(args)
	case "crm_log_activity":
		return e.crmLogActivity(args)
	case "crm_create_task":
		return e.crmCreateTask(args)
	case "crm_list_tasks":
		return e.crmListTasks(args)
	case "files_list":
		return e.filesList(args)
	case "files_search":
		return e.filesSearch(args)
	case "files_read":
		return e.filesRead(args)
	case "files_write":
		return e.filesWrite(args)
	case "files_move":
		return e.filesMove(args)
	case "files_archive":
		return e.filesArchive(args)
	case "research_search_notes":
		return e.researchSearch(args)
	case "research_get_source":
		return e.researchGetSource(args)
	case "research_save_finding":
		return e.researchSaveFinding(args)
	case "research_tag_finding":
		return e.researchTagFinding(args)
	case "research_list_findings":
		return e.researchListFindings(args)
	case "research_summarize":
		return e.researchSummarize(args)
	default:
		return ToolResponse{}, fmt.Errorf("tool %q has no Lite executor", name)
	}
}

func (e *Environment) mailSearch(args map[string]any) (ToolResponse, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return ToolResponse{}, err
	}
	limit, err := optionalPositiveInt(args, "limit", 10)
	if err != nil {
		return ToolResponse{}, err
	}
	query = strings.ToLower(query)
	result := make([]Email, 0, limit)
	for _, mail := range e.state.Emails {
		if containsFold(mail.From, query) || containsFold(mail.Subject, query) || containsFold(mail.Body, query) {
			result = append(result, cloneEmail(mail))
			if len(result) >= limit {
				break
			}
		}
	}
	return response("mail_search", result), nil
}

func (e *Environment) mailGet(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	for _, mail := range e.state.Emails {
		if mail.ID == id {
			return response("mail_get", cloneEmail(mail)), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("message %q not found", id)
}

func (e *Environment) mailMarkRead(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	read, err := requiredBool(args, "read")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Emails {
		if e.state.Emails[i].ID == id {
			e.state.Emails[i].Read = read
			return response("mail_mark_read", cloneEmail(e.state.Emails[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("message %q not found", id)
}

func (e *Environment) mailLabel(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	label, err := requiredString(args, "label")
	if err != nil {
		return ToolResponse{}, err
	}
	add, err := requiredBool(args, "add")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Emails {
		if e.state.Emails[i].ID != id {
			continue
		}
		labels := e.state.Emails[i].Labels
		if add {
			if !containsString(labels, label) {
				labels = append(labels, label)
			}
		} else {
			labels = removeString(labels, label)
		}
		sort.Strings(labels)
		e.state.Emails[i].Labels = labels
		return response("mail_label", cloneEmail(e.state.Emails[i])), nil
	}
	return ToolResponse{}, fmt.Errorf("message %q not found", id)
}

func (e *Environment) mailArchive(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	archived, err := requiredBool(args, "archived")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Emails {
		if e.state.Emails[i].ID == id {
			e.state.Emails[i].Archived = archived
			return response("mail_archive", cloneEmail(e.state.Emails[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("message %q not found", id)
}

func (e *Environment) mailCreateDraft(args map[string]any) (ToolResponse, error) {
	to, err := requiredString(args, "to")
	if err != nil {
		return ToolResponse{}, err
	}
	subject, err := requiredString(args, "subject")
	if err != nil {
		return ToolResponse{}, err
	}
	body, err := requiredString(args, "body")
	if err != nil {
		return ToolResponse{}, err
	}
	id := e.nextIDLocked("mail-draft")
	mail := Email{ID: id, From: "agent@example.test", To: to, Subject: subject, Body: body, Labels: []string{"draft"}}
	e.state.Emails = append(e.state.Emails, mail)
	return response("mail_create_draft", cloneEmail(mail)), nil
}

func (e *Environment) calendarList(args map[string]any) (ToolResponse, error) {
	from, err := optionalString(args, "from")
	if err != nil {
		return ToolResponse{}, err
	}
	to, err := optionalString(args, "to")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]CalendarEvent, 0)
	for _, event := range e.state.Events {
		if from != "" && event.End <= from {
			continue
		}
		if to != "" && event.Start >= to {
			continue
		}
		result = append(result, cloneEvent(event))
	}
	return response("calendar_list_events", result), nil
}

func (e *Environment) calendarFindSlots(args map[string]any) (ToolResponse, error) {
	date, err := requiredString(args, "date")
	if err != nil {
		return ToolResponse{}, err
	}
	duration, err := requiredInt(args, "duration_minutes")
	if err != nil {
		return ToolResponse{}, err
	}
	if duration <= 0 || duration > 480 {
		return ToolResponse{}, errors.New("duration_minutes must be between 1 and 480")
	}
	candidates := []string{date + "T09:00Z", date + "T10:00Z", date + "T11:00Z", date + "T13:00Z", date + "T15:00Z", date + "T16:00Z"}
	free := make([]string, 0, len(candidates))
	for _, start := range candidates {
		end := addMinutes(start, duration)
		busy := false
		for _, event := range e.state.Events {
			if !event.Cancelled && event.Start < end && event.End > start {
				busy = true
				break
			}
		}
		if !busy {
			free = append(free, start)
		}
	}
	return response("calendar_find_slots", free), nil
}

func (e *Environment) calendarCreate(args map[string]any) (ToolResponse, error) {
	title, err := requiredString(args, "title")
	if err != nil {
		return ToolResponse{}, err
	}
	start, err := requiredString(args, "start")
	if err != nil {
		return ToolResponse{}, err
	}
	end, err := requiredString(args, "end")
	if err != nil {
		return ToolResponse{}, err
	}
	if start >= end {
		return ToolResponse{}, errors.New("event start must be before end")
	}
	attendees, err := optionalCSV(args, "attendees")
	if err != nil {
		return ToolResponse{}, err
	}
	event := CalendarEvent{ID: e.nextIDLocked("event"), Title: title, Start: start, End: end, Attendees: attendees}
	e.state.Events = append(e.state.Events, event)
	return response("calendar_create_event", cloneEvent(event)), nil
}

func (e *Environment) calendarUpdate(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Events {
		if e.state.Events[i].ID != id {
			continue
		}
		// Parse and validate against a copy first. A malformed update must not
		// leave a partially changed event in the task world.
		event := cloneEvent(e.state.Events[i])
		if value, ok := args["title"]; ok {
			if event.Title, err = stringValue(value); err != nil {
				return ToolResponse{}, fmt.Errorf("title: %w", err)
			}
		}
		if value, ok := args["start"]; ok {
			if event.Start, err = stringValue(value); err != nil {
				return ToolResponse{}, fmt.Errorf("start: %w", err)
			}
		}
		if value, ok := args["end"]; ok {
			if event.End, err = stringValue(value); err != nil {
				return ToolResponse{}, fmt.Errorf("end: %w", err)
			}
		}
		if event.Start >= event.End {
			return ToolResponse{}, errors.New("event start must be before end")
		}
		if value, ok := args["attendees"]; ok {
			raw, e2 := stringValue(value)
			if e2 != nil {
				return ToolResponse{}, e2
			}
			event.Attendees = splitCSV(raw)
		}
		e.state.Events[i] = event
		return response("calendar_update_event", cloneEvent(event)), nil
	}
	return ToolResponse{}, fmt.Errorf("event %q not found", id)
}

func (e *Environment) calendarCancel(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	cancelled, err := requiredBool(args, "cancelled")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Events {
		if e.state.Events[i].ID == id {
			e.state.Events[i].Cancelled = cancelled
			return response("calendar_cancel_event", cloneEvent(e.state.Events[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("event %q not found", id)
}

func (e *Environment) calendarAddAttendee(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	attendee, err := requiredString(args, "attendee")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Events {
		if e.state.Events[i].ID == id {
			if !containsString(e.state.Events[i].Attendees, attendee) {
				e.state.Events[i].Attendees = append(e.state.Events[i].Attendees, attendee)
				sort.Strings(e.state.Events[i].Attendees)
			}
			return response("calendar_add_attendee", cloneEvent(e.state.Events[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("event %q not found", id)
}

func (e *Environment) docsSearch(args map[string]any) (ToolResponse, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return ToolResponse{}, err
	}
	limit, err := optionalPositiveInt(args, "limit", 10)
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]Document, 0, limit)
	for _, doc := range e.state.Documents {
		if containsFold(doc.Title, query) || containsFold(doc.Body, query) {
			result = append(result, cloneDocument(doc))
			if len(result) >= limit {
				break
			}
		}
	}
	return response("docs_search", result), nil
}

func (e *Environment) docsRead(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	for _, doc := range e.state.Documents {
		if doc.ID == id {
			return response("docs_read", cloneDocument(doc)), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("document %q not found", id)
}

func (e *Environment) docsCreate(args map[string]any) (ToolResponse, error) {
	title, err := requiredString(args, "title")
	if err != nil {
		return ToolResponse{}, err
	}
	body, err := requiredString(args, "body")
	if err != nil {
		return ToolResponse{}, err
	}
	doc := Document{ID: e.nextIDLocked("doc"), Title: title, Body: body, Version: 1}
	e.state.Documents = append(e.state.Documents, doc)
	return response("docs_create", cloneDocument(doc)), nil
}

func (e *Environment) docsAppend(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	text, err := requiredString(args, "text")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Documents {
		if e.state.Documents[i].ID == id {
			if e.state.Documents[i].Body != "" && !strings.HasSuffix(e.state.Documents[i].Body, "\n") {
				e.state.Documents[i].Body += "\n"
			}
			e.state.Documents[i].Body += text
			e.state.Documents[i].Version++
			return response("docs_append", cloneDocument(e.state.Documents[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("document %q not found", id)
}

func (e *Environment) docsReplace(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	body, err := requiredString(args, "body")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Documents {
		if e.state.Documents[i].ID == id {
			e.state.Documents[i].Body = body
			e.state.Documents[i].Version++
			return response("docs_replace", cloneDocument(e.state.Documents[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("document %q not found", id)
}

func (e *Environment) docsTag(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	tag, err := requiredString(args, "tag")
	if err != nil {
		return ToolResponse{}, err
	}
	add, err := requiredBool(args, "add")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Documents {
		if e.state.Documents[i].ID == id {
			if add {
				if !containsString(e.state.Documents[i].Tags, tag) {
					e.state.Documents[i].Tags = append(e.state.Documents[i].Tags, tag)
				}
			} else {
				e.state.Documents[i].Tags = removeString(e.state.Documents[i].Tags, tag)
			}
			sort.Strings(e.state.Documents[i].Tags)
			return response("docs_tag", cloneDocument(e.state.Documents[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("document %q not found", id)
}

func (e *Environment) sheetList(_ map[string]any) (ToolResponse, error) {
	type summary struct {
		ID, Name string
		Columns  []string `json:"columns"`
		RowCount int      `json:"row_count"`
	}
	result := make([]summary, 0, len(e.state.Sheets))
	for _, sheet := range e.state.Sheets {
		result = append(result, summary{ID: sheet.ID, Name: sheet.Name, Columns: append([]string(nil), sheet.Columns...), RowCount: len(sheet.Rows)})
	}
	return response("sheet_list", result), nil
}

func (e *Environment) sheetReadRows(args map[string]any) (ToolResponse, error) {
	sheetID, err := requiredString(args, "sheet_id")
	if err != nil {
		return ToolResponse{}, err
	}
	limit, err := optionalPositiveInt(args, "limit", 20)
	if err != nil {
		return ToolResponse{}, err
	}
	sheet, ok := findSheet(e.state.Sheets, sheetID)
	if !ok {
		return ToolResponse{}, fmt.Errorf("sheet %q not found", sheetID)
	}
	rows := make([]map[string]string, 0, min(limit, len(sheet.Rows)))
	for _, row := range sheet.Rows {
		rows = append(rows, cloneStringMap(row))
		if len(rows) >= limit {
			break
		}
	}
	return response("sheet_read_rows", map[string]any{"sheet_id": sheet.ID, "columns": append([]string(nil), sheet.Columns...), "rows": rows}), nil
}

func (e *Environment) sheetFindRows(args map[string]any) (ToolResponse, error) {
	sheetID, err := requiredString(args, "sheet_id")
	if err != nil {
		return ToolResponse{}, err
	}
	column, err := requiredString(args, "column")
	if err != nil {
		return ToolResponse{}, err
	}
	value, err := requiredString(args, "value")
	if err != nil {
		return ToolResponse{}, err
	}
	sheet, ok := findSheet(e.state.Sheets, sheetID)
	if !ok {
		return ToolResponse{}, fmt.Errorf("sheet %q not found", sheetID)
	}
	rows := make([]map[string]string, 0)
	for _, row := range sheet.Rows {
		if strings.EqualFold(row[column], value) {
			rows = append(rows, cloneStringMap(row))
		}
	}
	return response("sheet_find_rows", rows), nil
}

func (e *Environment) sheetUpdateCell(args map[string]any) (ToolResponse, error) {
	sheetID, err := requiredString(args, "sheet_id")
	if err != nil {
		return ToolResponse{}, err
	}
	rowIndex, err := requiredInt(args, "row")
	if err != nil {
		return ToolResponse{}, err
	}
	column, err := requiredString(args, "column")
	if err != nil {
		return ToolResponse{}, err
	}
	value, err := requiredString(args, "value")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Sheets {
		if e.state.Sheets[i].ID == sheetID {
			if rowIndex < 0 || rowIndex >= len(e.state.Sheets[i].Rows) {
				return ToolResponse{}, fmt.Errorf("row %d out of range", rowIndex)
			}
			if !containsString(e.state.Sheets[i].Columns, column) {
				return ToolResponse{}, fmt.Errorf("column %q not found", column)
			}
			if e.state.Sheets[i].Rows[rowIndex] == nil {
				e.state.Sheets[i].Rows[rowIndex] = make(map[string]string)
			}
			e.state.Sheets[i].Rows[rowIndex][column] = value
			return response("sheet_update_cell", map[string]any{"sheet_id": sheetID, "row": rowIndex, "column": column, "value": value}), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("sheet %q not found", sheetID)
}

func (e *Environment) sheetAppendRow(args map[string]any) (ToolResponse, error) {
	sheetID, err := requiredString(args, "sheet_id")
	if err != nil {
		return ToolResponse{}, err
	}
	raw, err := requiredString(args, "values")
	if err != nil {
		return ToolResponse{}, err
	}
	var encodedValues map[string]any
	if err := json.Unmarshal([]byte(raw), &encodedValues); err != nil {
		return ToolResponse{}, fmt.Errorf("values must be a JSON object: %w", err)
	}
	values := make(map[string]string, len(encodedValues))
	for column, value := range encodedValues {
		text, err := sheetCellValue(value)
		if err != nil {
			return ToolResponse{}, fmt.Errorf("values.%s: %w", column, err)
		}
		values[column] = text
	}
	for i := range e.state.Sheets {
		if e.state.Sheets[i].ID == sheetID {
			for key := range values {
				if !containsString(e.state.Sheets[i].Columns, key) {
					return ToolResponse{}, fmt.Errorf("column %q not found", key)
				}
			}
			e.state.Sheets[i].Rows = append(e.state.Sheets[i].Rows, cloneStringMap(values))
			return response("sheet_append_row", values), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("sheet %q not found", sheetID)
}

// sheetCellValue converts the scalar JSON values commonly emitted by models
// for spreadsheet cells into the fixture's canonical string representation.
// The public tool contract keeps the row payload JSON-encoded for compatibility
// with the original Lite fixture, but numeric cells such as quantity=1 should
// not fail merely because the model used a JSON number instead of "1".
func sheetCellValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), nil
	case bool:
		return strconv.FormatBool(typed), nil
	case nil:
		return "", errors.New("must be a non-null scalar")
	default:
		return "", fmt.Errorf("must be a string, number, or boolean; got %T", value)
	}
}

func (e *Environment) sheetSumColumn(args map[string]any) (ToolResponse, error) {
	sheetID, err := requiredString(args, "sheet_id")
	if err != nil {
		return ToolResponse{}, err
	}
	column, err := requiredString(args, "column")
	if err != nil {
		return ToolResponse{}, err
	}
	sheet, ok := findSheet(e.state.Sheets, sheetID)
	if !ok {
		return ToolResponse{}, fmt.Errorf("sheet %q not found", sheetID)
	}
	if !containsString(sheet.Columns, column) {
		return ToolResponse{}, fmt.Errorf("column %q not found", column)
	}
	total := 0.0
	for _, row := range sheet.Rows {
		if raw := strings.TrimSpace(row[column]); raw != "" {
			value, parseErr := strconv.ParseFloat(raw, 64)
			if parseErr != nil {
				return ToolResponse{}, fmt.Errorf("row value %q in %s is not numeric", raw, column)
			}
			total += value
		}
	}
	return response("sheet_sum_column", map[string]any{"sheet_id": sheetID, "column": column, "sum": total}), nil
}

func (e *Environment) inventorySearch(args map[string]any) (ToolResponse, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]Product, 0)
	for _, product := range e.state.Products {
		if containsFold(product.SKU, query) || containsFold(product.Name, query) {
			result = append(result, product)
		}
	}
	return response("inventory_search", result), nil
}

func (e *Environment) inventoryGetStock(args map[string]any) (ToolResponse, error) {
	sku, err := requiredString(args, "sku")
	if err != nil {
		return ToolResponse{}, err
	}
	for _, product := range e.state.Products {
		if product.SKU == sku {
			available := product.Stock - e.reservedQuantityLocked(sku)
			return response("inventory_get_stock", map[string]any{"sku": sku, "stock": product.Stock, "reserved": e.reservedQuantityLocked(sku), "available": available, "reorder_point": product.ReorderPoint}), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("product %q not found", sku)
}

func (e *Environment) inventoryReserve(args map[string]any) (ToolResponse, error) {
	sku, err := requiredString(args, "sku")
	if err != nil {
		return ToolResponse{}, err
	}
	quantity, err := requiredInt(args, "quantity")
	if err != nil {
		return ToolResponse{}, err
	}
	customer, err := requiredString(args, "customer")
	if err != nil {
		return ToolResponse{}, err
	}
	if quantity <= 0 {
		return ToolResponse{}, errors.New("quantity must be positive")
	}
	product, ok := findProduct(e.state.Products, sku)
	if !ok {
		return ToolResponse{}, fmt.Errorf("product %q not found", sku)
	}
	if product.Stock-e.reservedQuantityLocked(sku) < quantity {
		return ToolResponse{}, fmt.Errorf("insufficient available stock for %q", sku)
	}
	reservation := Reservation{ID: e.nextIDLocked("reservation"), SKU: sku, Customer: customer, Quantity: quantity}
	e.state.Reservations = append(e.state.Reservations, reservation)
	return response("inventory_reserve", reservation), nil
}

func (e *Environment) inventoryRelease(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "reservation_id")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Reservations {
		if e.state.Reservations[i].ID == id {
			e.state.Reservations[i].Released = true
			return response("inventory_release", e.state.Reservations[i]), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("reservation %q not found", id)
}

func (e *Environment) inventorySetReorder(args map[string]any) (ToolResponse, error) {
	sku, err := requiredString(args, "sku")
	if err != nil {
		return ToolResponse{}, err
	}
	point, err := requiredInt(args, "reorder_point")
	if err != nil {
		return ToolResponse{}, err
	}
	if point < 0 {
		return ToolResponse{}, errors.New("reorder_point cannot be negative")
	}
	for i := range e.state.Products {
		if e.state.Products[i].SKU == sku {
			e.state.Products[i].ReorderPoint = point
			return response("inventory_set_reorder_point", e.state.Products[i]), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("product %q not found", sku)
}

func (e *Environment) inventoryListReservations(args map[string]any) (ToolResponse, error) {
	sku, err := optionalString(args, "sku")
	if err != nil {
		return ToolResponse{}, err
	}
	customer, err := optionalString(args, "customer")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]Reservation, 0)
	for _, reservation := range e.state.Reservations {
		if sku != "" && reservation.SKU != sku {
			continue
		}
		if customer != "" && !strings.EqualFold(reservation.Customer, customer) {
			continue
		}
		result = append(result, reservation)
	}
	return response("inventory_list_reservations", result), nil
}

func (e *Environment) crmFindContact(args map[string]any) (ToolResponse, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]Contact, 0)
	for _, contact := range e.state.Contacts {
		if containsFold(contact.ID, query) || containsFold(contact.Name, query) || containsFold(contact.Email, query) || containsFold(contact.Company, query) {
			result = append(result, cloneContact(contact))
		}
	}
	return response("crm_find_contact", result), nil
}

func (e *Environment) crmGetContact(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	for _, contact := range e.state.Contacts {
		if contact.ID == id {
			return response("crm_get_contact", cloneContact(contact)), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("contact %q not found", id)
}

func (e *Environment) crmUpdateContact(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Contacts {
		if e.state.Contacts[i].ID != id {
			continue
		}
		// Keep field order deterministic and commit only after every supplied
		// value has been validated. This makes failed updates transactional.
		contact := cloneContact(e.state.Contacts[i])
		for _, key := range []string{"company", "phone", "notes"} {
			if value, ok := args[key]; ok {
				parsed, parseErr := stringValue(value)
				if parseErr != nil {
					return ToolResponse{}, fmt.Errorf("%s: %w", key, parseErr)
				}
				switch key {
				case "company":
					contact.Company = parsed
				case "phone":
					contact.Phone = parsed
				case "notes":
					contact.Notes = parsed
				}
			}
		}
		e.state.Contacts[i] = contact
		return response("crm_update_contact", cloneContact(contact)), nil
	}
	return ToolResponse{}, fmt.Errorf("contact %q not found", id)
}

func (e *Environment) crmLogActivity(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "contact_id")
	if err != nil {
		return ToolResponse{}, err
	}
	kind, err := requiredString(args, "kind")
	if err != nil {
		return ToolResponse{}, err
	}
	note, err := requiredString(args, "note")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Contacts {
		if e.state.Contacts[i].ID == id {
			activity := Activity{Kind: kind, Note: note}
			e.state.Contacts[i].Activities = append(e.state.Contacts[i].Activities, activity)
			return response("crm_log_activity", activity), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("contact %q not found", id)
}

func (e *Environment) crmCreateTask(args map[string]any) (ToolResponse, error) {
	contactID, err := requiredString(args, "contact_id")
	if err != nil {
		return ToolResponse{}, err
	}
	title, err := requiredString(args, "title")
	if err != nil {
		return ToolResponse{}, err
	}
	due, err := requiredString(args, "due")
	if err != nil {
		return ToolResponse{}, err
	}
	if _, ok := findContact(e.state.Contacts, contactID); !ok {
		return ToolResponse{}, fmt.Errorf("contact %q not found", contactID)
	}
	task := CRMTask{ID: e.nextIDLocked("crm-task"), ContactID: contactID, Title: title, Due: due}
	e.state.CRMTasks = append(e.state.CRMTasks, task)
	return response("crm_create_task", task), nil
}

func (e *Environment) crmListTasks(args map[string]any) (ToolResponse, error) {
	contactID, err := optionalString(args, "contact_id")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]CRMTask, 0)
	for _, task := range e.state.CRMTasks {
		if contactID != "" && task.ContactID != contactID {
			continue
		}
		if !task.Done {
			result = append(result, task)
		}
	}
	return response("crm_list_tasks", result), nil
}

func (e *Environment) filesList(args map[string]any) (ToolResponse, error) {
	prefix, err := optionalString(args, "prefix")
	if err != nil {
		return ToolResponse{}, err
	}
	type summary struct {
		Path     string `json:"path"`
		Archived bool   `json:"archived"`
		Bytes    int    `json:"bytes"`
	}
	result := make([]summary, 0)
	for _, file := range e.state.Files {
		if prefix != "" && !strings.HasPrefix(file.Path, prefix) {
			continue
		}
		result = append(result, summary{Path: file.Path, Archived: file.Archived, Bytes: len(file.Content)})
	}
	return response("files_list", result), nil
}

func (e *Environment) filesSearch(args map[string]any) (ToolResponse, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]FileEntry, 0)
	for _, file := range e.state.Files {
		if containsFold(file.Path, query) || containsFold(file.Content, query) {
			result = append(result, cloneFile(file))
		}
	}
	return response("files_search", result), nil
}

func (e *Environment) filesRead(args map[string]any) (ToolResponse, error) {
	path, err := requiredString(args, "path")
	if err != nil {
		return ToolResponse{}, err
	}
	for _, file := range e.state.Files {
		if file.Path == path {
			return response("files_read", cloneFile(file)), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("file %q not found", path)
}

func (e *Environment) filesWrite(args map[string]any) (ToolResponse, error) {
	path, err := requiredString(args, "path")
	if err != nil {
		return ToolResponse{}, err
	}
	content, err := requiredString(args, "content")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Files {
		if e.state.Files[i].Path == path {
			e.state.Files[i].Content = content
			e.state.Files[i].Archived = false
			return response("files_write", cloneFile(e.state.Files[i])), nil
		}
	}
	file := FileEntry{Path: path, Content: content}
	e.state.Files = append(e.state.Files, file)
	return response("files_write", cloneFile(file)), nil
}

func (e *Environment) filesMove(args map[string]any) (ToolResponse, error) {
	path, err := requiredString(args, "path")
	if err != nil {
		return ToolResponse{}, err
	}
	destination, err := requiredString(args, "destination")
	if err != nil {
		return ToolResponse{}, err
	}
	if destination == "" {
		return ToolResponse{}, errors.New("destination is required")
	}
	for i := range e.state.Files {
		if e.state.Files[i].Path == path {
			if _, exists := findFile(e.state.Files, destination); exists {
				return ToolResponse{}, fmt.Errorf("destination %q already exists", destination)
			}
			e.state.Files[i].Path = destination
			return response("files_move", cloneFile(e.state.Files[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("file %q not found", path)
}

func (e *Environment) filesArchive(args map[string]any) (ToolResponse, error) {
	path, err := requiredString(args, "path")
	if err != nil {
		return ToolResponse{}, err
	}
	archived, err := requiredBool(args, "archived")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Files {
		if e.state.Files[i].Path == path {
			e.state.Files[i].Archived = archived
			return response("files_archive", cloneFile(e.state.Files[i])), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("file %q not found", path)
}

func (e *Environment) researchSearch(args map[string]any) (ToolResponse, error) {
	query, err := requiredString(args, "query")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]Source, 0)
	for _, source := range e.state.Sources {
		if containsFold(source.ID, query) || containsFold(source.Title, query) || containsFold(source.Text, query) {
			result = append(result, cloneSource(source))
		}
	}
	return response("research_search_notes", result), nil
}

func (e *Environment) researchGetSource(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	for _, source := range e.state.Sources {
		if source.ID == id {
			return response("research_get_source", cloneSource(source)), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("source %q not found", id)
}

func (e *Environment) researchSaveFinding(args map[string]any) (ToolResponse, error) {
	topic, err := requiredString(args, "topic")
	if err != nil {
		return ToolResponse{}, err
	}
	claim, err := requiredString(args, "claim")
	if err != nil {
		return ToolResponse{}, err
	}
	sourceID, err := requiredString(args, "source_id")
	if err != nil {
		return ToolResponse{}, err
	}
	if _, ok := findSource(e.state.Sources, sourceID); !ok {
		return ToolResponse{}, fmt.Errorf("source %q not found", sourceID)
	}
	finding := Finding{ID: e.nextIDLocked("finding"), Topic: topic, Claim: claim, SourceID: sourceID}
	e.state.Findings = append(e.state.Findings, finding)
	return response("research_save_finding", finding), nil
}

func (e *Environment) researchTagFinding(args map[string]any) (ToolResponse, error) {
	id, err := requiredString(args, "id")
	if err != nil {
		return ToolResponse{}, err
	}
	tag, err := requiredString(args, "tag")
	if err != nil {
		return ToolResponse{}, err
	}
	add, err := requiredBool(args, "add")
	if err != nil {
		return ToolResponse{}, err
	}
	for i := range e.state.Findings {
		if e.state.Findings[i].ID == id {
			if add {
				if !containsString(e.state.Findings[i].Tags, tag) {
					e.state.Findings[i].Tags = append(e.state.Findings[i].Tags, tag)
				}
			} else {
				e.state.Findings[i].Tags = removeString(e.state.Findings[i].Tags, tag)
			}
			sort.Strings(e.state.Findings[i].Tags)
			return response("research_tag_finding", e.state.Findings[i]), nil
		}
	}
	return ToolResponse{}, fmt.Errorf("finding %q not found", id)
}

func (e *Environment) researchListFindings(args map[string]any) (ToolResponse, error) {
	topic, err := optionalString(args, "topic")
	if err != nil {
		return ToolResponse{}, err
	}
	result := make([]Finding, 0)
	for _, finding := range e.state.Findings {
		if topic != "" && !strings.EqualFold(finding.Topic, topic) {
			continue
		}
		result = append(result, cloneFinding(finding))
	}
	return response("research_list_findings", result), nil
}

func (e *Environment) researchSummarize(args map[string]any) (ToolResponse, error) {
	topic, err := requiredString(args, "topic")
	if err != nil {
		return ToolResponse{}, err
	}
	claims := make([]string, 0)
	for _, finding := range e.state.Findings {
		if strings.EqualFold(finding.Topic, topic) {
			claims = append(claims, finding.Claim)
		}
	}
	sort.Strings(claims)
	return response("research_summarize", map[string]any{"topic": topic, "finding_count": len(claims), "summary": strings.Join(claims, " ")}), nil
}

func (e *Environment) nextIDLocked(prefix string) string {
	// Handlers may reconstruct an Environment from the task world for every
	// call. Seed the counter from IDs already present in that world so two
	// generated objects in one task cannot both become prefix-001.
	if e.nextIDs[prefix] == 0 {
		e.nextIDs[prefix] = maxGeneratedID(e.state, prefix)
	}
	e.nextIDs[prefix]++
	return fmt.Sprintf("%s-%03d", prefix, e.nextIDs[prefix])
}

func maxGeneratedID(state State, prefix string) int {
	max := 0
	visit := func(id string) {
		id = strings.TrimSpace(id)
		marker := prefix + "-"
		if !strings.HasPrefix(id, marker) {
			return
		}
		value, err := strconv.Atoi(strings.TrimPrefix(id, marker))
		if err == nil && value > max {
			max = value
		}
	}
	for _, item := range state.Emails {
		visit(item.ID)
	}
	for _, item := range state.Events {
		visit(item.ID)
	}
	for _, item := range state.Documents {
		visit(item.ID)
	}
	for _, item := range state.Reservations {
		visit(item.ID)
	}
	for _, item := range state.CRMTasks {
		visit(item.ID)
	}
	for _, item := range state.Findings {
		visit(item.ID)
	}
	return max
}

func (e *Environment) reservedQuantityLocked(sku string) int {
	total := 0
	for _, reservation := range e.state.Reservations {
		if reservation.SKU == sku && !reservation.Released {
			total += reservation.Quantity
		}
	}
	return total
}

func response(tool string, data any) ToolResponse { return ToolResponse{Tool: tool, Data: data} }

func contextErr(ctx context.Context) error {
	if ctx == nil {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}

func errorString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func requiredString(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	parsed, err := stringValue(value)
	if err != nil || strings.TrimSpace(parsed) == "" {
		if err == nil {
			err = errors.New("must not be empty")
		}
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func optionalString(args map[string]any, key string) (string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return "", nil
	}
	parsed, err := stringValue(value)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func stringValue(value any) (string, error) {
	switch typed := value.(type) {
	case string:
		return typed, nil
	case fmt.Stringer:
		return typed.String(), nil
	default:
		return "", fmt.Errorf("expected string, got %T", value)
	}
}

func requiredInt(args map[string]any, key string) (int, error) {
	value, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	parsed, err := intValue(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return parsed, nil
}

func optionalPositiveInt(args map[string]any, key string, fallback int) (int, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return fallback, nil
	}
	parsed, err := intValue(value)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	if parsed <= 0 {
		return 0, fmt.Errorf("%s must be positive", key)
	}
	return parsed, nil
}

func intValue(value any) (int, error) {
	switch typed := value.(type) {
	case int:
		return typed, nil
	case int8:
		return int(typed), nil
	case int16:
		return int(typed), nil
	case int32:
		return int(typed), nil
	case int64:
		return int(typed), nil
	case float64:
		if typed != float64(int(typed)) {
			return 0, errors.New("must be an integer")
		}
		return int(typed), nil
	case json.Number:
		n, err := typed.Int64()
		return int(n), err
	default:
		return 0, fmt.Errorf("expected integer, got %T", value)
	}
}

func requiredBool(args map[string]any, key string) (bool, error) {
	value, ok := args[key]
	if !ok {
		return false, fmt.Errorf("%s is required", key)
	}
	parsed, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("%s: expected boolean, got %T", key, value)
	}
	return parsed, nil
}

func optionalCSV(args map[string]any, key string) ([]string, error) {
	value, ok := args[key]
	if !ok || value == nil {
		return nil, nil
	}
	raw, err := stringValue(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", key, err)
	}
	return splitCSV(raw), nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" && !containsString(out, part) {
			out = append(out, part)
		}
	}
	sort.Strings(out)
	return out
}

func containsFold(value, query string) bool {
	return strings.Contains(strings.ToLower(value), strings.ToLower(query))
}
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
func removeString(values []string, target string) []string {
	out := values[:0]
	for _, value := range values {
		if value != target {
			out = append(out, value)
		}
	}
	return out
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func addMinutes(timestamp string, minutes int) string { // Fixture timestamps are UTC and minute-aligned.
	if len(timestamp) < 16 {
		return timestamp
	}
	hour, _ := strconv.Atoi(timestamp[11:13])
	minute, _ := strconv.Atoi(timestamp[14:16])
	total := hour*60 + minute + minutes
	return fmt.Sprintf("%sT%02d:%02dZ", timestamp[:10], (total/60)%24, total%60)
}

func cloneArgs(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneArgument(v)
	}
	return out
}

// cloneArgument copies the JSON-shaped values accepted by benchmark tools.
// Tool arguments normally arrive through json.Unmarshal, so map[string]any
// and []any cover the wire representation; the typed cases keep direct Go
// callers' common string maps/slices isolated as well. Values outside this
// JSON-shaped subset are immutable-by-convention and are retained.
func cloneArgument(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneArgs(typed)
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = cloneArgument(item)
		}
		return out
	case map[string]string:
		out := make(map[string]string, len(typed))
		for key, item := range typed {
			out[key] = item
		}
		return out
	case []string:
		return append([]string(nil), typed...)
	case []map[string]any:
		out := make([]map[string]any, len(typed))
		for i, item := range typed {
			out[i] = cloneArgs(item)
		}
		return out
	default:
		return value
	}
}
func cloneEmail(in Email) Email { in.Labels = append([]string(nil), in.Labels...); return in }
func cloneEvent(in CalendarEvent) CalendarEvent {
	in.Attendees = append([]string(nil), in.Attendees...)
	return in
}
func cloneDocument(in Document) Document { in.Tags = append([]string(nil), in.Tags...); return in }
func cloneContact(in Contact) Contact {
	in.Activities = append([]Activity(nil), in.Activities...)
	return in
}
func cloneFile(in FileEntry) FileEntry { return in }
func cloneSource(in Source) Source     { in.Tags = append([]string(nil), in.Tags...); return in }
func cloneFinding(in Finding) Finding  { in.Tags = append([]string(nil), in.Tags...); return in }

func findSheet(sheets []Sheet, id string) (Sheet, bool) {
	for _, sheet := range sheets {
		if sheet.ID == id {
			return sheet, true
		}
	}
	return Sheet{}, false
}
func findProduct(products []Product, sku string) (Product, bool) {
	for _, product := range products {
		if product.SKU == sku {
			return product, true
		}
	}
	return Product{}, false
}
func findContact(contacts []Contact, id string) (Contact, bool) {
	for _, contact := range contacts {
		if contact.ID == id {
			return contact, true
		}
	}
	return Contact{}, false
}
func findFile(files []FileEntry, path string) (FileEntry, bool) {
	for _, file := range files {
		if file.Path == path {
			return file, true
		}
	}
	return FileEntry{}, false
}
func findSource(sources []Source, id string) (Source, bool) {
	for _, source := range sources {
		if source.ID == id {
			return source, true
		}
	}
	return Source{}, false
}
