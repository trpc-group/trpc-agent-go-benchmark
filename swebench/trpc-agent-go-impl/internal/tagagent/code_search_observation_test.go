//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package tagagent

import (
	"strings"
	"testing"

	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
)

func TestFormatCodeSearchXML(t *testing.T) {
	result := &knowledgetool.KnowledgeSearchResponse{
		Documents: []*knowledgetool.DocumentResult{
			{
				ID:    "internal-id-must-not-be-rendered",
				Text:  "def find_user(user_id):\n    return users[user_id]\n",
				Score: 0.875,
				Metadata: map[string]any{
					"trpc_ast_file_path":  `pkg/"users"&<lookup>.py`,
					"trpc_ast_line_start": 10,
					"trpc_ast_line_end":   11,
					"trpc_ast_full_name":  `pkg.users."find"&<user>`,
					"private":             "must-not-be-rendered",
				},
			},
			{
				Text: "if value < limit && ready {\n\treturn value\n}",
				Metadata: map[string]any{
					"trpc_agent_go_file_path": "fallback.go",
					"trpc_ast_line_start":     int64(7),
					"trpc_ast_line_end":       int64(9),
					"trpc_ast_name":           "must-not-be-used-as-symbol",
				},
			},
			{
				Text: "file contents",
				Metadata: map[string]any{
					"trpc_ast_type":      "file",
					"trpc_ast_file_path": "whole.py",
					"trpc_ast_full_name": "whole.py",
				},
			},
			nil,
			{Text: "plain text"},
		},
		Message: "must-not-be-rendered",
	}

	want := `<code_search_results snapshot="task_start">
<result path="pkg/&#34;users&#34;&amp;&lt;lookup&gt;.py" lines="10-11" symbol="pkg.users.&#34;find&#34;&amp;&lt;user&gt;">
<code>
def find_user(user_id):
    return users[user_id]
</code>
</result>
<result path="fallback.go" lines="7-9">
<code>
if value < limit && ready {
	return value
}
</code>
</result>
<result path="whole.py">
<code>
file contents
</code>
</result>
<result>
<code>
plain text
</code>
</result>
</code_search_results>`
	got, err := formatCodeSearchXMLLike(result)
	if err != nil {
		t.Fatalf("formatCodeSearchXMLLike() error = %v", err)
	}
	if got != want {
		t.Fatalf("formatCodeSearchXMLLike() mismatch\ngot:\n%s\n\nwant:\n%s", got, want)
	}
}

func TestFormatCodeSearchXMLEmpty(t *testing.T) {
	want := "<code_search_results snapshot=\"task_start\">\n</code_search_results>"
	for _, result := range []*knowledgetool.KnowledgeSearchResponse{
		{},
		{Documents: []*knowledgetool.DocumentResult{nil}, Message: "ignored"},
	} {
		got, err := formatCodeSearchXMLLike(result)
		if err != nil {
			t.Fatalf("formatCodeSearchXMLLike(%#v) error = %v", result, err)
		}
		if got != want {
			t.Fatalf("formatCodeSearchXMLLike(%#v) = %q, want %q", result, got, want)
		}
	}
}

func TestFormatCodeSearchXMLLikeRejectsUnexpectedResult(t *testing.T) {
	for _, result := range []any{nil, knowledgetool.KnowledgeSearchResponse{}, "invalid"} {
		if _, err := formatCodeSearchXMLLike(result); err == nil {
			t.Fatalf("formatCodeSearchXMLLike(%T) error = nil, want type error", result)
		}
	}
}

func TestFormatCodeSearchXMLOmitsMissingAndInvalidAttributes(t *testing.T) {
	result := &knowledgetool.KnowledgeSearchResponse{Documents: []*knowledgetool.DocumentResult{{
		Text: "x = 1",
		Metadata: map[string]any{
			"trpc_ast_file_path":  "  ",
			"trpc_ast_line_start": 0,
			"trpc_ast_line_end":   "not-a-line",
			"trpc_ast_full_name":  123,
		},
	}}}

	got, err := formatCodeSearchXMLLike(result)
	if err != nil {
		t.Fatalf("formatCodeSearchXMLLike() error = %v", err)
	}
	if strings.Contains(got, " path=") || strings.Contains(got, " lines=") ||
		strings.Contains(got, " symbol=") {
		t.Fatalf("missing or invalid attributes must be omitted: %s", got)
	}
	if !strings.Contains(got, "<code>\nx = 1\n</code>") {
		t.Fatalf("source code was not preserved: %s", got)
	}
}

func TestCodeSearchLinesRequiresValidRange(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		want     string
	}{
		{name: "same range", metadata: map[string]any{
			"trpc_ast_line_start": 3,
			"trpc_ast_line_end":   3,
		}, want: "3"},
		{name: "start only", metadata: map[string]any{
			"trpc_ast_line_start": 2,
		}, want: ""},
		{name: "end only", metadata: map[string]any{
			"trpc_ast_line_end": 9,
		}, want: ""},
		{name: "reversed", metadata: map[string]any{
			"trpc_ast_line_start": 9,
			"trpc_ast_line_end":   2,
		}, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := codeSearchLines(test.metadata); got != test.want {
				t.Fatalf("codeSearchLines() = %q, want %q", got, test.want)
			}
		})
	}
}
