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
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	knowledgetool "trpc.group/trpc-go/trpc-agent-go/knowledge/tool"
)

const codeSearchSnapshot = "task_start"

// formatCodeSearchXMLLike renders one code_search result for the model. Source
// code is deliberately preserved verbatim; only XML attribute values are
// escaped.
func formatCodeSearchXMLLike(result any) (string, error) {
	response, ok := result.(*knowledgetool.KnowledgeSearchResponse)
	if !ok {
		return "", fmt.Errorf(
			"code_search returned %T, want *tool.KnowledgeSearchResponse",
			result,
		)
	}
	return renderCodeSearchXMLLike(response), nil
}

func renderCodeSearchXMLLike(result *knowledgetool.KnowledgeSearchResponse) string {
	var b strings.Builder
	b.WriteString(`<code_search_results snapshot="`)
	b.WriteString(codeSearchSnapshot)
	b.WriteString(`">`)
	b.WriteByte('\n')

	if result != nil {
		for _, document := range result.Documents {
			if document == nil {
				continue
			}
			b.WriteString("<result")
			writeCodeSearchAttribute(&b, "path", codeSearchPath(document.Metadata))
			writeCodeSearchAttribute(&b, "lines", codeSearchLines(document.Metadata))
			writeCodeSearchAttribute(&b, "symbol", codeSearchSymbol(document.Metadata))
			b.WriteString(">\n<code>\n")
			b.WriteString(document.Text)
			if !strings.HasSuffix(document.Text, "\n") {
				b.WriteByte('\n')
			}
			b.WriteString("</code>\n</result>\n")
		}
	}

	b.WriteString("</code_search_results>")
	return b.String()
}

func writeCodeSearchAttribute(b *strings.Builder, name, value string) {
	if value == "" {
		return
	}
	b.WriteByte(' ')
	b.WriteString(name)
	b.WriteString(`="`)
	_ = xml.EscapeText(b, []byte(value))
	b.WriteByte('"')
}

func codeSearchPath(metadata map[string]any) string {
	return firstCodeSearchMetadataString(
		metadata,
		"trpc_ast_file_path",
		"trpc_agent_go_file_path",
	)
}

func codeSearchSymbol(metadata map[string]any) string {
	if nodeType := firstCodeSearchMetadataString(metadata, "trpc_ast_type"); strings.EqualFold(nodeType, "file") {
		return ""
	}
	symbol := firstCodeSearchMetadataString(metadata, "trpc_ast_full_name")
	if symbol == "" || symbol == codeSearchPath(metadata) {
		return ""
	}
	return symbol
}

func firstCodeSearchMetadataString(metadata map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := metadata[key].(string)
		if !ok {
			continue
		}
		if value = strings.TrimSpace(value); value != "" {
			return value
		}
	}
	return ""
}

func codeSearchLines(metadata map[string]any) string {
	start, hasStart := codeSearchLine(metadata, "trpc_ast_line_start")
	end, hasEnd := codeSearchLine(metadata, "trpc_ast_line_end")
	if !hasStart || !hasEnd || start > end {
		return ""
	}
	if start == end {
		return strconv.FormatInt(start, 10)
	}
	return fmt.Sprintf("%d-%d", start, end)
}

func codeSearchLine(metadata map[string]any, key string) (int64, bool) {
	value, ok := metadata[key]
	if !ok {
		return 0, false
	}
	line, err := strconv.ParseInt(fmt.Sprint(value), 10, 64)
	return line, err == nil && line > 0
}
