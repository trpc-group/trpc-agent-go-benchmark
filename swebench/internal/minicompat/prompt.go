//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package minicompat contains the mini-SWE-agent v2.1 model-facing protocol.
package minicompat

import "strings"

// SystemPrompt is rendered from v2.1.0 config/benchmarks/swebench.yaml.
const SystemPrompt = "You are a helpful assistant that can interact with a computer shell to solve programming tasks."

const offlineGuidance = "The shell has no public internet access; only declared local services are reachable. " +
	"Use the PR description, repository and base-or-earlier local history, local tests, and locally available tools and dependencies. " +
	"If an optional dependency is absent, continue with the available evidence."

// OfflineSystemPrompt adapts the source-aligned system prompt for an isolated
// generation environment.
const OfflineSystemPrompt = SystemPrompt + " " + offlineGuidance

// SystemPromptWithCodeSearch adapts the source-aligned system prompt for an
// agent that can also search a task-start workspace index.
const SystemPromptWithCodeSearch = SystemPrompt + " You can also use code_search to search a static task-start snapshot of the workspace."

// OfflineSystemPromptWithCodeSearch combines the isolated environment
// guidance with the code-search capability description.
const OfflineSystemPromptWithCodeSearch = OfflineSystemPrompt + " You can also use code_search to search a static task-start snapshot of the workspace."

const codeSearchRouting = `Use code_search to locate relevant code when the implementation location is unclear. Query with identifiers, error text, paths, or expected behavior.
Use Bash to inspect current files, edit, test, and submit.
code_search uses a static snapshot captured at task start and is not updated after edits.
If the results are weak, refine the query or continue with targeted Bash exploration.`

const codeSearchExample = `Example of a CORRECT code_search response:
<example_response>
I need to locate the Builder validation implementation.

[Makes a code_search tool call: {"query": "Builder validation and construction flow"}]
</example_response>

`

const instancePrompt = `<pr_description>
Consider the following PR description:
{{task}}
</pr_description>

<instructions>
# Task Instructions

## Overview

You're a software engineer interacting continuously with a computer by submitting commands.
You'll be helping implement necessary changes to meet requirements in the PR description.
Your task is specifically to make changes to non-test files in the current directory in order to fix the issue described in the PR description in a way that is general and consistent with the codebase.
<IMPORTANT>This is an interactive process where you will think and issue AT LEAST ONE command, see the result, then think and issue your next command(s).</important>

For each response:

1. Include a THOUGHT section explaining your reasoning and what you're trying to accomplish
2. Provide one or more bash tool calls to execute

## Important Boundaries

- MODIFY: Regular source code files in /testbed (this is the working directory for all your subsequent commands)
- DO NOT MODIFY: Tests, configuration files (pyproject.toml, setup.cfg, etc.)

## Recommended Workflow

1. Analyze the codebase by finding and reading relevant files
2. Create a script to reproduce the issue
3. Edit the source code to resolve the issue
4. Verify your fix works by running your script again
5. Test edge cases to ensure your fix is robust

## Command Execution Rules

You are operating in an environment where

1. You issue at least one command
3. The system executes the command(s) in a subshell
4. You see the result(s)
5. You write your next command(s)

Each response should include:

1. **Reasoning text** where you explain your analysis and plan
2. At least one tool call with your command

**CRITICAL REQUIREMENTS:**

- Your response SHOULD include reasoning text explaining what you're doing
- Your response MUST include AT LEAST ONE bash tool call. You can make MULTIPLE tool calls in a single response when the commands are independent (e.g., searching multiple files, reading different parts of the codebase).
- Directory or environment variable changes are not persistent. Every action is executed in a new subshell.
- However, you can prefix any action with [[BACKTICK]]MY_ENV_VAR=MY_VALUE cd /path/to/working/dir && ...[[BACKTICK]] or write/load environment variables from files

Example of a CORRECT response:
<example_response>
I need to understand the Builder-related code. Let me find relevant files and check the project structure.

[Makes multiple bash tool calls: {"command": "ls -la"}, {"command": "find src -name '*.java' | grep -i builder"}, {"command": "cat README.md | head -50"}]
</example_response>

## Environment Details

- You have a full Linux shell environment
- Always use non-interactive flags (-y, -f) for commands
- Avoid interactive tools like vi, nano, or any that require user input
- You can use bash commands or invoke any tool that is available in the environment
- You can also create new tools or scripts to help you with the task
- If a tool isn't available, you can also install it

## Submission

When you've completed your work, you MUST submit your changes as a git patch.
Follow these steps IN ORDER, with SEPARATE commands:

Step 1: Create the patch file
Run [[BACKTICK]]git diff -- path/to/file1 path/to/file2 > patch.txt[[BACKTICK]] listing only the source files you modified.
Do NOT commit your changes.

<IMPORTANT>
The patch must only contain changes to the specific source files you modified to fix the issue.
Do not submit file creations or changes to any of the following files:

- test and reproduction files
- helper scripts, tests, or tools that you created
- installation, build, packaging, configuration, or setup scripts unless they are directly part of the issue you were fixing (you can assume that the environment is already set up for your client)
- binary or compiled files
</IMPORTANT>

Step 2: Verify your patch
Inspect patch.txt to confirm it only contains your intended changes and headers show [[BACKTICK]]--- a/[[BACKTICK]] and [[BACKTICK]]+++ b/[[BACKTICK]] paths.

Step 3: Submit (EXACT command required)
You MUST use this EXACT command to submit:

[[BACKTICK]][[BACKTICK]][[BACKTICK]]bash
echo COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT && cat patch.txt
[[BACKTICK]][[BACKTICK]][[BACKTICK]]

If the command fails (nonzero exit status), it will not submit.

<CRITICAL>
- Creating/viewing the patch and submitting it MUST be separate commands (not combined with &&).
- If you modify patch.txt after verifying, you SHOULD verify again before submitting.
- You CANNOT continue working (reading, editing, testing) in any way on this task after submitting.
</CRITICAL>
</instructions>`

var codeSearchPromptReplacer = strings.NewReplacer(
	"You're a software engineer interacting continuously with a computer by submitting commands.",
	"You're a software engineer interacting continuously with a computer by using tools.",
	"<IMPORTANT>This is an interactive process where you will think and issue AT LEAST ONE command, see the result, then think and issue your next command(s).</important>",
	"<IMPORTANT>This is an interactive process where you will think and issue AT LEAST ONE tool call, see the result, then think and issue your next tool call(s).</important>",
	"2. Provide one or more bash tool calls to execute",
	"2. Provide one or more tool calls to execute",
	"## Recommended Workflow\n\n",
	"## Recommended Workflow\n\n"+codeSearchRouting+"\n\n",
	"## Command Execution Rules",
	"## Tool Execution Rules",
	"1. You issue at least one command",
	"1. You issue at least one tool call",
	"3. The system executes the command(s) in a subshell",
	"3. The system executes the tool call(s); Bash commands run in a subshell",
	"2. At least one tool call with your command",
	"2. At least one tool call",
	"- Your response MUST include AT LEAST ONE bash tool call. You can make MULTIPLE tool calls in a single response when the commands are independent (e.g., searching multiple files, reading different parts of the codebase).",
	"- Your response MUST include AT LEAST ONE tool call. You can make MULTIPLE tool calls in a single response when the calls are independent (e.g., searching multiple files, reading different parts of the codebase).",
	"- Directory or environment variable changes are not persistent. Every action is executed in a new subshell.",
	"- Directory or environment variable changes are not persistent. Every Bash action is executed in a new subshell.",
	"- However, you can prefix any action with [[BACKTICK]]MY_ENV_VAR=MY_VALUE cd /path/to/working/dir && ...[[BACKTICK]] or write/load environment variables from files",
	"- However, you can prefix any Bash action with [[BACKTICK]]MY_ENV_VAR=MY_VALUE cd /path/to/working/dir && ...[[BACKTICK]] or write/load environment variables from files",
	"Example of a CORRECT response:\n",
	codeSearchExample+"Example of a CORRECT response:\n",
)

var offlinePromptReplacer = strings.NewReplacer(
	"\n- If a tool isn't available, you can also install it\n",
	"\n",
)

// PromptForTask renders the source-aligned instance prompt.
func PromptForTask(task string) string {
	return renderPrompt(instancePrompt, task)
}

// PromptForTaskOffline renders the instance prompt for an isolated generation
// environment while preserving PromptForTask as the upstream-compatible form.
func PromptForTaskOffline(task string) string {
	return renderPrompt(offlinePromptReplacer.Replace(instancePrompt), task)
}

// PromptForTaskWithCodeSearch renders the source-aligned instance prompt with
// the minimum protocol changes needed to make code_search a first-class tool.
func PromptForTaskWithCodeSearch(task string) string {
	return renderPrompt(codeSearchPromptReplacer.Replace(instancePrompt), task)
}

// PromptForTaskWithCodeSearchOffline renders the code-search protocol for an
// isolated generation environment.
func PromptForTaskWithCodeSearchOffline(task string) string {
	prompt := codeSearchPromptReplacer.Replace(instancePrompt)
	return renderPrompt(offlinePromptReplacer.Replace(prompt), task)
}

func renderPrompt(prompt, task string) string {
	return strings.NewReplacer("{{task}}", task, "[[BACKTICK]]", "`").Replace(prompt)
}
