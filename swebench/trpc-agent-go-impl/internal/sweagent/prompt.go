//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweagent

import (
	"fmt"
	"strings"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/internal/contract"
)

// SystemPrompt matches the deliberately small mini-swe-agent system role.
const SystemPrompt = "You are a helpful assistant that can interact with a computer shell to solve programming tasks."

const instancePrompt = `Your task is to write a patch that resolves the following issue in the repository.

<issue>
%s
</issue>

You are in the root directory of the repository. Follow these rules:

1. Inspect the relevant code and tests before editing.
2. Make the smallest complete source-code change that fixes the issue.
3. Run focused tests or other checks when practical.
4. Do not modify tests, build artifacts, generated files, or repository metadata unless the issue explicitly requires it.
5. Do not use interactive commands, editors, or commands that wait for user input.
6. Use the bash tool for every repository operation. Each call runs independently, so include any required working-directory changes in the command.
7. When the solution is complete, submit it by calling bash with a command whose first line is exactly:
COMPLETE_TASK_AND_SUBMIT_FINAL_OUTPUT
Do not place that marker in prose or combine it with shell commands. Any following lines are treated as a short final summary.

The evaluator will collect the final patch from git diff. Do not merely describe a patch; edit the repository.`

// PromptForCase renders the safe benchmark fields visible to the agent.
func PromptForCase(c contract.Case) string {
	issue := strings.TrimSpace(c.ProblemStatement)
	if hints := strings.TrimSpace(c.HintsText); hints != "" {
		issue += "\n\nHints:\n" + hints
	}
	return fmt.Sprintf(instancePrompt, issue)
}
