//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package catalog

import trpctool "trpc.group/trpc-go/trpc-agent-go/tool"

// defaultSpecs keeps the fixture readable while making all names explicit.
// The first six tools in each set are functional in env.Environment; the last
// two are harmless distractors that make menu selection non-trivial.
// Model-facing prose uses {{tool:raw_name}} references; tasks and runner
// resolve them from the generated ToolSpec metadata.
func defaultSpecs() ([]SkillSpec, []ToolSetSpec, []ToolSpec) {
	skills := []SkillSpec{
		{
			ID: "mail", Name: "Mail operations",
			Summary:      "Search and update the mailbox; use this for email triage, labels, drafts, and sending.",
			Instructions: "Search before mutating. Use the message id returned by {{tool:mail_search}}. Never infer an id from a subject.\n",
			ToolSetID:    "mail-tools", Tags: []string{"communication", "messages"},
		},
		{
			ID: "calendar", Name: "Calendar operations",
			Summary:      "Inspect availability and create or update calendar events.",
			Instructions: "Use ISO-8601 timestamps and attendee email addresses. Check existing events before creating a new one, and preserve attendees when updating an event.\n",
			ToolSetID:    "calendar-tools", Tags: []string{"scheduling", "meetings"},
		},
		{
			ID: "documents", Name: "Document operations",
			Summary:      "Search, read, and edit documents with explicit append or replace semantics.",
			Instructions: "Read a document before replacing it. Append preserves existing content; replace is intentional.\n",
			ToolSetID:    "documents-tools", Tags: []string{"writing", "knowledge"},
		},
		{
			ID: "spreadsheets", Name: "Spreadsheet operations",
			Summary:      "Read and update tabular sheets, rows, cells, and numeric summaries.",
			Instructions: "Use the sheet id and column names returned by {{tool:sheet_list}} or {{tool:sheet_read_rows}}. Cell updates are exact and do not reorder rows. When appending a row, encode its scalar cell values as a JSON object; numeric cells are stored as text.\n",
			ToolSetID:    "spreadsheets-tools", Tags: []string{"data", "tables"},
		},
		{
			ID: "inventory", Name: "Inventory operations",
			Summary:      "Look up stock and make reservations or reorder-point changes.",
			Instructions: "Check stock before reserving. Reservations use a stable reservation id and cannot exceed available stock.\n",
			ToolSetID:    "inventory-tools", Tags: []string{"commerce", "stock"},
		},
		{
			ID: "crm", Name: "CRM operations",
			Summary:      "Find contacts and maintain CRM activities and follow-up tasks.",
			Instructions: "Find the contact first, then use its stable id. Updating a contact preserves fields omitted from the request.\n",
			ToolSetID:    "crm-tools", Tags: []string{"contacts", "sales"},
		},
		{
			ID: "files", Name: "File operations",
			Summary:      "Search and edit files in the workspace.",
			Instructions: "Search or list before reading. Write replaces file content intentionally; archive is reversible and preferred to deletion.\n",
			ToolSetID:    "files-tools", Tags: []string{"workspace", "storage"},
		},
		{
			ID: "research", Name: "Research operations",
			Summary:      "Search research notes, inspect sources, and save structured findings.",
			Instructions: "Ground findings in a source id returned by {{tool:research_search_notes}}. Save concise findings with a stable topic and source.\n",
			ToolSetID:    "research-tools", Tags: []string{"research", "notes"},
		},
	}

	sets := []ToolSetSpec{
		{ID: "mail-tools", Name: "mail-tools", SkillID: "mail"},
		{ID: "calendar-tools", Name: "calendar-tools", SkillID: "calendar"},
		{ID: "documents-tools", Name: "documents-tools", SkillID: "documents"},
		{ID: "spreadsheets-tools", Name: "spreadsheets-tools", SkillID: "spreadsheets"},
		{ID: "inventory-tools", Name: "inventory-tools", SkillID: "inventory"},
		{ID: "crm-tools", Name: "crm-tools", SkillID: "crm"},
		{ID: "files-tools", Name: "files-tools", SkillID: "files"},
		{ID: "research-tools", Name: "research-tools", SkillID: "research"},
	}

	tools := []ToolSpec{
		// Mail.
		tool("mail_search", "Find messages by sender, subject, or free-text query.", "mail", "mail-tools", true, false, objectSchema(
			stringField("query", "Text to search in sender, subject, and body.", true),
			intField("limit", "Maximum number of messages to return; defaults to 10.", false),
		)),
		tool("mail_get", "Read one message by id.", "mail", "mail-tools", true, false, objectSchema(
			stringField("id", "Message id returned by {{tool:mail_search}}.", true),
		)),
		tool("mail_mark_read", "Mark a message as read or unread.", "mail", "mail-tools", false, false, objectSchema(
			stringField("id", "Message id.", true), boolField("read", "Whether the message is read.", true),
		)),
		tool("mail_label", "Add or remove a label on a message.", "mail", "mail-tools", false, false, objectSchema(
			stringField("id", "Message id.", true), stringField("label", "Label to change.", true), boolField("add", "True to add, false to remove.", true),
		)),
		tool("mail_archive", "Archive or unarchive a message.", "mail", "mail-tools", false, false, objectSchema(
			stringField("id", "Message id.", true), boolField("archived", "Whether the message is archived.", true),
		)),
		tool("mail_create_draft", "Create an email draft.", "mail", "mail-tools", false, false, objectSchema(
			stringField("to", "Recipient address.", true), stringField("subject", "Draft subject.", true), stringField("body", "Draft body.", true),
		)),
		tool("mail_export", "Return mailbox metadata in a selected export format.", "mail", "mail-tools", true, true, objectSchema(
			stringField("format", "Export format.", false),
		)),
		tool("mail_set_signature", "Preview how a mailbox signature would be applied.", "mail", "mail-tools", true, true, objectSchema(
			stringField("signature", "Signature text.", true),
		)),

		// Calendar.
		tool("calendar_list_events", "List calendar events in a time window.", "calendar", "calendar-tools", true, false, objectSchema(
			stringField("from", "Inclusive ISO-8601 start.", false), stringField("to", "Exclusive ISO-8601 end.", false),
		)),
		tool("calendar_find_slots", "Find free calendar slots of a requested duration.", "calendar", "calendar-tools", true, false, objectSchema(
			stringField("date", "Date in YYYY-MM-DD format.", true), intField("duration_minutes", "Required duration.", true),
		)),
		tool("calendar_create_event", "Create a calendar event.", "calendar", "calendar-tools", false, false, objectSchema(
			stringField("title", "Event title.", true), stringField("start", "ISO-8601 start.", true), stringField("end", "ISO-8601 end.", true), stringField("attendees", "Comma-separated attendee email addresses.", false),
		)),
		tool("calendar_update_event", "Update selected fields of an event.", "calendar", "calendar-tools", false, false, objectSchema(
			stringField("id", "Event id.", true), stringField("title", "New title.", false), stringField("start", "New start.", false), stringField("end", "New end.", false), stringField("attendees", "Comma-separated attendee email addresses; include existing attendees you want to preserve.", false),
		)),
		tool("calendar_cancel_event", "Cancel or restore an event.", "calendar", "calendar-tools", false, false, objectSchema(
			stringField("id", "Event id.", true), boolField("cancelled", "Whether to cancel.", true),
		)),
		tool("calendar_add_attendee", "Add one attendee to an event.", "calendar", "calendar-tools", false, false, objectSchema(
			stringField("id", "Event id.", true), stringField("attendee", "Email address.", true),
		)),
		tool("calendar_timezone_convert", "Convert a timestamp between time zones for display.", "calendar", "calendar-tools", true, true, objectSchema(
			stringField("timestamp", "ISO-8601 timestamp.", true), stringField("timezone", "Target time zone.", true),
		)),
		tool("calendar_share_room", "Return room availability for an event.", "calendar", "calendar-tools", true, true, objectSchema(
			stringField("event_id", "Event id.", true), stringField("room", "Room name.", true),
		)),

		// Documents.
		tool("docs_search", "Search documents by title or body text.", "documents", "documents-tools", true, false, objectSchema(
			stringField("query", "Search text.", true), intField("limit", "Maximum results.", false),
		)),
		tool("docs_read", "Read one document by id.", "documents", "documents-tools", true, false, objectSchema(stringField("id", "Document id.", true))),
		tool("docs_create", "Create a document.", "documents", "documents-tools", false, false, objectSchema(
			stringField("title", "Document title.", true), stringField("body", "Document body.", true),
		)),
		tool("docs_append", "Append text to an existing document.", "documents", "documents-tools", false, false, objectSchema(
			stringField("id", "Document id.", true), stringField("text", "Text to append.", true),
		)),
		tool("docs_replace", "Replace the body of a document.", "documents", "documents-tools", false, false, objectSchema(
			stringField("id", "Document id.", true), stringField("body", "Replacement body.", true),
		)),
		tool("docs_tag", "Add or remove a tag on a document.", "documents", "documents-tools", false, false, objectSchema(
			stringField("id", "Document id.", true), stringField("tag", "Tag value.", true), boolField("add", "True to add, false to remove.", true),
		)),
		tool("docs_publish", "Check whether a document is ready for publication.", "documents", "documents-tools", true, true, objectSchema(stringField("id", "Document id.", true))),
		tool("docs_set_permissions", "Preview effective document permissions for a principal.", "documents", "documents-tools", true, true, objectSchema(stringField("id", "Document id.", true), stringField("principal", "Principal.", true))),

		// Spreadsheets.
		tool("sheet_list", "List sheets and their columns.", "spreadsheets", "spreadsheets-tools", true, false, objectSchema()),
		tool("sheet_read_rows", "Read rows from a sheet.", "spreadsheets", "spreadsheets-tools", true, false, objectSchema(stringField("sheet_id", "Sheet id.", true), intField("limit", "Maximum rows.", false))),
		tool("sheet_find_rows", "Find rows in a sheet by column value.", "spreadsheets", "spreadsheets-tools", true, false, objectSchema(stringField("sheet_id", "Sheet id.", true), stringField("column", "Column name.", true), stringField("value", "Value to match.", true))),
		tool("sheet_update_cell", "Update one cell in a sheet.", "spreadsheets", "spreadsheets-tools", false, false, objectSchema(stringField("sheet_id", "Sheet id.", true), intField("row", "Zero-based row index.", true), stringField("column", "Column name.", true), stringField("value", "New cell value.", true))),
		tool("sheet_append_row", "Append one row to a sheet.", "spreadsheets", "spreadsheets-tools", false, false, objectSchema(stringField("sheet_id", "Sheet id.", true), stringField("values", "JSON object encoded as a string; each cell value may be a string, number, or boolean and is stored as text.", true))),
		tool("sheet_sum_column", "Sum numeric values in a sheet column.", "spreadsheets", "spreadsheets-tools", true, false, objectSchema(stringField("sheet_id", "Sheet id.", true), stringField("column", "Column name.", true))),
		tool("sheet_format", "Describe a presentation format available for a sheet.", "spreadsheets", "spreadsheets-tools", true, true, objectSchema(stringField("sheet_id", "Sheet id.", true), stringField("format", "Format name.", true))),
		tool("sheet_create_chart", "Describe a chart that could be created from a sheet column.", "spreadsheets", "spreadsheets-tools", true, true, objectSchema(stringField("sheet_id", "Sheet id.", true), stringField("column", "Column name.", true))),

		// Inventory.
		tool("inventory_search", "Search products by SKU or name.", "inventory", "inventory-tools", true, false, objectSchema(stringField("query", "SKU or product text.", true))),
		tool("inventory_get_stock", "Read current stock for a product.", "inventory", "inventory-tools", true, false, objectSchema(stringField("sku", "Product SKU.", true))),
		tool("inventory_reserve", "Reserve a quantity of a product.", "inventory", "inventory-tools", false, false, objectSchema(stringField("sku", "Product SKU.", true), intField("quantity", "Quantity to reserve.", true), stringField("customer", "Customer or order id.", true))),
		tool("inventory_release", "Release a reservation.", "inventory", "inventory-tools", false, false, objectSchema(stringField("reservation_id", "Reservation id.", true))),
		tool("inventory_set_reorder_point", "Set a product reorder point.", "inventory", "inventory-tools", false, false, objectSchema(stringField("sku", "Product SKU.", true), intField("reorder_point", "New reorder point.", true))),
		tool("inventory_list_reservations", "List reservations for a product or customer.", "inventory", "inventory-tools", true, false, objectSchema(stringField("sku", "Optional product SKU.", false), stringField("customer", "Optional customer.", false))),
		tool("inventory_ship", "Return shipment details for a reservation.", "inventory", "inventory-tools", true, true, objectSchema(stringField("reservation_id", "Reservation id.", true))),
		tool("inventory_adjust_cost", "Read accounting cost metadata for a SKU.", "inventory", "inventory-tools", true, true, objectSchema(stringField("sku", "Product SKU.", true))),

		// CRM.
		tool("crm_find_contact", "Find contacts by name or email.", "crm", "crm-tools", true, false, objectSchema(stringField("query", "Contact search text.", true))),
		tool("crm_get_contact", "Read one contact by id.", "crm", "crm-tools", true, false, objectSchema(stringField("id", "Contact id.", true))),
		tool("crm_update_contact", "Update selected fields of a contact.", "crm", "crm-tools", false, false, objectSchema(stringField("id", "Contact id.", true), stringField("company", "Company name.", false), stringField("phone", "Phone number.", false), stringField("notes", "Notes.", false))),
		tool("crm_log_activity", "Log a CRM activity for a contact.", "crm", "crm-tools", false, false, objectSchema(stringField("contact_id", "Contact id.", true), stringField("kind", "Activity kind.", true), stringField("note", "Activity note.", true))),
		tool("crm_create_task", "Create a follow-up task.", "crm", "crm-tools", false, false, objectSchema(stringField("contact_id", "Contact id.", true), stringField("title", "Task title.", true), stringField("due", "Due date.", true))),
		tool("crm_list_tasks", "List open CRM tasks.", "crm", "crm-tools", true, false, objectSchema(stringField("contact_id", "Optional contact id.", false))),
		tool("crm_delete_contact", "Preview the records affected by deleting a contact.", "crm", "crm-tools", true, true, objectSchema(stringField("id", "Contact id.", true))),
		tool("crm_merge_contacts", "Preview whether two contacts can be merged.", "crm", "crm-tools", true, true, objectSchema(stringField("source_id", "Source contact.", true), stringField("target_id", "Target contact.", true))),

		// Files.
		tool("files_list", "List files in the workspace.", "files", "files-tools", true, false, objectSchema(stringField("prefix", "Optional path prefix.", false))),
		tool("files_search", "Search file names and contents.", "files", "files-tools", true, false, objectSchema(stringField("query", "Search text.", true))),
		tool("files_read", "Read one file by path.", "files", "files-tools", true, false, objectSchema(stringField("path", "File path.", true))),
		tool("files_write", "Write or replace a file.", "files", "files-tools", false, false, objectSchema(stringField("path", "File path.", true), stringField("content", "File content.", true))),
		tool("files_move", "Move a file to another path.", "files", "files-tools", false, false, objectSchema(stringField("path", "Current path.", true), stringField("destination", "Destination path.", true))),
		tool("files_archive", "Archive or restore a file.", "files", "files-tools", false, false, objectSchema(stringField("path", "File path.", true), boolField("archived", "Whether to archive.", true))),
		tool("files_chmod", "Inspect the effective permissions for a file.", "files", "files-tools", true, true, objectSchema(stringField("path", "File path.", true))),
		tool("files_compress", "List files that would be included in an archive.", "files", "files-tools", true, true, objectSchema(stringField("prefix", "Path prefix.", true))),

		// Research.
		tool("research_search_notes", "Search research notes by text or topic.", "research", "research-tools", true, false, objectSchema(stringField("query", "Search text.", true))),
		tool("research_get_source", "Read a research source by id.", "research", "research-tools", true, false, objectSchema(stringField("id", "Source id.", true))),
		tool("research_save_finding", "Save a structured research finding.", "research", "research-tools", false, false, objectSchema(stringField("topic", "Finding topic.", true), stringField("claim", "Finding claim.", true), stringField("source_id", "Supporting source id.", true))),
		tool("research_tag_finding", "Add or remove a tag on a finding.", "research", "research-tools", false, false, objectSchema(stringField("id", "Finding id.", true), stringField("tag", "Tag value.", true), boolField("add", "True to add, false to remove.", true))),
		tool("research_list_findings", "List findings by topic.", "research", "research-tools", true, false, objectSchema(stringField("topic", "Optional topic.", false))),
		tool("research_summarize", "Summarize findings for a topic.", "research", "research-tools", true, false, objectSchema(stringField("topic", "Topic to summarize.", true))),
		tool("research_publish", "Check whether a finding is ready for publication.", "research", "research-tools", true, true, objectSchema(stringField("id", "Finding id.", true))),
		tool("research_translate", "Return translation metadata for a finding.", "research", "research-tools", true, true, objectSchema(stringField("id", "Finding id.", true), stringField("language", "Target language.", true))),
	}
	return skills, sets, tools
}

func tool(name, description, skillID, setID string, readOnly, distractor bool, schema *trpctool.Schema) ToolSpec {
	return ToolSpec{
		Name: name, Description: description, SkillID: skillID, ToolSetID: setID,
		InputSchema: schema, ReadOnly: readOnly, Distractor: distractor,
	}
}
