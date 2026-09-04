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
	"fmt"
	"sort"
)

// generatedSkillTemplates supplies ordinary, production-shaped capability
// names for menu-size experiments.  The benchmark metadata still marks these
// entries as distractors, but that label must never leak into the model-facing
// Skill or Tool text: a real catalog would describe what a capability does,
// not explain whether the current task needs it.
var generatedSkillTemplates = []struct {
	ID           string
	Name         string
	Summary      string
	Instructions string
	ToolName     string
	ToolDesc     string
}{
	{ID: "travel", Name: "Travel planning", Summary: "Search itineraries, reservations, and traveler preferences.", Instructions: "Search for matching travel records before requesting details.", ToolName: "search_itineraries", ToolDesc: "Search travel itineraries by destination or date."},
	{ID: "billing", Name: "Billing operations", Summary: "Review invoices, payment status, and account balances.", Instructions: "Use the account or invoice reference returned by a search when requesting details.", ToolName: "search_invoices", ToolDesc: "Search invoices by account, status, or date."},
	{ID: "projects", Name: "Project coordination", Summary: "Inspect project milestones, owners, and delivery status.", Instructions: "Locate the project before reading its milestones or assignments.", ToolName: "search_projects", ToolDesc: "Search projects by name, owner, or status."},
	{ID: "support", Name: "Support operations", Summary: "Search customer cases, conversations, and resolution history.", Instructions: "Find the relevant case before reading its conversation history.", ToolName: "search_cases", ToolDesc: "Search support cases by customer, subject, or status."},
	{ID: "people", Name: "People operations", Summary: "Look up employee profiles, teams, and reporting relationships.", Instructions: "Find the employee record before requesting profile details.", ToolName: "search_employees", ToolDesc: "Search employee records by name, team, or email."},
	{ID: "procurement", Name: "Procurement operations", Summary: "Review purchase requests, suppliers, and approval status.", Instructions: "Search purchase requests before inspecting approval details.", ToolName: "search_purchase_requests", ToolDesc: "Search purchase requests by requester, supplier, or status."},
	{ID: "shipping", Name: "Shipping operations", Summary: "Track shipments, delivery estimates, and carrier events.", Instructions: "Search for a shipment before requesting its tracking events.", ToolName: "search_shipments", ToolDesc: "Search shipments by order, carrier, or tracking reference."},
	{ID: "analytics", Name: "Analytics operations", Summary: "Inspect saved reports, metrics, and dashboard definitions.", Instructions: "Find the report or metric before reading its definition.", ToolName: "search_reports", ToolDesc: "Search saved reports by title, owner, or metric."},
	{ID: "monitoring", Name: "Monitoring operations", Summary: "Review service health, alerts, and recent observations.", Instructions: "Find the service before requesting its health observations.", ToolName: "search_services", ToolDesc: "Search monitored services by name or environment."},
	{ID: "incidents", Name: "Incident management", Summary: "Inspect incidents, responders, and resolution timelines.", Instructions: "Locate an incident before reading its timeline or responders.", ToolName: "search_incidents", ToolDesc: "Search incidents by service, severity, or status."},
	{ID: "deployments", Name: "Deployment operations", Summary: "Review deployment history, environments, and release status.", Instructions: "Find the service or release before reading deployment details.", ToolName: "search_deployments", ToolDesc: "Search deployments by service, environment, or release."},
	{ID: "feature_flags", Name: "Feature flag operations", Summary: "Inspect feature flags, environments, and rollout status.", Instructions: "Search for the flag before inspecting its environment assignments.", ToolName: "search_feature_flags", ToolDesc: "Search feature flags by name, service, or environment."},
	{ID: "access_review", Name: "Access review", Summary: "Review access requests, memberships, and approval records.", Instructions: "Find the access record before reading its approval history.", ToolName: "search_access_requests", ToolDesc: "Search access requests by principal, resource, or status."},
	{ID: "subscriptions", Name: "Subscription operations", Summary: "Review subscriptions, plans, and renewal status.", Instructions: "Find the customer subscription before requesting plan details.", ToolName: "search_subscriptions", ToolDesc: "Search subscriptions by customer, plan, or status."},
	{ID: "expenses", Name: "Expense operations", Summary: "Inspect expense reports, receipts, and reimbursement status.", Instructions: "Search expense reports before reading individual entries.", ToolName: "search_expenses", ToolDesc: "Search expense reports by employee, period, or status."},
	{ID: "legal", Name: "Legal operations", Summary: "Review agreements, clauses, and renewal dates.", Instructions: "Find the agreement before requesting clause details.", ToolName: "search_agreements", ToolDesc: "Search agreements by party, title, or renewal date."},
	{ID: "compliance", Name: "Compliance operations", Summary: "Inspect controls, evidence, and review status.", Instructions: "Locate the control or review before reading its evidence.", ToolName: "search_controls", ToolDesc: "Search compliance controls by owner, framework, or status."},
	{ID: "sales_ops", Name: "Sales operations", Summary: "Review opportunities, accounts, and sales stages.", Instructions: "Find the opportunity before requesting account or stage details.", ToolName: "search_opportunities", ToolDesc: "Search sales opportunities by account, owner, or stage."},
	{ID: "marketing", Name: "Marketing operations", Summary: "Inspect campaigns, audiences, and delivery status.", Instructions: "Search for the campaign before requesting audience details.", ToolName: "search_campaigns", ToolDesc: "Search campaigns by name, channel, or status."},
	{ID: "customer_success", Name: "Customer success", Summary: "Review customer health, plans, and engagement history.", Instructions: "Find the customer record before reading its health details.", ToolName: "search_customer_accounts", ToolDesc: "Search customer accounts by name, tier, or health status."},
	{ID: "notifications", Name: "Notification operations", Summary: "Inspect notification preferences, templates, and delivery history.", Instructions: "Find the notification profile before reading delivery details.", ToolName: "search_notifications", ToolDesc: "Search notifications by recipient, channel, or status."},
	{ID: "forms", Name: "Forms operations", Summary: "Review forms, submissions, and response status.", Instructions: "Locate the form before requesting submission details.", ToolName: "search_forms", ToolDesc: "Search forms by title, owner, or status."},
	{ID: "knowledge_base", Name: "Knowledge base", Summary: "Search articles, collections, and publication metadata.", Instructions: "Search the knowledge base before requesting an article.", ToolName: "search_articles", ToolDesc: "Search knowledge articles by title, topic, or status."},
	{ID: "facilities", Name: "Facilities operations", Summary: "Review rooms, locations, and maintenance requests.", Instructions: "Find the location or request before reading its details.", ToolName: "search_facilities", ToolDesc: "Search facilities by location, type, or status."},
	{ID: "security", Name: "Security operations", Summary: "Inspect security findings, assets, and remediation status.", Instructions: "Locate the finding or asset before reading remediation details.", ToolName: "search_findings", ToolDesc: "Search security findings by asset, severity, or status."},
}

var generatedToolVariants = []string{
	"list_recent", "lookup_metadata", "search_history", "find_related", "get_summary", "list_associations",
}

func generatedToolName(skillID string, ordinal int) string {
	if ordinal < 1 {
		ordinal = 1
	}
	variant := generatedToolVariants[(ordinal-1)%len(generatedToolVariants)]
	name := fmt.Sprintf("%s_%s", skillID, variant)
	if ordinal > len(generatedToolVariants) {
		name = fmt.Sprintf("%s_%02d", name, (ordinal-1)/len(generatedToolVariants)+1)
	}
	return name
}

func generatedToolDescription(skillID, _ string) string {
	return fmt.Sprintf("Retrieve %s records or metadata for review.", skillID)
}

// Scale returns a deterministic catalog with at least targetSkills Skills and
// exactly targetTools tool declarations. Existing capabilities are preserved;
// any newly generated Skill, ToolSet, and tool is a local, read-only
// distractor. The generated Skills each own one ToolSet and at least one tool
// before the remaining tool budget is distributed as decoys.
//
// targetSkills is a lower bound relative to the receiver; targetTools is the
// exact resulting declaration count. A request that would remove existing
// capabilities is rejected, as is a tool budget smaller than the requested
// Skill count. This makes accidental under-sized benchmark runs fail early
// instead of silently changing task semantics.
func (c *Catalog) Scale(targetSkills, targetTools int) (*Catalog, error) {
	if c == nil {
		return nil, fmt.Errorf("catalog: cannot scale a nil catalog")
	}
	if targetSkills < 0 || targetTools < 0 {
		return nil, fmt.Errorf("catalog: scale targets must be non-negative")
	}
	existingSkills := len(c.skills)
	existingTools := len(c.tools)
	if targetSkills < existingSkills {
		return nil, fmt.Errorf("catalog: target skill count %d is below existing count %d", targetSkills, existingSkills)
	}
	if targetTools < existingTools {
		return nil, fmt.Errorf("catalog: target tool count %d is below existing count %d", targetTools, existingTools)
	}
	minimumTools := existingTools
	if addedSkills := targetSkills - existingSkills; addedSkills > 0 {
		minimumTools += addedSkills
	}
	if targetTools < minimumTools {
		return nil, fmt.Errorf("catalog: target tool count %d cannot provide one tool for each added Skill (minimum %d)", targetTools, minimumTools)
	}

	manifest := c.Manifest()
	usedSkills := make(map[string]bool, len(manifest.Skills))
	usedSets := make(map[string]bool, len(manifest.ToolSets))
	usedTools := make(map[string]bool, len(manifest.Tools))
	for _, spec := range manifest.Skills {
		usedSkills[spec.ID] = true
	}
	for _, spec := range manifest.ToolSets {
		usedSets[spec.ID] = true
	}
	for _, spec := range manifest.Tools {
		usedTools[spec.Name] = true
	}

	// Add Skills first. Each generated Skill gets a private ToolSet and a
	// private tool, so every newly added Skill is useful for activation-menu
	// experiments even when targetTools == targetSkills.
	for len(manifest.Skills) < targetSkills {
		ordinal := len(manifest.Skills) - existingSkills + 1
		template := generatedSkillTemplates[(ordinal-1)%len(generatedSkillTemplates)]
		id := template.ID
		serial := 1
		for usedSkills[id] {
			serial++
			id = fmt.Sprintf("%s-%02d", template.ID, serial)
		}
		// Keep model-facing function names within the 64-character limit used by
		// OpenAI-compatible tool APIs. The generated identifiers are ordinary
		// capability names; the benchmark-only classification stays in metadata.
		setID := fmt.Sprintf("%s-tools", id)
		for usedSets[setID] {
			serial++
			id = fmt.Sprintf("%s-%02d", template.ID, serial)
			setID = fmt.Sprintf("%s-tools", id)
		}
		displayName := template.Name
		if serial > 1 {
			displayName = fmt.Sprintf("%s %d", template.Name, serial)
		}
		toolName := template.ToolName
		toolOrdinal := 1
		for usedTools[toolName] {
			toolOrdinal++
			toolName = fmt.Sprintf("%s_%02d", template.ToolName, toolOrdinal)
		}

		usedSkills[id] = true
		usedSets[setID] = true
		usedTools[toolName] = true
		manifest.Skills = append(manifest.Skills, SkillSpec{
			ID:           id,
			Name:         displayName,
			Summary:      template.Summary,
			Instructions: template.Instructions + "\n",
			ToolSetID:    setID,
			ToolNames:    []string{toolName},
			Tags:         []string{"generated", "distractor", "local"},
		})
		manifest.ToolSets = append(manifest.ToolSets, ToolSetSpec{
			ID:        setID,
			Name:      setID,
			SkillID:   id,
			ToolNames: []string{toolName},
		})
		manifest.Tools = append(manifest.Tools, ToolSpec{
			Name:        toolName,
			Description: template.ToolDesc,
			SkillID:     id,
			ToolSetID:   setID,
			InputSchema: objectSchema(stringField("query", "Optional query text.", false)),
			ReadOnly:    true,
			Distractor:  true,
			Tags:        []string{"generated", "distractor", "local"},
		})
	}

	// Fill the remaining budget with deterministic decoys. Sorting the set IDs
	// makes the round-robin assignment independent of map iteration order.
	setOrder := make([]string, 0, len(manifest.ToolSets))
	for _, set := range manifest.ToolSets {
		setOrder = append(setOrder, set.ID)
	}
	sort.Strings(setOrder)
	for len(manifest.Tools) < targetTools {
		index := len(manifest.Tools) + 1
		setID := setOrder[(index-1)%len(setOrder)]
		set, ok := c.ToolSet(setID)
		if !ok {
			// Generated sets are not in c; locate them in the manifest instead.
			for _, candidate := range manifest.ToolSets {
				if candidate.ID == setID {
					set = candidate
					ok = true
					break
				}
			}
		}
		if !ok {
			return nil, fmt.Errorf("catalog: tool set %q disappeared", setID)
		}
		// Use the ToolSet id rather than the verbose Skill id as the decoy
		// prefix; this keeps the fully-qualified name short for provider APIs.
		name := generatedToolName(set.SkillID, index)
		for usedTools[name] {
			index++
			name = generatedToolName(set.SkillID, index)
		}
		usedTools[name] = true
		manifest.Tools = append(manifest.Tools, ToolSpec{
			Name:        name,
			Description: generatedToolDescription(set.SkillID, name),
			SkillID:     set.SkillID,
			ToolSetID:   set.ID,
			InputSchema: objectSchema(stringField("query", "Optional query text.", false)),
			ReadOnly:    true,
			Distractor:  true,
			Tags:        []string{"generated", "distractor", "local"},
		})
	}
	return NewFromManifest(manifest)
}

// Scale creates a scaled copy of the built-in catalog. It is a convenience
// for callers that do not need to retain a custom base catalog.
func Scale(targetSkills, targetTools int) (*Catalog, error) {
	return Default().Scale(targetSkills, targetTools)
}

// ScaleDistractors returns a catalog with at least targetTools declarations.
// Existing tools and Skill/ToolSet mappings are preserved; any additional
// declarations are deterministic, read-only distractors distributed evenly
// across the existing ToolSets.  This is useful for menu-size sweeps such as
// 64/128/256/512 without introducing external services.
func (c *Catalog) ScaleDistractors(targetTools int) (*Catalog, error) {
	if c == nil {
		return nil, fmt.Errorf("catalog: cannot scale a nil catalog")
	}
	if targetTools < 0 {
		return nil, fmt.Errorf("catalog: target tool count must be non-negative")
	}
	if targetTools <= len(c.tools) {
		return NewFromManifest(c.Manifest())
	}
	manifest := c.Manifest()
	sets := manifest.ToolSets
	if len(sets) == 0 {
		return nil, fmt.Errorf("catalog: cannot add distractors without tool sets")
	}
	used := make(map[string]bool, len(manifest.Tools))
	for _, spec := range manifest.Tools {
		used[spec.Name] = true
	}
	setOrder := make([]string, 0, len(sets))
	for _, set := range sets {
		setOrder = append(setOrder, set.ID)
	}
	sort.Strings(setOrder)
	for len(manifest.Tools) < targetTools {
		index := len(manifest.Tools) + 1
		setID := setOrder[(index-1)%len(setOrder)]
		set, ok := c.ToolSet(setID)
		if !ok {
			return nil, fmt.Errorf("catalog: tool set %q disappeared", setID)
		}
		name := generatedToolName(set.SkillID, index)
		for used[name] {
			index++
			name = generatedToolName(set.SkillID, index)
		}
		used[name] = true
		manifest.Tools = append(manifest.Tools, ToolSpec{
			Name:        name,
			Description: generatedToolDescription(set.SkillID, name),
			SkillID:     set.SkillID,
			ToolSetID:   set.ID,
			InputSchema: objectSchema(stringField("query", "Search text.", false)),
			ReadOnly:    true,
			Distractor:  true,
			Tags:        []string{"generated", "distractor"},
		})
	}
	return NewFromManifest(manifest)
}

// NewFromManifest reconstructs a catalog from a previously serialized
// manifest.  It is intentionally a thin alias around New for callers that
// need to scale or persist a catalog between runs.
func NewFromManifest(manifest Manifest) (*Catalog, error) {
	return New(manifest.Skills, manifest.ToolSets, manifest.Tools)
}
