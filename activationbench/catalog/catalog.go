//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

// Package catalog contains the small, local capability catalog used by
// ActivationBench-Lite.  The catalog deliberately separates Skill metadata
// from executable state: a runner can expose summaries first and mount a
// ToolSet only after the corresponding skill_load event.
package catalog

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	trpcskill "trpc.group/trpc-go/trpc-agent-go/skill"
	trpctool "trpc.group/trpc-go/trpc-agent-go/tool"
)

// SkillSpec is the benchmark-facing representation of a Skill.  ToolNames
// identifies the ToolSet that should be activated when this skill is loaded.
type SkillSpec struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Summary      string   `json:"summary"`
	Instructions string   `json:"instructions"`
	ToolSetID    string   `json:"tool_set_id"`
	ToolNames    []string `json:"tool_names"`
	Tags         []string `json:"tags,omitempty"`
}

// ToolSpec is stable metadata for one executable benchmark tool.
// InputSchema and OutputSchema use the same schema type as trpc-agent-go so
// callers can turn a ToolSpec into a model-facing declaration without a
// lossy conversion.
type ToolSpec struct {
	Name         string           `json:"name"`
	Description  string           `json:"description"`
	SkillID      string           `json:"skill_id"`
	ToolSetID    string           `json:"tool_set_id"`
	InputSchema  *trpctool.Schema `json:"input_schema"`
	OutputSchema *trpctool.Schema `json:"output_schema,omitempty"`
	ReadOnly     bool             `json:"read_only"`
	Distractor   bool             `json:"distractor"`
	Tags         []string         `json:"tags,omitempty"`
}

// ToolSetSpec describes the activation unit registered with llmagent.
type ToolSetSpec struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	SkillID   string   `json:"skill_id"`
	ToolNames []string `json:"tool_names"`
}

// Manifest is a serializable snapshot useful for benchmark reports and
// runner diagnostics.
type Manifest struct {
	Skills   []SkillSpec   `json:"skills"`
	ToolSets []ToolSetSpec `json:"tool_sets"`
	Tools    []ToolSpec    `json:"tools"`
}

// Stats summarizes catalog size without requiring callers to inspect the
// manifest.
type Stats struct {
	Skills          int `json:"skills"`
	ToolSets        int `json:"tool_sets"`
	Tools           int `json:"tools"`
	ReadOnlyTools   int `json:"read_only_tools"`
	DistractorTools int `json:"distractor_tools"`
}

// Catalog is an immutable-by-convention capability catalog.  Constructors
// validate all cross references; accessor methods return copies so benchmark
// runs cannot accidentally mutate the shared default catalog.
type Catalog struct {
	skills map[string]SkillSpec
	sets   map[string]ToolSetSpec
	tools  map[string]ToolSpec
}

// New constructs a catalog from explicit specs.  It is useful when a caller
// wants to scale the number of distractor tools while keeping the same local
// environment.
func New(skills []SkillSpec, sets []ToolSetSpec, tools []ToolSpec) (*Catalog, error) {
	c := &Catalog{
		skills: make(map[string]SkillSpec, len(skills)),
		sets:   make(map[string]ToolSetSpec, len(sets)),
		tools:  make(map[string]ToolSpec, len(tools)),
	}
	// SkillSpec currently has one canonical ToolSetID. Keep the catalog
	// one-to-one at this boundary so callers cannot construct a catalog whose
	// extra sets disappear when it is adapted to bench.Suite or resolved by the
	// runner's activation rules.
	toolSetBySkill := make(map[string]string, len(skills))
	// ToolSet IDs are canonical runtime prefixes. Display names are accepted
	// only as compatibility aliases by env.Environment, so both namespaces
	// must be unambiguous at the catalog boundary.
	setAliases := make(map[string]string, len(sets)*2)
	for _, in := range skills {
		s := cloneSkill(in)
		s.ID = strings.TrimSpace(s.ID)
		s.Name = strings.TrimSpace(s.Name)
		s.ToolSetID = strings.TrimSpace(s.ToolSetID)
		if !safeIdentifier(s.ID) || s.Name == "" {
			return nil, errors.New("catalog: skill id and name are required")
		}
		if _, ok := c.skills[s.ID]; ok {
			return nil, fmt.Errorf("catalog: duplicate skill %q", s.ID)
		}
		c.skills[s.ID] = s
	}
	for _, in := range sets {
		ts := cloneToolSet(in)
		ts.ID = strings.TrimSpace(ts.ID)
		ts.Name = strings.TrimSpace(ts.Name)
		ts.SkillID = strings.TrimSpace(ts.SkillID)
		if !safeIdentifier(ts.ID) || ts.Name == "" || !safeIdentifier(ts.SkillID) {
			return nil, errors.New("catalog: tool set id, name, and skill id are required")
		}
		if _, ok := c.sets[ts.ID]; ok {
			return nil, fmt.Errorf("catalog: duplicate tool set %q", ts.ID)
		}
		for _, alias := range []string{ts.ID, ts.Name} {
			if previous, ok := setAliases[alias]; ok && previous != ts.ID {
				return nil, fmt.Errorf("catalog: tool set alias %q is shared by %q and %q", alias, previous, ts.ID)
			}
		}
		setAliases[ts.ID] = ts.ID
		setAliases[ts.Name] = ts.ID
		if _, ok := c.skills[ts.SkillID]; !ok {
			return nil, fmt.Errorf("catalog: tool set %q references unknown skill %q", ts.ID, ts.SkillID)
		}
		if previous, ok := toolSetBySkill[ts.SkillID]; ok {
			return nil, fmt.Errorf(
				"catalog: skill %q has multiple tool sets %q and %q; SkillSpec supports one ToolSetID",
				ts.SkillID, previous, ts.ID,
			)
		}
		toolSetBySkill[ts.SkillID] = ts.ID
		c.sets[ts.ID] = ts
	}
	for _, in := range tools {
		t := cloneTool(in)
		t.Name = strings.TrimSpace(t.Name)
		t.SkillID = strings.TrimSpace(t.SkillID)
		t.ToolSetID = strings.TrimSpace(t.ToolSetID)
		if !safeIdentifier(t.Name) || !safeIdentifier(t.SkillID) || !safeIdentifier(t.ToolSetID) || t.InputSchema == nil {
			return nil, fmt.Errorf("catalog: tool %q needs name, skill id, tool set id, and input schema", t.Name)
		}
		if _, ok := c.tools[t.Name]; ok {
			return nil, fmt.Errorf("catalog: duplicate tool %q", t.Name)
		}
		if _, ok := c.skills[t.SkillID]; !ok {
			return nil, fmt.Errorf("catalog: tool %q references unknown skill %q", t.Name, t.SkillID)
		}
		set, ok := c.sets[t.ToolSetID]
		if !ok {
			return nil, fmt.Errorf("catalog: tool %q references unknown tool set %q", t.Name, t.ToolSetID)
		}
		if set.SkillID != t.SkillID {
			return nil, fmt.Errorf("catalog: tool %q skill/set mismatch", t.Name)
		}
		c.tools[t.Name] = t
	}
	// Every set and skill must agree on membership.  Membership is normalized
	// here so callers can supply either side of the relation.
	for id, set := range c.sets {
		set.ToolNames = uniqueSorted(set.ToolNames)
		for name, tool := range c.tools {
			if tool.ToolSetID == id && !contains(set.ToolNames, name) {
				set.ToolNames = append(set.ToolNames, name)
			}
		}
		set.ToolNames = uniqueSorted(set.ToolNames)
		for _, name := range set.ToolNames {
			tool, ok := c.tools[name]
			if !ok {
				return nil, fmt.Errorf("catalog: tool set %q references unknown tool %q", id, name)
			}
			if tool.ToolSetID != id {
				return nil, fmt.Errorf("catalog: tool %q belongs to %q, not %q", name, tool.ToolSetID, id)
			}
		}
		c.sets[id] = set
	}
	for id, skill := range c.skills {
		if skill.ToolSetID != "" {
			set, ok := c.sets[skill.ToolSetID]
			if !ok {
				return nil, fmt.Errorf("catalog: skill %q references unknown tool set %q", id, skill.ToolSetID)
			}
			if set.SkillID != id {
				return nil, fmt.Errorf("catalog: skill %q/set mismatch", id)
			}
			skill.ToolNames = append([]string(nil), set.ToolNames...)
		} else if toolSetID := toolSetBySkill[id]; toolSetID != "" {
			return nil, fmt.Errorf(
				"catalog: skill %q owns tool set %q but ToolSetID is empty",
				id, toolSetID,
			)
		}
		skill.ToolNames = uniqueSorted(skill.ToolNames)
		c.skills[id] = skill
	}
	// A model-facing qualified name is <ToolSetID>_<raw name>. Reject a raw
	// tool that shadows that spelling; bench.Suite.Validate performs the same
	// check for runner-created suites, and keeping it here protects callers who
	// use Catalog and env directly.
	qualifiedOwners := make(map[string]string, len(c.tools))
	for name, tool := range c.tools {
		qualified := tool.ToolSetID + "_" + name
		if _, exists := c.tools[qualified]; exists && qualified != name {
			return nil, fmt.Errorf("catalog: raw tool %q conflicts with qualified tool %q", qualified, name)
		}
		if previous, exists := qualifiedOwners[qualified]; exists && previous != name {
			return nil, fmt.Errorf("catalog: duplicate qualified tool %q for %q and %q", qualified, previous, name)
		}
		qualifiedOwners[qualified] = name
	}
	return c, nil
}

// Default returns a fresh, deterministic 8-skill/64-tool catalog.  It is
// intentionally local and contains no credentials, network endpoints, or
// filesystem paths.
func Default() *Catalog {
	skills, sets, tools := defaultSpecs()
	c, err := New(skills, sets, tools)
	if err != nil {
		// defaultSpecs is compile-time data.  Keeping this panic here makes a
		// malformed built-in catalog fail immediately instead of producing a
		// partially configured benchmark.
		panic(err)
	}
	return c
}

// Skill returns a copy of a skill specification.
func (c *Catalog) Skill(id string) (SkillSpec, bool) {
	if c == nil {
		return SkillSpec{}, false
	}
	s, ok := c.skills[strings.TrimSpace(id)]
	return cloneSkill(s), ok
}

// ToolSet returns a copy of a tool set specification.
func (c *Catalog) ToolSet(id string) (ToolSetSpec, bool) {
	if c == nil {
		return ToolSetSpec{}, false
	}
	ts, ok := c.sets[strings.TrimSpace(id)]
	return cloneToolSet(ts), ok
}

// Tool returns a copy of a tool specification.
func (c *Catalog) Tool(name string) (ToolSpec, bool) {
	if c == nil {
		return ToolSpec{}, false
	}
	t, ok := c.tools[strings.TrimSpace(name)]
	return cloneTool(t), ok
}

// Skills returns skills sorted by stable ID.
func (c *Catalog) Skills() []SkillSpec {
	if c == nil {
		return nil
	}
	out := make([]SkillSpec, 0, len(c.skills))
	for _, s := range c.skills {
		out = append(out, cloneSkill(s))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// ToolSets returns tool sets sorted by stable ID.
func (c *Catalog) ToolSets() []ToolSetSpec {
	if c == nil {
		return nil
	}
	out := make([]ToolSetSpec, 0, len(c.sets))
	for _, ts := range c.sets {
		out = append(out, cloneToolSet(ts))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Tools returns all tools sorted by stable name.
func (c *Catalog) Tools() []ToolSpec {
	if c == nil {
		return nil
	}
	out := make([]ToolSpec, 0, len(c.tools))
	for _, t := range c.tools {
		out = append(out, cloneTool(t))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ToolsForSkill returns all tools in a Skill's ToolSet, sorted by name.
func (c *Catalog) ToolsForSkill(skillID string) []ToolSpec {
	if c == nil {
		return nil
	}
	skillID = strings.TrimSpace(skillID)
	out := make([]ToolSpec, 0)
	for _, t := range c.tools {
		if t.SkillID == skillID {
			out = append(out, cloneTool(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ToolsForSet returns all tools in a ToolSet, sorted by name.
func (c *Catalog) ToolsForSet(setID string) []ToolSpec {
	if c == nil {
		return nil
	}
	setID = strings.TrimSpace(setID)
	out := make([]ToolSpec, 0)
	for _, t := range c.tools {
		if t.ToolSetID == setID {
			out = append(out, cloneTool(t))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Manifest returns a deep-copied serializable catalog snapshot.
func (c *Catalog) Manifest() Manifest {
	if c == nil {
		return Manifest{}
	}
	return Manifest{Skills: c.Skills(), ToolSets: c.ToolSets(), Tools: c.Tools()}
}

// Stats returns deterministic catalog counts.
func (c *Catalog) Stats() Stats {
	if c == nil {
		return Stats{}
	}
	stats := Stats{Skills: len(c.skills), ToolSets: len(c.sets), Tools: len(c.tools)}
	for _, t := range c.tools {
		if t.ReadOnly {
			stats.ReadOnlyTools++
		}
		if t.Distractor {
			stats.DistractorTools++
		}
	}
	return stats
}

// SkillSummaries converts the catalog to the repository summary type used by
// trpc-agent-go.  The returned order is stable.
func (c *Catalog) SkillSummaries() []trpcskill.Summary {
	if c == nil {
		return nil
	}
	skills := c.Skills()
	out := make([]trpcskill.Summary, 0, len(skills))
	for _, s := range skills {
		out = append(out, trpcskill.Summary{Name: s.ID, Description: s.Summary})
	}
	return out
}

func cloneSkill(in SkillSpec) SkillSpec {
	in.ToolNames = append([]string(nil), in.ToolNames...)
	in.Tags = append([]string(nil), in.Tags...)
	return in
}

func cloneToolSet(in ToolSetSpec) ToolSetSpec {
	in.ToolNames = append([]string(nil), in.ToolNames...)
	return in
}

func cloneTool(in ToolSpec) ToolSpec {
	in.Tags = append([]string(nil), in.Tags...)
	in.InputSchema = cloneSchema(in.InputSchema)
	in.OutputSchema = cloneSchema(in.OutputSchema)
	return in
}

func cloneSchema(in *trpctool.Schema) *trpctool.Schema {
	if in == nil {
		return nil
	}
	out := *in
	if in.Required != nil {
		out.Required = append([]string(nil), in.Required...)
	}
	if in.Enum != nil {
		out.Enum = append([]any(nil), in.Enum...)
	}
	if in.Properties != nil {
		out.Properties = make(map[string]*trpctool.Schema, len(in.Properties))
		for k, v := range in.Properties {
			out.Properties[k] = cloneSchema(v)
		}
	}
	out.Items = cloneSchema(in.Items)
	if in.Defs != nil {
		out.Defs = make(map[string]*trpctool.Schema, len(in.Defs))
		for k, v := range in.Defs {
			out.Defs[k] = cloneSchema(v)
		}
	}
	return &out
}

func uniqueSorted(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	sort.Strings(out)
	return out
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func safeIdentifier(value string) bool {
	value = strings.TrimSpace(value)
	return value != "" && value != "." && value != ".." &&
		filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`) &&
		strings.IndexFunc(value, unicode.IsSpace) < 0
}

type field struct {
	name        string
	typeName    string
	description string
	required    bool
}

func objectSchema(fields ...field) *trpctool.Schema {
	properties := make(map[string]*trpctool.Schema, len(fields))
	required := make([]string, 0)
	for _, f := range fields {
		properties[f.name] = &trpctool.Schema{Type: f.typeName, Description: f.description}
		if f.required {
			required = append(required, f.name)
		}
	}
	return &trpctool.Schema{Type: "object", Properties: properties, Required: required, AdditionalProperties: false}
}

func stringField(name, description string, required bool) field {
	return field{name: name, typeName: "string", description: description, required: required}
}

func intField(name, description string, required bool) field {
	return field{name: name, typeName: "integer", description: description, required: required}
}

func boolField(name, description string, required bool) field {
	return field{name: name, typeName: "boolean", description: description, required: required}
}
