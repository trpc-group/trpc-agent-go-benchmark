//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License 2.0.
//

package activationbench

import (
	"strings"

	"trpc.group/trpc-go/trpc-agent-go/agent/llmagent"
)

// frameworkToolNames contains names reserved by the framework Skill and
// workspace surfaces. Some entries are only installed for an opt-in profile
// or executor, but reserving the complete known set keeps a custom fixture
// safe if its runner options change later. The legacy skill_list spelling is
// retained for compatibility with older framework revisions.
var frameworkToolNames = map[string]struct{}{
	"skill_list":                          {},
	string(llmagent.SkillToolLoad):        {},
	string(llmagent.SkillToolListDocs):    {},
	string(llmagent.SkillToolSelectDocs):  {},
	string(llmagent.SkillToolRun):         {},
	string(llmagent.SkillToolExec):        {},
	string(llmagent.SkillToolWriteStdin):  {},
	string(llmagent.SkillToolPollSession): {},
	string(llmagent.SkillToolKillSession): {},
	"workspace_exec":                      {},
	"workspace_save_artifact":             {},
	"workspace_write_stdin":               {},
	"workspace_kill_session":              {},
}

// IsFrameworkToolName reports whether name is one of the framework-managed
// Skill/workspace names reserved by Lite. Surrounding whitespace is ignored,
// but the normalized name is still matched exactly: a model-returned name
// such as evil_skill_load is an invalid fixture call, not a framework call.
func IsFrameworkToolName(name string) bool {
	_, ok := frameworkToolNames[strings.TrimSpace(name)]
	return ok
}

// ConventionalToolSetAlias reports whether qualified is the framework's
// conventional ToolSet spelling for unqualified: <set>-tools_<tool>.
// Custom ToolSet names are resolved from ToolSpec metadata by the runner and
// do not use this fallback.
func ConventionalToolSetAlias(qualified, unqualified string) bool {
	qualified = strings.TrimSpace(qualified)
	unqualified = strings.TrimSpace(unqualified)
	if qualified == "" || unqualified == "" {
		return false
	}
	suffix := "_" + unqualified
	if !strings.HasSuffix(qualified, suffix) {
		return false
	}
	prefix := strings.TrimSuffix(qualified, suffix)
	return strings.HasSuffix(prefix, "-tools")
}
