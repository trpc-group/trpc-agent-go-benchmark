//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package protocol implements the mini-SWE-agent v2.1 model-facing protocol
// used by the tRPC-Agent-Go runner.
package protocol

import "strings"

// UpstreamCommit pins the mini-SWE-agent source used for the protocol.
const UpstreamCommit = "3a9b8e874d322a9cfb1f391ff4f4df67721c108c"

// SystemPrompt is rendered from v2.1.0 config/benchmarks/swebench.yaml.
const SystemPrompt = "You are a helpful assistant that can interact with a computer shell to solve programming tasks."

const offlineGuidance = "The shell has no public internet access; only declared local services are reachable. " +
	"Use the PR description, repository and base-or-earlier local history, local tests, and locally available tools and dependencies. " +
	"If an optional dependency is absent, continue with the available evidence."

// OfflineSystemPrompt adds the minimum accurate capability notice for a
// clean-room generation container.
const OfflineSystemPrompt = SystemPrompt + " " + offlineGuidance

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

// PromptForTask renders the source-aligned instance prompt.
func PromptForTask(task string) string {
	return renderPrompt(instancePrompt, task)
}

var offlinePromptReplacer = strings.NewReplacer(
	"\n- If a tool isn't available, you can also install it\n",
	"\n",
)

// PromptForTaskOffline removes the upstream installation suggestion that is
// impossible inside a network-none generation container.
func PromptForTaskOffline(task string) string {
	return renderPrompt(offlinePromptReplacer.Replace(instancePrompt), task)
}

// PromptForTaskForMode selects the source-aligned or clean-room prompt.
func PromptForTaskForMode(task string, cleanRoom bool) string {
	if cleanRoom {
		return PromptForTaskOffline(task)
	}
	return PromptForTask(task)
}

func renderPrompt(prompt, task string) string {
	return strings.NewReplacer("{{task}}", task, "[[BACKTICK]]", "`").Replace(prompt)
}
