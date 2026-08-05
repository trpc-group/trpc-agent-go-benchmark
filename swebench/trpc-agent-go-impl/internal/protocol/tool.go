//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/sweenv"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const (
	// SubmissionMarker is the first successful output line that ends a case.
	SubmissionMarker    = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"
	formatErrorTemplate = `Tool call error:

<error>
%s
</error>

Here is general guidance on how to submit correct toolcalls:

Every response needs to use the 'bash' tool at least once to execute commands.

Call the bash tool with your command as the argument:
- Tool: bash
- Arguments: {"command": "your_command_here"}

If you have completed your assignment, please consult the first message about how to
submit your solution (you will not be able to continue working on this task after that).`
)

// Action is one validated bash action.
type Action struct {
	Command    string `json:"command"`
	ToolCallID string `json:"tool_call_id"`
}

// FormatError identifies a response that should be replaced and retried.
type FormatError struct{ Message string }

func (e FormatError) Error() string { return e.Message }

// BashDeclaration returns the source-aligned model-facing tool declaration.
func BashDeclaration() *tool.Declaration {
	return &tool.Declaration{
		Name:        "bash",
		Description: "Execute a bash command",
		InputSchema: &tool.Schema{
			Type:     "object",
			Required: []string{"command"},
			Properties: map[string]*tool.Schema{
				"command": {Type: "string", Description: "The bash command to execute"},
			},
		},
	}
}

// ParseActions mirrors v2.1.0 actions_toolcall.parse_toolcall_actions.
func ParseActions(toolCalls []model.ToolCall) ([]Action, error) {
	if len(toolCalls) == 0 {
		return nil, formatError("No tool calls found in the response. Every response MUST include at least one tool call.")
	}
	actions := make([]Action, 0, len(toolCalls))
	for _, call := range toolCalls {
		var arguments map[string]json.RawMessage
		var errorText strings.Builder
		if err := json.Unmarshal(call.Function.Arguments, &arguments); err != nil {
			fmt.Fprintf(&errorText, "Error parsing tool call arguments: %s. ", pythonJSONError(call.Function.Arguments, err))
			arguments = map[string]json.RawMessage{}
		}
		if call.Function.Name != "bash" {
			fmt.Fprintf(&errorText, "Unknown tool '%s'.", call.Function.Name)
		}
		rawCommand, found := arguments["command"]
		if !found {
			errorText.WriteString("Missing 'command' argument in bash tool call.")
		}
		var command string
		if found {
			if err := json.Unmarshal(rawCommand, &command); err != nil {
				fmt.Fprintf(&errorText, "Error parsing tool call arguments: %v. ", err)
			}
		}
		if errorText.Len() > 0 {
			return nil, formatError(strings.TrimSpace(errorText.String()))
		}
		actions = append(actions, Action{Command: command, ToolCallID: call.ID})
	}
	return actions, nil
}

func pythonJSONError(input []byte, err error) string {
	if !strings.Contains(err.Error(), "unexpected end of JSON input") {
		return err.Error()
	}
	text := string(input)
	line := 1 + strings.Count(text, "\n")
	lastNewline := strings.LastIndex(text, "\n")
	lineText := text
	if lastNewline >= 0 {
		lineText = text[lastNewline+1:]
	}
	column := len([]rune(lineText)) + 1
	character := len([]rune(text))
	return fmt.Sprintf("Expecting value: line %d column %d (char %d)", line, column, character)
}

func formatError(message string) error {
	return FormatError{Message: fmt.Sprintf(formatErrorTemplate, message)}
}

// SubmissionFromResult extracts a patch from a successful terminal command.
// The bool is deliberately independent of patch contents: a valid submission
// may contain an empty patch.
func SubmissionFromResult(result sweenv.CommandResult) (string, bool) {
	if result.ReturnCode != 0 {
		return "", false
	}
	trimmed := strings.TrimLeftFunc(result.Output, unicode.IsSpace)
	first, rest := splitFirstLineKeepEnds(trimmed)
	if strings.TrimSpace(first) != SubmissionMarker {
		return "", false
	}
	return rest, true
}

func splitFirstLineKeepEnds(value string) (string, string) {
	for index, character := range value {
		size := len(string(character))
		switch character {
		case '\n', '\v', '\f', '\u001c', '\u001d', '\u001e', '\u0085', '\u2028', '\u2029':
			return value[:index+size], value[index+size:]
		case '\r':
			end := index + size
			if end < len(value) && value[end] == '\n' {
				end++
			}
			return value[:end], value[end:]
		}
	}
	return value, ""
}
