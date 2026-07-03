//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/result"
)

const (
	submissionSentinel     = "COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT"
	maxDetailedHistoryStep = 8
	maxRepeatedCommands    = 3
)

type stepTrace struct {
	Step      int           `json:"step"`
	Assistant string        `json:"assistant"`
	Action    agentAction   `json:"action"`
	Command   commandResult `json:"command,omitempty"`
	Usage     result.Usage  `json:"usage,omitempty"`
	Error     string        `json:"error,omitempty"`
}

type instanceTrace struct {
	InstanceID    string      `json:"instance_id"`
	Repo          string      `json:"repo,omitempty"`
	BaseCommit    string      `json:"base_commit,omitempty"`
	Model         string      `json:"model"`
	Environment   string      `json:"environment,omitempty"`
	Image         string      `json:"image,omitempty"`
	Status        string      `json:"status"`
	StopReason    string      `json:"stop_reason,omitempty"`
	Steps         []stepTrace `json:"steps"`
	RevertedFiles []string    `json:"reverted_files,omitempty"`
	Error         string      `json:"error,omitempty"`
}

type loopResult struct {
	Patch        string
	ChangedFiles []string
	PatchAdded   int
	PatchDeleted int
	Status       string
	Usage        result.Usage
	DurationMs   int64
	Trace        instanceTrace
	Err          error
}

type agentAction struct {
	Thought string `json:"thought,omitempty"`
	Command string `json:"command,omitempty"`
	Final   string `json:"final,omitempty"`
}

func runInstance(ctx context.Context, client *chatClient, inst dataset.Instance, ws workspace, req RunRequest) loopResult {
	start := time.Now()
	trace := instanceTrace{
		InstanceID:  inst.InstanceID,
		Repo:        inst.Repo,
		BaseCommit:  inst.BaseCommit,
		Model:       req.Model,
		Environment: nativeEnvironment(req.Environment),
		Image:       ws.Image,
		Status:      "incomplete",
	}
	messages := []chatMessage{
		{Role: "system", Content: sweSystemPrompt()},
		{Role: "user", Content: sweUserPrompt(inst)},
	}
	totalUsage := result.Usage{}
	var loopErr error
	var stopReason string
	var submittedPatch string
	lastCommand := ""
	repeatedCommands := 0
	for step := 1; step <= req.StepLimit; step++ {
		resp, err := client.chat(ctx, messages)
		totalUsage = addUsage(totalUsage, resp.Usage)
		st := stepTrace{Step: step, Assistant: resp.Content, Usage: resp.Usage}
		if err != nil {
			st.Error = err.Error()
			trace.Steps = append(trace.Steps, st)
			loopErr = err
			break
		}
		if len(resp.ToolCalls) > 0 {
			messages = append(messages, assistantMessage(resp))
			submitted := false
			for i, call := range resp.ToolCalls {
				action, parseErr := toolCallAction(call)
				cmdTrace := st
				if i > 0 {
					cmdTrace.Usage = result.Usage{}
				}
				cmdTrace.Action = action
				if parseErr != nil {
					cmdTrace.Error = parseErr.Error()
					trace.Steps = append(trace.Steps, cmdTrace)
					messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: miniFormatError(parseErr)})
					continue
				}
				if repeatedCommand(action.Command, &lastCommand, &repeatedCommands) {
					err := fmt.Errorf("the same bash command was repeated %d times; choose a different command, inspect/create patch.txt, or submit with %s", repeatedCommands, submissionSentinel)
					cmdTrace.Error = err.Error()
					cmdTrace.Command = commandResult{
						Command:  action.Command,
						Output:   err.Error(),
						ExitCode: 2,
						Rejected: true,
					}
					trace.Steps = append(trace.Steps, cmdTrace)
					messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: miniFormatError(err)})
					continue
				}
				cmdRes := ws.exec(ctx, action.Command, commandTimeout(req.Timeout), req.MaxOutputChars)
				cmdTrace.Command = cmdRes
				trace.Steps = append(trace.Steps, cmdTrace)
				if strings.Contains(cmdRes.Output, submissionSentinel) || strings.Contains(action.Command, submissionSentinel) {
					stopReason = "submitted"
					submittedPatch = extractSubmittedPatch(cmdRes.Output)
					submitted = true
					break
				}
				messages = append(messages, chatMessage{Role: "tool", ToolCallID: call.ID, Content: observationMessage(cmdRes)})
			}
			if submitted {
				break
			}
		} else {
			action, err := parseAction(resp.Content)
			st.Action = action
			messages = append(messages, assistantMessage(resp))
			if err != nil {
				st.Error = err.Error()
				trace.Steps = append(trace.Steps, st)
				messages = append(messages, chatMessage{Role: "user", Content: miniFormatError(err)})
				continue
			}
			if action.Final != "" {
				trace.Steps = append(trace.Steps, st)
				break
			}
			if strings.TrimSpace(action.Command) == "" {
				st.Error = "missing command or final"
				trace.Steps = append(trace.Steps, st)
				messages = append(messages, chatMessage{Role: "user", Content: miniFormatError(fmt.Errorf("missing bash tool call"))})
				continue
			}
			if repeatedCommand(action.Command, &lastCommand, &repeatedCommands) {
				err := fmt.Errorf("the same bash command was repeated %d times; choose a different command, inspect/create patch.txt, or submit with %s", repeatedCommands, submissionSentinel)
				st.Error = err.Error()
				st.Command = commandResult{
					Command:  action.Command,
					Output:   err.Error(),
					ExitCode: 2,
					Rejected: true,
				}
				trace.Steps = append(trace.Steps, st)
				messages = append(messages, chatMessage{Role: "user", Content: miniFormatError(err)})
				continue
			}
			cmdRes := ws.exec(ctx, action.Command, commandTimeout(req.Timeout), req.MaxOutputChars)
			st.Command = cmdRes
			trace.Steps = append(trace.Steps, st)
			if strings.Contains(cmdRes.Output, submissionSentinel) || strings.Contains(action.Command, submissionSentinel) {
				stopReason = "submitted"
				submittedPatch = extractSubmittedPatch(cmdRes.Output)
				break
			}
			messages = append(messages, chatMessage{Role: "user", Content: observationMessage(cmdRes)})
		}
		if req.TokenLimit > 0 && totalUsage.TotalTokens > req.TokenLimit {
			stopReason = fmt.Sprintf("token_limit_reached:%d>%d", totalUsage.TotalTokens, req.TokenLimit)
			break
		}
	}
	files, filesErr := ws.changedFiles(ctx)
	if filesErr != nil && loopErr == nil {
		loopErr = filesErr
	}
	forbidden := forbiddenPatchFiles(files)
	if len(forbidden) > 0 {
		if err := ws.revertFiles(ctx, forbidden); err != nil && loopErr == nil {
			loopErr = err
		} else {
			trace.RevertedFiles = forbidden
			files, filesErr = ws.changedFiles(ctx)
			if filesErr != nil && loopErr == nil {
				loopErr = filesErr
			}
		}
	}
	patch := submittedPatch
	if strings.TrimSpace(patch) == "" {
		var diffErr error
		patch, diffErr = ws.diff(ctx)
		if diffErr != nil && loopErr == nil {
			loopErr = diffErr
		}
		if diffErr != nil {
			patch = ""
		}
	} else {
		files = patchChangedFiles(patch)
	}
	added, deleted := patchStats(patch)
	status := "pending_verifier"
	if strings.TrimSpace(patch) == "" {
		status = "incomplete"
	}
	if loopErr != nil {
		status = "error"
		trace.Error = loopErr.Error()
	}
	trace.Status = status
	trace.StopReason = stopReason
	return loopResult{
		Patch:        patch,
		ChangedFiles: files,
		PatchAdded:   added,
		PatchDeleted: deleted,
		Status:       status,
		Usage:        totalUsage,
		DurationMs:   time.Since(start).Milliseconds(),
		Trace:        trace,
		Err:          loopErr,
	}
}

func sweSystemPrompt() string {
	return `You are a helpful assistant that can interact with a computer shell to solve programming tasks.`
}

func sweUserPrompt(inst dataset.Instance) string {
	return strings.Join([]string{
		"<pr_description>",
		"Consider the following PR description:",
		inst.ProblemStatement,
		"</pr_description>",
		"",
		"<instructions>",
		"# Task Instructions",
		"",
		"## Overview",
		"",
		"You're a software engineer interacting continuously with a computer by submitting commands.",
		"You'll be helping implement necessary changes to meet requirements in the PR description.",
		"Your task is specifically to make changes to non-test files in the current directory in order to fix the issue described in the PR description in a way that is general and consistent with the codebase.",
		"<IMPORTANT>This is an interactive process where you will think and issue AT LEAST ONE command, see the result, then think and issue your next command(s).</important>",
		"",
		"For each response:",
		"",
		"1. Include a THOUGHT section explaining your reasoning and what you're trying to accomplish",
		"2. Provide one or more bash tool calls to execute",
		"",
		"## Important Boundaries",
		"",
		"- MODIFY: Regular source code files in /testbed (this is the working directory for all your subsequent commands)",
		"- DO NOT MODIFY: Tests, configuration files (pyproject.toml, setup.cfg, etc.)",
		"",
		"## Recommended Workflow",
		"",
		"1. Analyze the codebase by finding and reading relevant files",
		"2. Create a script to reproduce the issue",
		"3. Edit the source code to resolve the issue",
		"4. Verify your fix works by running your script again",
		"5. Test edge cases to ensure your fix is robust",
		"",
		"## Command Execution Rules",
		"",
		"You are operating in an environment where",
		"",
		"1. You issue at least one command",
		"2. The system executes the command(s) in a subshell",
		"3. You see the result(s)",
		"4. You write your next command(s)",
		"",
		"Each response should include:",
		"",
		"1. **Reasoning text** where you explain your analysis and plan",
		"2. At least one tool call with your command",
		"",
		"**CRITICAL REQUIREMENTS:**",
		"",
		"- Your response SHOULD include reasoning text explaining what you're doing",
		"- Your response MUST include AT LEAST ONE bash tool call. You can make MULTIPLE tool calls in a single response when the commands are independent (e.g., searching multiple files, reading different parts of the codebase).",
		"- Directory or environment variable changes are not persistent. Every action is executed in a new subshell.",
		"- However, you can prefix any action with `MY_ENV_VAR=MY_VALUE cd /path/to/working/dir && ...` or write/load environment variables from files",
		"",
		"Example of a CORRECT response:",
		"<example_response>",
		"I need to understand the Builder-related code. Let me find relevant files and check the project structure.",
		"",
		"[Makes multiple bash tool calls: {\"command\": \"ls -la\"}, {\"command\": \"find src -name '*.java' | grep -i builder\"}, {\"command\": \"cat README.md | head -50\"}]",
		"</example_response>",
		"",
		"## Environment Details",
		"",
		"- You have a full Linux shell environment",
		"- Always use non-interactive flags (-y, -f) for commands",
		"- Avoid interactive tools like vi, nano, or any that require user input",
		"- You can use bash commands or invoke any tool that is available in the environment",
		"- You can also create new tools or scripts to help you with the task",
		"- If a tool isn't available, you can also install it",
		"",
		"## Submission",
		"",
		"When you've completed your work, you MUST submit your changes as a git patch.",
		"Follow these steps IN ORDER, with SEPARATE commands:",
		"",
		"Step 1: Create the patch file",
		"Run `git diff -- path/to/file1 path/to/file2 > patch.txt` listing only the source files you modified.",
		"Do NOT commit your changes.",
		"",
		"<IMPORTANT>",
		"The patch must only contain changes to the specific source files you modified to fix the issue.",
		"Do not submit file creations or changes to any of the following files:",
		"",
		"- test and reproduction files",
		"- helper scripts, tests, or tools that you created",
		"- installation, build, packaging, configuration, or setup scripts unless they are directly part of the issue you were fixing (you can assume that the environment is already set up for your client)",
		"- binary or compiled files",
		"</IMPORTANT>",
		"",
		"Step 2: Verify your patch",
		"Inspect patch.txt to confirm it only contains your intended changes and headers show `--- a/` and `+++ b/` paths.",
		"",
		"Step 3: Submit (EXACT command required)",
		"You MUST use this EXACT command to submit:",
		"",
		"```bash",
		"echo COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT && cat patch.txt",
		"```",
		"",
		"If the command fails (nonzero exit status), it will not submit.",
		"",
		"<CRITICAL>",
		"- Creating/viewing the patch and submitting it MUST be separate commands (not combined with &&).",
		"- If you modify patch.txt after verifying, you SHOULD verify again before submitting.",
		"- You CANNOT continue working (reading, editing, testing) in any way on this task after submitting.",
		"</CRITICAL>",
		"</instructions>",
	}, "\n")
}

func observationMessage(cmdRes commandResult) string {
	var b strings.Builder
	if cmdRes.Rejected {
		b.WriteString("<exception>command rejected</exception>\n")
	}
	b.WriteString(fmt.Sprintf("<returncode>%d</returncode>\n", cmdRes.ExitCode))
	if !cmdRes.OutputTruncated && len(cmdRes.Output) < 10000 {
		b.WriteString("<output>\n")
		b.WriteString(cmdRes.Output)
		if !strings.HasSuffix(cmdRes.Output, "\n") {
			b.WriteString("\n")
		}
		b.WriteString("</output>")
		return b.String()
	}
	b.WriteString("<warning>\n")
	b.WriteString("The output of your last command was too long.\n")
	b.WriteString("Please try a different command that produces less output.\n")
	b.WriteString("If you're looking at a file you can try use head, tail or sed to view a smaller number of lines selectively.\n")
	b.WriteString("If you're using grep or find and it produced too much output, you can use a more selective search pattern.\n")
	b.WriteString("If you really need to see something from the full command's output, you can redirect output to a file and then search in that file.\n")
	b.WriteString("</warning>\n")
	head, tail := outputHeadTail(cmdRes.Output)
	elided := cmdRes.OutputBytes - len(head) - len(tail)
	if elided < 0 {
		elided = 0
	}
	b.WriteString("<output_head>\n")
	b.WriteString(head)
	if !strings.HasSuffix(head, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</output_head>\n")
	b.WriteString("<elided_chars>\n")
	b.WriteString(fmt.Sprintf("%d characters elided\n", elided))
	b.WriteString("</elided_chars>\n")
	b.WriteString("<output_tail>\n")
	b.WriteString(tail)
	if !strings.HasSuffix(tail, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("</output_tail>")
	return b.String()
}

func assistantMessage(resp chatResponse) chatMessage {
	return chatMessage{
		Role:             "assistant",
		Content:          resp.Content,
		ReasoningContent: resp.ReasoningContent,
		ToolCalls:        resp.ToolCalls,
	}
}

func toolCallAction(call toolCall) (agentAction, error) {
	if call.Function.Name != "bash" {
		return agentAction{}, fmt.Errorf("unsupported tool %q", call.Function.Name)
	}
	var args struct {
		Command string `json:"command"`
	}
	if err := json.Unmarshal([]byte(call.Function.Arguments), &args); err != nil {
		return agentAction{}, fmt.Errorf("parse bash tool arguments: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return agentAction{}, fmt.Errorf("bash tool call missing command")
	}
	return agentAction{Command: args.Command}, nil
}

func miniFormatError(err error) string {
	return fmt.Sprintf(`Tool call error:

<error>
%s
</error>

Here is general guidance on how to submit correct toolcalls:

Every response needs to use the 'bash' tool at least once to execute commands.

Call the bash tool with your command as the argument:
- Tool: bash
- Arguments: {"command": "your_command_here"}

If you have completed your assignment, please consult the first message about how to
submit your solution (you will not be able to continue working on this task after that).`, err)
}

func repeatedCommand(command string, lastCommand *string, repeated *int) bool {
	normalized := normalizeCommand(command)
	if normalized == "" {
		*lastCommand = ""
		*repeated = 0
		return false
	}
	if normalized == *lastCommand {
		*repeated++
	} else {
		*lastCommand = normalized
		*repeated = 1
	}
	return *repeated > maxRepeatedCommands
}

func normalizeCommand(command string) string {
	return strings.Join(strings.Fields(command), " ")
}

func outputHeadTail(output string) (string, string) {
	const marker = "\n... output truncated ...\n"
	if strings.Contains(output, marker) {
		parts := strings.SplitN(output, marker, 2)
		return parts[0], parts[1]
	}
	if len(output) <= 10000 {
		return output, ""
	}
	return output[:5000], output[len(output)-5000:]
}

func extractSubmittedPatch(output string) string {
	idx := strings.Index(output, submissionSentinel)
	if idx < 0 {
		return ""
	}
	patch := output[idx+len(submissionSentinel):]
	patch = strings.TrimLeft(patch, "\r\n\t ")
	return strings.TrimSpace(patch)
}

func patchChangedFiles(patch string) []string {
	files := make([]string, 0)
	seen := map[string]bool{}
	for _, line := range strings.Split(patch, "\n") {
		if !strings.HasPrefix(line, "+++ b/") {
			continue
		}
		file := strings.TrimSpace(strings.TrimPrefix(line, "+++ b/"))
		if file == "" || file == "/dev/null" || seen[file] {
			continue
		}
		seen[file] = true
		files = append(files, file)
	}
	return files
}

func compactMessages(messages []chatMessage, steps []stepTrace) []chatMessage {
	const prefixMessages = 2
	if len(messages) <= prefixMessages+2*maxDetailedHistoryStep {
		return messages
	}
	keepStart := len(messages) - 2*maxDetailedHistoryStep
	if keepStart < prefixMessages {
		keepStart = prefixMessages
	}
	summary := summarizeOlderSteps(steps, maxDetailedHistoryStep)
	out := make([]chatMessage, 0, prefixMessages+1+len(messages)-keepStart)
	out = append(out, messages[:prefixMessages]...)
	if summary != "" {
		out = append(out, chatMessage{Role: "user", Content: summary})
	}
	out = append(out, messages[keepStart:]...)
	return out
}

func summarizeOlderSteps(steps []stepTrace, keepLast int) string {
	if len(steps) <= keepLast {
		return ""
	}
	older := steps[:len(steps)-keepLast]
	var b strings.Builder
	b.WriteString("<previous_steps_summary>\n")
	for _, st := range older {
		action := st.Action.Command
		if action == "" {
			action = st.Action.Final
		}
		if action == "" {
			action = "(format/error step)"
		}
		exit := ""
		if st.Command.Command != "" {
			exit = fmt.Sprintf(" exit=%d", st.Command.ExitCode)
		}
		b.WriteString(fmt.Sprintf("step %d%s: %s\n", st.Step, exit, firstLine(action, 180)))
	}
	b.WriteString("</previous_steps_summary>\n")
	b.WriteString("Older command outputs are summarized to control context size. Use current files and git diff for exact state.")
	return b.String()
}

func parseAction(content string) (agentAction, error) {
	raw := strings.TrimSpace(content)
	if start := strings.Index(raw, "{"); start >= 0 {
		if end := strings.LastIndex(raw, "}"); end >= start {
			raw = raw[start : end+1]
		}
	}
	var action agentAction
	if err := json.Unmarshal([]byte(raw), &action); err != nil {
		action, fallbackErr := parseLenientAction(raw)
		if fallbackErr != nil {
			return agentAction{}, err
		}
		if strings.TrimSpace(action.Command) == "" && strings.TrimSpace(action.Final) == "" {
			return agentAction{}, fmt.Errorf("action must contain command or final")
		}
		return action, nil
	}
	if strings.TrimSpace(action.Command) == "" && strings.TrimSpace(action.Final) == "" {
		return agentAction{}, fmt.Errorf("action must contain command or final")
	}
	return action, nil
}

func parseLenientAction(raw string) (agentAction, error) {
	thought, _ := jsonishStringField(raw, "thought")
	command, _ := jsonishStringField(raw, "command")
	final, _ := jsonishStringField(raw, "final")
	if strings.TrimSpace(command) == "" && strings.TrimSpace(final) == "" {
		return agentAction{}, fmt.Errorf("lenient parse found no command/final")
	}
	return agentAction{
		Thought: thought,
		Command: command,
		Final:   final,
	}, nil
}

func jsonishStringField(raw, key string) (string, bool) {
	keyPattern := `"` + key + `"`
	keyIndex := strings.Index(raw, keyPattern)
	if keyIndex < 0 {
		return "", false
	}
	rest := raw[keyIndex+len(keyPattern):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return "", false
	}
	rest = rest[colon+1:]
	start := strings.Index(rest, `"`)
	if start < 0 {
		return "", false
	}
	rest = rest[start+1:]
	var b strings.Builder
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch == '"' {
			return b.String(), true
		}
		if ch != '\\' || i+1 >= len(rest) {
			b.WriteByte(ch)
			continue
		}
		i++
		switch rest[i] {
		case '"', '\\', '/':
			b.WriteByte(rest[i])
		case 'b':
			b.WriteByte('\b')
		case 'f':
			b.WriteByte('\f')
		case 'n':
			b.WriteByte('\n')
		case 'r':
			b.WriteByte('\r')
		case 't':
			b.WriteByte('\t')
		default:
			// Models sometimes escape shell metacharacters like \|.
			b.WriteByte(rest[i])
		}
	}
	return "", false
}

func addUsage(a, b result.Usage) result.Usage {
	return result.Usage{
		PromptTokens:     a.PromptTokens + b.PromptTokens,
		CompletionTokens: a.CompletionTokens + b.CompletionTokens,
		TotalTokens:      a.TotalTokens + b.TotalTokens,
		APICalls:         a.APICalls + b.APICalls,
		Retries:          a.Retries + b.Retries,
	}
}

func commandTimeout(instanceTimeout time.Duration) time.Duration {
	if instanceTimeout <= 0 {
		return 5 * time.Minute
	}
	if instanceTimeout < 5*time.Minute {
		return instanceTimeout
	}
	return 5 * time.Minute
}

func patchStats(patch string) (added int, deleted int) {
	for _, line := range strings.Split(patch, "\n") {
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			continue
		case strings.HasPrefix(line, "+"):
			added++
		case strings.HasPrefix(line, "-"):
			deleted++
		}
	}
	return added, deleted
}

func formatTestList(tests []string) string {
	if len(tests) == 0 {
		return "(none listed)"
	}
	return "- " + strings.Join(tests, "\n- ")
}

func forbiddenPatchFiles(files []string) []string {
	forbidden := make([]string, 0)
	for _, file := range files {
		clean := strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
		lower := strings.ToLower(clean)
		switch {
		case lower == "":
			continue
		case strings.HasPrefix(lower, "tests/"),
			strings.HasPrefix(lower, "test/"),
			strings.Contains(lower, "/tests/"),
			strings.Contains(lower, "/test/"),
			strings.HasSuffix(lower, "_test.go"),
			strings.HasPrefix(filepathBase(lower), "test_"):
			forbidden = append(forbidden, clean)
		}
	}
	return forbidden
}

func sourcePatchFiles(files []string) []string {
	forbidden := map[string]bool{}
	for _, file := range forbiddenPatchFiles(files) {
		forbidden[file] = true
	}
	source := make([]string, 0, len(files))
	for _, file := range files {
		clean := strings.TrimSpace(strings.ReplaceAll(file, "\\", "/"))
		if clean == "" || forbidden[clean] {
			continue
		}
		source = append(source, clean)
	}
	return source
}

func isInspectionOnlyCommand(command string) bool {
	trimmed := strings.TrimSpace(strings.ToLower(command))
	if trimmed == "" {
		return false
	}
	editHints := []string{
		">",
		"tee ",
		"python - <<",
		"python3 - <<",
		"perl -",
		"sed -i",
		"apply_patch",
	}
	for _, hint := range editHints {
		if strings.Contains(trimmed, hint) {
			return false
		}
	}
	testHints := []string{"pytest", "go test", "tox ", "npm test"}
	for _, hint := range testHints {
		if strings.Contains(trimmed, hint) {
			return false
		}
	}
	inspectionStarts := []string{
		"grep ",
		"rg ",
		"sed -n",
		"cat ",
		"ls",
		"find ",
		"git grep",
		"git log",
		"git show",
		"git blame",
		"python -c",
		"python3 -c",
	}
	for _, prefix := range inspectionStarts {
		if strings.HasPrefix(trimmed, prefix) || strings.Contains(trimmed, "&& "+prefix) {
			return true
		}
	}
	return false
}

func filepathBase(path string) string {
	if i := strings.LastIndex(path, "/"); i >= 0 {
		return path[i+1:]
	}
	return path
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= 3 {
		return s[:max]
	}
	return s[:max-3] + "..."
}
