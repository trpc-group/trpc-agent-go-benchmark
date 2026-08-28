//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package activationbench contains the local, deterministic ActivationBench-
// Lite benchmark data model.  The benchmark deliberately keeps tools and
// state in process so an experiment does not need containers or external
// accounts.  The runner package wires this model to an llmagent and compares
// static and skill-triggered tool activation.
package activationbench

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"trpc.group/trpc-go/trpc-agent-go/tool"
)

// Mode identifies the tool-surface policy used for a run.
type Mode string

const (
	// ModeStaticAll exposes every registered tool from the first model request.
	ModeStaticAll Mode = "static-all"
	// ModeDynamicActivation initially exposes only framework/skill tools and
	// activates mapped ToolSets after skill_load.
	ModeDynamicActivation Mode = "dynamic-activation"
)

// Valid reports whether m is a supported benchmark mode.
func (m Mode) Valid() bool {
	return m == ModeStaticAll || m == ModeDynamicActivation
}

// ToolHandler executes a benchmark tool.  The handler receives the raw JSON
// arguments and the task-local mutable state.  Handlers must not access
// external services; keeping them deterministic is a property of Lite.
type ToolHandler func(context.Context, []byte, *TaskState) (any, error)

// ToolSpec describes one benchmark tool before it is wrapped in a ToolSet.
// The framework exposes a ToolSet member as "<tool-set>_<name>". Lite keeps
// the unqualified names unique across a Suite as well, so Task.RequiredTools
// remains unambiguous; callers that need duplicate provider names can use
// distinct local aliases and retain the qualified name in diagnostics.
type ToolSpec struct {
	Name         string       `json:"name"`
	Description  string       `json:"description,omitempty"`
	Skill        string       `json:"skill,omitempty"`
	ToolSet      string       `json:"tool_set,omitempty"`
	ReadOnly     bool         `json:"read_only,omitempty"`
	Distractor   bool         `json:"distractor,omitempty"`
	InputSchema  *tool.Schema `json:"input_schema,omitempty"`
	OutputSchema *tool.Schema `json:"output_schema,omitempty"`
	// Handler is required for a tool that a runner may execute. A missing
	// handler is treated as a failed call rather than an implicit no-op.
	Handler ToolHandler `json:"-"`
}

// SkillSpec describes a compact Skill summary and its on-demand body.
type SkillSpec struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Body        string   `json:"body,omitempty"`
	ToolSets    []string `json:"tool_sets,omitempty"`
}

// CallRecord records one executed benchmark tool call.
type CallRecord struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments,omitempty"`
	Succeeded bool   `json:"succeeded"`
	Error     string `json:"error,omitempty"`
}

// TaskState is the isolated mutable state for one task run.  A new state is
// created for every mode/task pair, preventing one arm from affecting the
// other.  Handlers may use Values for domain state and Calls for diagnostics.
type TaskState struct {
	mu     sync.RWMutex
	Values map[string]any `json:"values,omitempty"`
	Calls  []CallRecord   `json:"calls,omitempty"`
}

// NewTaskState creates a task state with a deep, type-preserving copy of
// initial values. This prevents nested maps, slices, pointers, and fixture
// structs from leaking mutations between static/dynamic arms or repetitions.
// Values with inherently shared semantics (for example, functions and
// channels) are retained as-is by the cloning helper.
func NewTaskState(initial map[string]any) *TaskState {
	values := cloneInitialValues(initial)
	return &TaskState{Values: values}
}

// RecordCall appends a copy of a tool call to the state.
func (s *TaskState) RecordCall(record CallRecord) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.Calls = append(s.Calls, record)
	s.mu.Unlock()
}

// SnapshotCalls returns a copy of the calls observed so far.
func (s *TaskState) SnapshotCalls() []CallRecord {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	return append([]CallRecord(nil), s.Calls...)
}

// Get returns a state value while holding the state read lock.
func (s *TaskState) Get(key string) (any, bool) {
	if s == nil {
		return nil, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	value, ok := s.Values[key]
	return value, ok
}

// Set updates a state value while holding the state write lock.
func (s *TaskState) Set(key string, value any) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if s.Values == nil {
		s.Values = make(map[string]any)
	}
	s.Values[key] = value
	s.mu.Unlock()
}

// Evaluation is the outcome of a task evaluator.
type Evaluation struct {
	Passed          bool    `json:"passed"`
	Score           float64 `json:"score"`
	Message         string  `json:"message,omitempty"`
	RequiredCount   int     `json:"required_count,omitempty"`
	SatisfiedCount  int     `json:"satisfied_count,omitempty"`
	CollateralCount int     `json:"collateral_count,omitempty"`
}

// Evaluator checks final task state and returns a quality result.  Returning a
// zero Evaluation is valid; the runner will fill in a default call-based
// evaluator when Evaluator is nil.
type Evaluator func(*TaskState) Evaluation

// Task is a deterministic benchmark case.  RequiredTools are unqualified
// names from ToolSpec.Name; the runner also accepts namespaced names for
// callers that construct tasks from an existing Toolathlon-style catalog.
type Task struct {
	ID             string         `json:"id"`
	Prompt         string         `json:"prompt"`
	RequiredSkills []string       `json:"required_skills,omitempty"`
	RequiredTools  []string       `json:"required_tools,omitempty"`
	SessionID      string         `json:"session_id,omitempty"`
	InitialState   map[string]any `json:"initial_state,omitempty"`
	Evaluate       Evaluator      `json:"-"`
}

// Suite is a self-contained set of Skills, tools, and tasks.
type Suite struct {
	Name   string      `json:"name"`
	Skills []SkillSpec `json:"skills"`
	Tools  []ToolSpec  `json:"tools"`
	Tasks  []Task      `json:"tasks"`
}

// ToolSetName returns the canonical ToolSet for a skill or tool.
func ToolSetName(skillName string) string {
	name := strings.TrimSpace(skillName)
	if name == "" {
		return ""
	}
	return name + "-tools"
}

// QualifiedToolName returns the name visible to a model when spec is in its
// ToolSet.  If spec.ToolSet is empty, its Skill determines the set.  A tool
// without a Skill is returned unchanged because it is registered as a base
// tool.
func QualifiedToolName(spec ToolSpec) string {
	setName := strings.TrimSpace(spec.ToolSet)
	if setName == "" {
		setName = ToolSetName(spec.Skill)
	}
	name := strings.TrimSpace(spec.Name)
	if setName == "" {
		return name
	}
	if name == "" {
		return setName
	}
	return setName + "_" + name
}

const toolReferencePrefix = "{{tool:"

// RenderModelFacingText expands explicit benchmark tool references to the
// names that the framework exposes to the model.  Catalog authors write
// {{tool:raw_name}} so the same text remains readable and independent of a
// particular ToolSet spelling; the runner resolves it from ToolSpec metadata
// before constructing the agent.  Bare tool names are deliberately not
// rewritten, which avoids changing ordinary prose or accidentally rewriting a
// name that is not a tool reference.
func RenderModelFacingText(text string, specs []ToolSpec) (string, error) {
	if !strings.Contains(text, toolReferencePrefix) {
		return text, nil
	}
	qualifiedByRaw := make(map[string]string, len(specs))
	// Framework-managed tools are already model-facing names and are not part
	// of the benchmark ToolSpec catalog. Allowing them as explicit references
	// keeps the renderer useful for Skill text that documents skill_load or
	// another opt-in framework operation.
	for name := range frameworkToolNames {
		qualifiedByRaw[name] = name
	}
	for _, spec := range specs {
		raw := strings.TrimSpace(spec.Name)
		if raw == "" {
			return "", fmt.Errorf("cannot render tool references: tool name must not be empty")
		}
		qualified := strings.TrimSpace(QualifiedToolName(spec))
		if qualified == "" {
			return "", fmt.Errorf("cannot render tool %q: qualified name is empty", raw)
		}
		if _, exists := qualifiedByRaw[raw]; exists {
			return "", fmt.Errorf("cannot render tool references: duplicate tool %q", raw)
		}
		qualifiedByRaw[raw] = qualified
	}

	var rendered strings.Builder
	rendered.Grow(len(text))
	for offset := 0; offset < len(text); {
		relativeStart := strings.Index(text[offset:], toolReferencePrefix)
		if relativeStart < 0 {
			rendered.WriteString(text[offset:])
			break
		}
		start := offset + relativeStart
		rendered.WriteString(text[offset:start])
		contentStart := start + len(toolReferencePrefix)
		relativeEnd := strings.Index(text[contentStart:], "}}")
		if relativeEnd < 0 {
			return "", fmt.Errorf("unterminated tool reference at byte %d", start)
		}
		end := contentStart + relativeEnd
		raw := strings.TrimSpace(text[contentStart:end])
		if raw == "" {
			return "", fmt.Errorf("empty tool reference at byte %d", start)
		}
		qualified, ok := qualifiedByRaw[raw]
		if !ok {
			return "", fmt.Errorf("unknown tool reference %q", raw)
		}
		rendered.WriteString(qualified)
		offset = end + len("}}")
	}
	return rendered.String(), nil
}

// RenderModelFacingSuite returns a copy of s whose model-facing prose uses
// framework-visible qualified tool names.  This includes Skill summaries and
// bodies, Tool descriptions, and task prompts. Raw ToolSpec names, task plans,
// and evaluator metadata are preserved, so benchmark logic can continue to
// use canonical unqualified names while only model-facing text is rendered.
func RenderModelFacingSuite(s Suite) (Suite, error) {
	rendered := s
	rendered.Skills = append([]SkillSpec(nil), s.Skills...)
	rendered.Tools = append([]ToolSpec(nil), s.Tools...)
	rendered.Tasks = append([]Task(nil), s.Tasks...)
	for index := range rendered.Skills {
		var err error
		rendered.Skills[index].Description, err = RenderModelFacingText(s.Skills[index].Description, s.Tools)
		if err != nil {
			return Suite{}, fmt.Errorf("render skill %q description: %w", s.Skills[index].Name, err)
		}
		rendered.Skills[index].Body, err = RenderModelFacingText(s.Skills[index].Body, s.Tools)
		if err != nil {
			return Suite{}, fmt.Errorf("render skill %q body: %w", s.Skills[index].Name, err)
		}
		rendered.Skills[index].ToolSets = append([]string(nil), s.Skills[index].ToolSets...)
	}
	schemaCopies := make(map[*tool.Schema]*tool.Schema)
	for index := range rendered.Tools {
		var err error
		rendered.Tools[index].Description, err = RenderModelFacingText(s.Tools[index].Description, s.Tools)
		if err != nil {
			return Suite{}, fmt.Errorf("render tool %q description: %w", s.Tools[index].Name, err)
		}
		rendered.Tools[index].InputSchema, err = renderModelFacingSchema(s.Tools[index].InputSchema, s.Tools, schemaCopies)
		if err != nil {
			return Suite{}, fmt.Errorf("render tool %q input schema: %w", s.Tools[index].Name, err)
		}
		rendered.Tools[index].OutputSchema, err = renderModelFacingSchema(s.Tools[index].OutputSchema, s.Tools, schemaCopies)
		if err != nil {
			return Suite{}, fmt.Errorf("render tool %q output schema: %w", s.Tools[index].Name, err)
		}
	}
	for index := range rendered.Tasks {
		var err error
		rendered.Tasks[index].Prompt, err = RenderModelFacingText(s.Tasks[index].Prompt, s.Tools)
		if err != nil {
			return Suite{}, fmt.Errorf("render task %q prompt: %w", s.Tasks[index].ID, err)
		}
	}
	return rendered, nil
}

func renderModelFacingSchema(in *tool.Schema, specs []ToolSpec, copies map[*tool.Schema]*tool.Schema) (*tool.Schema, error) {
	if in == nil {
		return nil, nil
	}
	if existing, ok := copies[in]; ok {
		return existing, nil
	}
	out := *in
	copies[in] = &out
	var err error
	out.Description, err = RenderModelFacingText(in.Description, specs)
	if err != nil {
		return nil, err
	}
	if in.Required != nil {
		out.Required = append([]string(nil), in.Required...)
	}
	if in.Enum != nil {
		out.Enum = append([]any(nil), in.Enum...)
	}
	if in.Properties != nil {
		out.Properties = make(map[string]*tool.Schema, len(in.Properties))
		for name, property := range in.Properties {
			out.Properties[name], err = renderModelFacingSchema(property, specs, copies)
			if err != nil {
				return nil, err
			}
		}
	}
	if in.Items != nil {
		out.Items, err = renderModelFacingSchema(in.Items, specs, copies)
		if err != nil {
			return nil, err
		}
	}
	if in.Defs != nil {
		out.Defs = make(map[string]*tool.Schema, len(in.Defs))
		for name, definition := range in.Defs {
			out.Defs[name], err = renderModelFacingSchema(definition, specs, copies)
			if err != nil {
				return nil, err
			}
		}
	}
	if additional, ok := in.AdditionalProperties.(*tool.Schema); ok {
		out.AdditionalProperties, err = renderModelFacingSchema(additional, specs, copies)
		if err != nil {
			return nil, err
		}
	}
	return &out, nil
}

// Validate checks the structural invariants required by the runner.
func (s Suite) Validate() error {
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("suite name must not be empty")
	}
	skillNames := make(map[string]struct{}, len(s.Skills))
	for _, skill := range s.Skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" {
			return fmt.Errorf("skill name must not be empty")
		}
		if _, exists := skillNames[name]; exists {
			return fmt.Errorf("duplicate skill %q", name)
		}
		skillNames[name] = struct{}{}
	}
	toolNames := make(map[string]struct{}, len(s.Tools))
	toolOwners := make(map[string]int, len(s.Tools))
	qualifiedToolNames := make(map[string]struct{}, len(s.Tools))
	qualifiedToolOwners := make(map[string]int, len(s.Tools))
	toolSetNames := make(map[string]struct{})
	toolSetSkills := make(map[string]string)
	// Keep the reverse mapping while validating so we can prove that every
	// model-facing spelling resolves to one ToolSpec. A raw tool name that is
	// equal to another spec's qualified name is otherwise ambiguous.
	skillToolSets := make(map[string]map[string]struct{}, len(skillNames))
	for skillName := range skillNames {
		skillToolSets[skillName] = make(map[string]struct{})
	}
	for index, spec := range s.Tools {
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			return fmt.Errorf("tool name must not be empty")
		}
		// callableTool exposes spec.Name verbatim in its Declaration, while
		// qualified names and task lookups use the canonical trimmed spelling.
		// Rejecting surrounding whitespace here keeps the model-visible and
		// audit-visible names identical without changing the public ToolSpec API.
		if spec.Name != name {
			return fmt.Errorf("tool name %q must not have surrounding whitespace", spec.Name)
		}
		if IsFrameworkToolName(name) {
			return fmt.Errorf("tool %q conflicts with a framework-provided tool", name)
		}
		if _, exists := toolNames[name]; exists {
			return fmt.Errorf("duplicate tool %q", name)
		}
		toolNames[name] = struct{}{}
		toolOwners[name] = index
		qualified := strings.TrimSpace(QualifiedToolName(spec))
		if qualified == "" {
			return fmt.Errorf("tool %q has an empty qualified name", name)
		}
		// ToolSet declarations are exposed to the model under their qualified
		// name.  Check that spelling as well as the raw alias: a fixture named
		// "load" in a ToolSet named "skill" would otherwise shadow the
		// framework's built-in "skill_load" declaration.
		if IsFrameworkToolName(qualified) {
			return fmt.Errorf("qualified tool %q conflicts with a framework-provided tool", qualified)
		}
		if _, exists := qualifiedToolNames[qualified]; exists {
			return fmt.Errorf("duplicate qualified tool %q", qualified)
		}
		qualifiedToolNames[qualified] = struct{}{}
		qualifiedToolOwners[qualified] = index

		skillName := strings.TrimSpace(spec.Skill)
		if skillName == "" {
			// QualifiedToolName intentionally honors ToolSet even when Skill is
			// empty, but buildToolSets registers such a spec as a base tool.  A
			// non-empty ToolSet would therefore make the declaration and audit
			// names disagree and can never be activated by a Skill load.
			if strings.TrimSpace(spec.ToolSet) != "" {
				return fmt.Errorf("base tool %q must not specify tool set %q", name, spec.ToolSet)
			}
			if spec.Skill != "" {
				return fmt.Errorf("tool %q references unknown skill %q", name, spec.Skill)
			}
			continue
		}
		if _, exists := skillNames[skillName]; !exists {
			return fmt.Errorf("tool %q references unknown skill %q", name, spec.Skill)
		}
		toolSet := strings.TrimSpace(spec.ToolSet)
		if toolSet == "" {
			toolSet = ToolSetName(skillName)
		}
		if toolSet != "" {
			toolSetNames[toolSet] = struct{}{}
			skillToolSets[skillName][toolSet] = struct{}{}
			if owner, exists := toolSetSkills[toolSet]; exists && owner != skillName {
				return fmt.Errorf("tool set %q is shared by skills %q and %q", toolSet, owner, skillName)
			}
			toolSetSkills[toolSet] = skillName
		}
	}
	for name, owner := range toolOwners {
		if qualifiedOwner, exists := qualifiedToolOwners[name]; exists && qualifiedOwner != owner {
			return fmt.Errorf(
				"tool name %q is ambiguous: it is the unqualified name of tool %q and the qualified name of tool %q",
				name,
				strings.TrimSpace(s.Tools[owner].Name),
				strings.TrimSpace(s.Tools[qualifiedOwner].Name),
			)
		}
	}
	for _, skill := range s.Skills {
		skillName := strings.TrimSpace(skill.Name)
		declaredToolSets := make(map[string]struct{}, len(skill.ToolSets))
		for _, toolSet := range skill.ToolSets {
			toolSet = strings.TrimSpace(toolSet)
			if toolSet == "" {
				return fmt.Errorf("skill %q has an empty tool set", skill.Name)
			}
			if _, exists := toolSetNames[toolSet]; !exists {
				return fmt.Errorf("skill %q references unknown tool set %q", skill.Name, toolSet)
			}
			if owner := toolSetSkills[toolSet]; owner != "" && owner != strings.TrimSpace(skill.Name) {
				return fmt.Errorf("skill %q references tool set %q owned by skill %q", skill.Name, toolSet, owner)
			}
			declaredToolSets[toolSet] = struct{}{}
		}
		actualToolSets := skillToolSets[skillName]
		if len(actualToolSets) > 1 && len(declaredToolSets) == 0 {
			return fmt.Errorf("skill %q has multiple tool sets but declares no ToolSets mapping", skill.Name)
		}
		if len(declaredToolSets) > 0 {
			for toolSet := range actualToolSets {
				if _, mapped := declaredToolSets[toolSet]; !mapped {
					return fmt.Errorf("skill %q does not map owned tool set %q", skill.Name, toolSet)
				}
			}
		}
	}
	taskIDs := make(map[string]struct{}, len(s.Tasks))
	for _, task := range s.Tasks {
		id := strings.TrimSpace(task.ID)
		if id == "" {
			return fmt.Errorf("task id must not be empty")
		}
		if _, exists := taskIDs[id]; exists {
			return fmt.Errorf("duplicate task %q", id)
		}
		taskIDs[id] = struct{}{}
		requiredSkillNames := make(map[string]struct{}, len(task.RequiredSkills))
		for _, name := range task.RequiredSkills {
			name = strings.TrimSpace(name)
			if name == "" {
				return fmt.Errorf("task %q has an empty required skill", id)
			}
			if _, duplicate := requiredSkillNames[name]; duplicate {
				return fmt.Errorf("task %q lists required skill %q more than once", id, name)
			}
			requiredSkillNames[name] = struct{}{}
			if _, exists := skillNames[name]; !exists {
				return fmt.Errorf("task %q references unknown skill %q", id, name)
			}
		}
		requiredToolNames := make(map[string]struct{}, len(task.RequiredTools))
		for _, name := range task.RequiredTools {
			base := strings.TrimSpace(name)
			if base == "" {
				return fmt.Errorf("task %q has an empty required tool", id)
			}
			if _, exists := toolNames[base]; !exists {
				foundQualified := false
				for _, spec := range s.Tools {
					if QualifiedToolName(spec) == base {
						foundQualified = true
						break
					}
				}
				if !foundQualified {
					return fmt.Errorf("task %q references unknown tool %q", id, name)
				}
			}
			owner, found := toolOwners[base]
			if !found {
				owner, found = qualifiedToolOwners[base]
			}
			// Treat raw and qualified spellings of the same ToolSpec as one
			// requirement. The evaluator is presence-based, so allowing either
			// spelling twice would make RequiredCount/Score lie about coverage.
			requirementKey := "name:" + base
			if found {
				requirementKey = fmt.Sprintf("tool:%d", owner)
			}
			if _, duplicate := requiredToolNames[requirementKey]; duplicate {
				return fmt.Errorf("task %q lists required tool %q more than once", id, name)
			}
			requiredToolNames[requirementKey] = struct{}{}
			if found {
				ownerSkill := strings.TrimSpace(s.Tools[owner].Skill)
				if ownerSkill != "" {
					if _, declared := requiredSkillNames[ownerSkill]; !declared {
						return fmt.Errorf("task %q requires tool %q from skill %q but does not declare that skill", id, base, ownerSkill)
					}
				}
			}
		}
	}
	return nil
}

// DefaultEvaluation checks that every required tool was successfully called
// and computes a partial score for tasks that made progress.  It recognizes
// the historical <skill>-tools_<tool> spelling, but has no catalog with which
// to disambiguate arbitrary ToolSet names.  Call DefaultEvaluationWithSpecs
// when a suite may use custom ToolSet names (for example, "research_ops").
func DefaultEvaluation(task Task, state *TaskState) Evaluation {
	return defaultEvaluation(task, state, nil)
}

// DefaultEvaluationWithSpecs is the catalog-aware form of DefaultEvaluation.
// It resolves unqualified required names through the supplied ToolSpec
// metadata, so a ToolSet-qualified execution name such as
// "research_ops_search" satisfies a required tool named "search".  Keeping
// this as a separate API preserves the original two-argument helper while
// avoiding unsafe broad suffix matching for callers that do not provide a
// catalog.
func DefaultEvaluationWithSpecs(
	task Task,
	state *TaskState,
	specs []ToolSpec,
) Evaluation {
	return defaultEvaluation(task, state, specs)
}

func defaultEvaluation(task Task, state *TaskState, specs []ToolSpec) Evaluation {
	aliases := ToolNameAliases(specs)
	required := make([]string, 0, len(task.RequiredTools))
	for _, name := range task.RequiredTools {
		required = append(required, canonicalEvaluationName(name, aliases))
	}
	seen := make(map[string]bool, len(required))
	for _, call := range state.SnapshotCalls() {
		if !call.Succeeded {
			continue
		}
		actual := canonicalEvaluationName(call.Name, aliases)
		seen[actual] = true
		for index, target := range task.RequiredTools {
			if equivalentToolNameWithEvaluationAliases(call.Name, target, aliases) {
				seen[required[index]] = true
			}
		}
	}
	satisfied := 0
	for _, target := range required {
		if seen[target] {
			satisfied++
		}
	}
	score := 1.0
	if len(required) > 0 {
		score = float64(satisfied) / float64(len(required))
	}
	passed := satisfied == len(required)
	return Evaluation{
		Passed:         passed,
		Score:          score,
		RequiredCount:  len(required),
		SatisfiedCount: satisfied,
	}
}

// ToolNameAliases maps both qualified and (when unambiguous) unqualified
// ToolSpec names to one canonical qualified name. Keeping this in the root
// benchmark package gives the evaluator, runner, and activation audit one
// spelling policy instead of three subtly different suffix rules.
//
// The returned map is newly allocated and may be modified by the caller.
func ToolNameAliases(specs []ToolSpec) map[string]string {
	if len(specs) == 0 {
		return nil
	}
	aliases := make(map[string]string, len(specs)*2)
	counts := make(map[string]int, len(specs))
	qualifiedByName := make(map[string]string, len(specs))
	for _, spec := range specs {
		qualified := strings.TrimSpace(QualifiedToolName(spec))
		if qualified == "" {
			continue
		}
		aliases[qualified] = qualified
		name := strings.TrimSpace(spec.Name)
		if name == "" {
			continue
		}
		counts[name]++
		qualifiedByName[name] = qualified
	}
	for name, qualified := range qualifiedByName {
		// Keep a qualified alias authoritative. A base tool called "x" can
		// otherwise overwrite the qualified spelling of another spec whose
		// ToolSet-qualified name happens to be "x".
		if counts[name] == 1 {
			if _, qualifiedCollision := aliases[name]; qualifiedCollision {
				continue
			}
			aliases[name] = qualified
		}
	}
	return aliases
}

func canonicalEvaluationName(name string, aliases map[string]string) string {
	name = strings.TrimSpace(name)
	if canonical, ok := aliases[name]; ok {
		return canonical
	}
	return name
}

func equivalentToolNameWithEvaluationAliases(
	actual, target string,
	aliases map[string]string,
) bool {
	actual = strings.TrimSpace(actual)
	target = strings.TrimSpace(target)
	if actual == target {
		return true
	}
	if len(aliases) > 0 {
		canonicalActual, actualKnown := aliases[actual]
		canonicalTarget, targetKnown := aliases[target]
		if actualKnown && targetKnown {
			return canonicalActual == canonicalTarget
		}
	}
	// Preserve the conservative legacy spelling check when no catalog alias is
	// available.  In particular, do not accept arbitrary suffix lookalikes.
	return equivalentToolName(actual, target)
}

// equivalentToolName accepts an exact tool name or the conventional
// ToolSet-qualified spelling (<name>-tools_<tool>). Keeping the prefix check
// strict prevents a model call such as evil_mail_search from satisfying a
// required mail_search operation.
func equivalentToolName(actual, target string) bool {
	actual = strings.TrimSpace(actual)
	target = strings.TrimSpace(target)
	if actual == target {
		return true
	}
	return ConventionalToolSetAlias(actual, target)
}
