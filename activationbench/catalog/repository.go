//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package catalog

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	trpcskill "trpc.group/trpc-go/trpc-agent-go/skill"
)

// SkillRepository is a small lifecycle facade around a framework
// skill.Repository. Filesystem construction materializes deterministic
// SKILL.md files and then delegates indexing/reading/path semantics to
// trpc-agent-go's FSRepository. Callers can pass it directly to
// llmagent.WithSkills.
type SkillRepository struct {
	skills     []trpcskill.Skill
	root       string
	ownsRoot   bool
	underlying trpcskill.Repository
}

// NewSkillRepository creates a repository and materializes SKILL.md files
// below root.  root must be a caller-owned directory (a testing t.TempDir is
// ideal); no files outside it are touched.
func NewSkillRepository(c *Catalog, root string) (*SkillRepository, error) {
	if c == nil {
		return nil, errors.New("catalog: skill repository requires a catalog")
	}
	return NewSkillRepositoryFromSkills(catalogSkills(c), root)
}

// NewSkillRepositoryFromSkills creates a filesystem-backed repository from
// framework Skill values and materializes one standard SKILL.md per value
// below root. root must be caller-owned (a testing t.TempDir is ideal); no
// files outside it are touched.
func NewSkillRepositoryFromSkills(values []trpcskill.Skill, root string) (*SkillRepository, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("catalog: skill repository root is required")
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, fmt.Errorf("catalog: create skill repository root: %w", err)
	}
	normalized, err := normalizeSkills(values)
	if err != nil {
		return nil, err
	}
	r := &SkillRepository{skills: normalized, root: filepath.Clean(root)}
	if err := r.materialize(); err != nil {
		return nil, err
	}
	underlying, err := trpcskill.NewFSRepository(r.root)
	if err != nil {
		return nil, fmt.Errorf("catalog: index skill repository: %w", err)
	}
	r.underlying = underlying
	return r, nil
}

// OpenSkillRepository opens an existing local Skill root through the
// framework's FSRepository without creating or changing any files. The root
// is caller-owned and remains available after Close.
func OpenSkillRepository(root string) (*SkillRepository, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, errors.New("catalog: skill repository root is required")
	}
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("catalog: inspect skill repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("catalog: skill repository root %q is not a directory", root)
	}
	underlying, err := trpcskill.NewFSRepository(root)
	if err != nil {
		return nil, fmt.Errorf("catalog: index skill repository: %w", err)
	}
	return &SkillRepository{
		root:       filepath.Clean(root),
		underlying: underlying,
	}, nil
}

// NewTempSkillRepository creates a repository in a temporary directory.  The
// caller should call Close when finished; Close removes only this directory.
func NewTempSkillRepository(c *Catalog) (*SkillRepository, error) {
	if c == nil {
		return nil, errors.New("catalog: skill repository requires a catalog")
	}
	return NewTempSkillRepositoryFromSkills(catalogSkills(c))
}

// NewTempSkillRepositoryFromSkills creates a filesystem-backed repository in
// a temporary local directory. The caller should call Close when finished;
// Close removes only this directory.
func NewTempSkillRepositoryFromSkills(values []trpcskill.Skill) (*SkillRepository, error) {
	root, err := os.MkdirTemp("", "activationbench-skills-")
	if err != nil {
		return nil, fmt.Errorf("catalog: create temporary skill root: %w", err)
	}
	r, err := NewSkillRepositoryFromSkills(values, root)
	if err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	r.ownsRoot = true
	return r, nil
}

var _ trpcskill.Repository = (*SkillRepository)(nil)

// Root returns the generated skills root.  It can be passed to
// skill.NewFSRepository when a test specifically needs filesystem semantics.
func (r *SkillRepository) Root() string {
	if r == nil {
		return ""
	}
	return r.root
}

// Close removes a temporary root created by NewTempSkillRepository.  A
// caller-owned root is intentionally left untouched.
func (r *SkillRepository) Close() error {
	if r == nil || !r.ownsRoot || r.root == "" {
		return nil
	}
	err := os.RemoveAll(r.root)
	if err != nil {
		return err
	}
	r.root = ""
	r.underlying = nil
	return nil
}

// Summaries implements skill.Repository.
func (r *SkillRepository) Summaries() []trpcskill.Summary {
	if r == nil || r.underlying == nil {
		return nil
	}
	return r.underlying.Summaries()
}

// Get implements skill.Repository and returns a defensive copy.
func (r *SkillRepository) Get(name string) (*trpcskill.Skill, error) {
	if r == nil || r.underlying == nil {
		return nil, errors.New("catalog: nil skill repository")
	}
	return r.underlying.Get(strings.TrimSpace(name))
}

// Path implements skill.Repository.  The path is stable for the lifetime of
// the repository and points to the generated skill directory.
func (r *SkillRepository) Path(name string) (string, error) {
	if r == nil || r.underlying == nil {
		return "", errors.New("catalog: nil skill repository")
	}
	if r.root == "" {
		return "", fmt.Errorf("skill %q: skill repository has no filesystem root", strings.TrimSpace(name))
	}
	return r.underlying.Path(strings.TrimSpace(name))
}

func catalogSkills(c *Catalog) []trpcskill.Skill {
	if c == nil {
		return nil
	}
	specs := c.Skills()
	values := make([]trpcskill.Skill, 0, len(specs))
	for _, spec := range specs {
		values = append(values, trpcskill.Skill{
			Summary: trpcskill.Summary{Name: spec.ID, Description: spec.Summary},
			Body:    spec.Instructions,
		})
	}
	return values
}

func (r *SkillRepository) materialize() error {
	if r.root == "" {
		return nil
	}
	for _, skill := range r.skills {
		dir := filepath.Join(r.root, skill.Summary.Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("catalog: create skill %q: %w", skill.Summary.Name, err)
		}
		body := "---\n" +
			"name: " + skill.Summary.Name + "\n" +
			"description: " + yamlDescription(skill.Summary.Description) + "\n" +
			"---\n" + skill.Body
		path := filepath.Join(dir, trpcskill.SkillFile)
		if existing, err := os.ReadFile(path); err == nil {
			if string(existing) != body {
				return fmt.Errorf("catalog: refusing to overwrite existing skill file %q", path)
			}
			continue
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("catalog: inspect skill %q: %w", skill.Summary.Name, err)
		}
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			return fmt.Errorf("catalog: write skill %q: %w", skill.Summary.Name, err)
		}
	}
	return nil
}

func normalizeSkills(values []trpcskill.Skill) ([]trpcskill.Skill, error) {
	result := make([]trpcskill.Skill, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name := strings.TrimSpace(value.Summary.Name)
		if !safeIdentifier(name) {
			return nil, fmt.Errorf("catalog: skill name %q is not a safe local identifier", value.Summary.Name)
		}
		if _, exists := seen[name]; exists {
			return nil, fmt.Errorf("catalog: duplicate skill %q", name)
		}
		seen[name] = struct{}{}
		value.Summary.Name = name
		value.Docs = append([]trpcskill.Doc(nil), value.Docs...)
		result = append(result, value)
	}
	return result, nil
}

// yamlDescription uses the subset understood by the framework's SKILL.md
// parser. Plain one-line values preserve punctuation verbatim; block scalars
// keep multi-line summaries unambiguous without relying on quote handling in
// older parser revisions.
func yamlDescription(value string) string {
	if !strings.ContainsAny(value, "\r\n") {
		return value
	}
	value = strings.ReplaceAll(value, "\r\n", "\n")
	value = strings.ReplaceAll(value, "\r", "\n")
	lines := strings.Split(value, "\n")
	var builder strings.Builder
	builder.WriteString("|-\n")
	for _, line := range lines {
		builder.WriteString("  ")
		builder.WriteString(line)
		builder.WriteByte('\n')
	}
	// The caller appends the front-matter newline after this scalar. Returning
	// the indicator plus its indented content keeps the generated file valid
	// even when a summary contains punctuation such as ':' or '#'.
	return strings.TrimSuffix(builder.String(), "\n")
}
