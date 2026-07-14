//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type billingRow struct {
	AgentName          string          `json:"agent_name"`
	InputTokens        flexibleInt64   `json:"input_tokens"`
	OutputTokens       flexibleInt64   `json:"output_tokens"`
	TotalTokens        flexibleInt64   `json:"total_tokens"`
	PromptCachedTokens flexibleInt64   `json:"prompt_cached_tokens"`
	Cost               flexibleDecimal `json:"cost"`
}

func (r *billingRow) UnmarshalJSON(data []byte) error {
	type rowAlias billingRow
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range []string{"agent_name", "input_tokens", "output_tokens", "total_tokens", "prompt_cached_tokens", "cost"} {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("missing required billing field %q", name)
		}
	}
	var decoded rowAlias
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*r = billingRow(decoded)
	return nil
}

type billingDocument struct {
	SchemaVersion        int           `json:"schema_version"`
	GeneratedAt          time.Time     `json:"generated_at"`
	AgentName            string        `json:"agent_name"`
	ObservationCodec     string        `json:"observation_codec"`
	ExperimentID         string        `json:"experiment_id"`
	InputTokens          int64         `json:"input_tokens"`
	OutputTokens         int64         `json:"output_tokens"`
	TotalTokens          int64         `json:"total_tokens"`
	PromptCachedTokens   int64         `json:"prompt_cached_tokens"`
	PromptUncachedTokens int64         `json:"prompt_uncached_tokens"`
	Cost                 string        `json:"cost"`
	TotalTokenDelta      int64         `json:"total_token_delta"`
	Source               billingSource `json:"source"`
}

type billingSource struct {
	Path        string `json:"path"`
	SHA256      string `json:"sha256"`
	Rows        int    `json:"rows"`
	MatchedRows int    `json:"matched_rows"`
	IgnoredRows int    `json:"ignored_rows"`
}

type billingIdentity struct {
	ObservationCodec string `json:"observation_codec"`
	BillingAgentName string `json:"billing_agent_name"`
	ExperimentID     string `json:"experiment_id"`
}

type flexibleInt64 int64

func (v *flexibleInt64) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	var parsed int64
	if _, err := fmt.Sscan(raw, &parsed); err != nil {
		return fmt.Errorf("invalid integer %q", raw)
	}
	if fmt.Sprintf("%d", parsed) != raw && raw != "+"+fmt.Sprintf("%d", parsed) {
		return fmt.Errorf("invalid integer %q", raw)
	}
	*v = flexibleInt64(parsed)
	return nil
}

type flexibleDecimal string

func (v *flexibleDecimal) UnmarshalJSON(data []byte) error {
	raw := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if raw == "" {
		return fmt.Errorf("empty decimal")
	}
	if _, ok := new(big.Rat).SetString(raw); !ok {
		return fmt.Errorf("invalid decimal %q", raw)
	}
	*v = flexibleDecimal(raw)
	return nil
}

func runImportBilling(args []string) error {
	fs := flag.NewFlagSet("import-billing", flag.ExitOnError)
	input := fs.String("input", "", "backend billing JSON object or array")
	manifestPath := fs.String("manifest", "", "runner or shards manifest containing experiment identity")
	output := fs.String("output", "", "normalized billing JSON path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	for name, value := range map[string]string{"input": *input, "manifest": *manifestPath, "output": *output} {
		if err := required(fs, name, value); err != nil {
			return err
		}
	}
	var identity billingIdentity
	if err := readJSONFile(*manifestPath, &identity); err != nil {
		return fmt.Errorf("read experiment manifest: %w", err)
	}
	if strings.TrimSpace(identity.BillingAgentName) == "" || strings.TrimSpace(identity.ObservationCodec) == "" || strings.TrimSpace(identity.ExperimentID) == "" {
		return fmt.Errorf("experiment manifest is missing codec, billing agent name, or experiment id")
	}
	doc, err := normalizeBilling(*input, identity)
	if err != nil {
		return err
	}
	if err := writeJSON(*output, doc); err != nil {
		return err
	}
	fmt.Printf("agent_name=%s input_tokens=%d output_tokens=%d cached_tokens=%d cost=%s\nbilling=%s\n",
		doc.AgentName, doc.InputTokens, doc.OutputTokens, doc.PromptCachedTokens, doc.Cost, *output)
	return nil
}

func normalizeBilling(path string, identity billingIdentity) (billingDocument, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return billingDocument{}, fmt.Errorf("read billing export: %w", err)
	}
	rows, err := decodeBillingRows(data)
	if err != nil {
		return billingDocument{}, err
	}
	doc := billingDocument{
		SchemaVersion:    1,
		GeneratedAt:      time.Now().UTC(),
		AgentName:        identity.BillingAgentName,
		ObservationCodec: identity.ObservationCodec,
		ExperimentID:     identity.ExperimentID,
		Source:           billingSource{Path: absPath(path), Rows: len(rows)},
	}
	sum := new(big.Rat)
	for i, row := range rows {
		if row.AgentName != identity.BillingAgentName {
			doc.Source.IgnoredRows++
			continue
		}
		values := []struct {
			name  string
			value flexibleInt64
		}{{"input_tokens", row.InputTokens}, {"output_tokens", row.OutputTokens}, {"total_tokens", row.TotalTokens}, {"prompt_cached_tokens", row.PromptCachedTokens}}
		for _, value := range values {
			if value.value < 0 {
				return billingDocument{}, fmt.Errorf("billing row %d has negative %s", i, value.name)
			}
		}
		if row.PromptCachedTokens > row.InputTokens {
			return billingDocument{}, fmt.Errorf("billing row %d cached tokens exceed input tokens", i)
		}
		cost, ok := new(big.Rat).SetString(string(row.Cost))
		if !ok {
			return billingDocument{}, fmt.Errorf("billing row %d is missing a valid cost", i)
		}
		if cost.Sign() < 0 {
			return billingDocument{}, fmt.Errorf("billing row %d has negative cost", i)
		}
		doc.InputTokens += int64(row.InputTokens)
		doc.OutputTokens += int64(row.OutputTokens)
		doc.TotalTokens += int64(row.TotalTokens)
		doc.PromptCachedTokens += int64(row.PromptCachedTokens)
		sum.Add(sum, cost)
		doc.Source.MatchedRows++
	}
	if doc.Source.MatchedRows == 0 {
		return billingDocument{}, fmt.Errorf("billing export has no row for agent_name %q", identity.BillingAgentName)
	}
	doc.PromptUncachedTokens = doc.InputTokens - doc.PromptCachedTokens
	doc.TotalTokenDelta = doc.TotalTokens - doc.InputTokens - doc.OutputTokens
	doc.Cost, err = exactDecimal(sum)
	if err != nil {
		return billingDocument{}, fmt.Errorf("sum billing cost: %w", err)
	}
	hash := sha256.Sum256(data)
	doc.Source.SHA256 = hex.EncodeToString(hash[:])
	return doc, nil
}

func decodeBillingRows(data []byte) ([]billingRow, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 {
		return nil, fmt.Errorf("billing export is empty")
	}
	var rows []billingRow
	if trimmed[0] == '[' {
		if err := json.Unmarshal(trimmed, &rows); err != nil {
			return nil, fmt.Errorf("decode billing rows: %w", err)
		}
	} else {
		var row billingRow
		if err := json.Unmarshal(trimmed, &row); err != nil {
			return nil, fmt.Errorf("decode billing row: %w", err)
		}
		rows = []billingRow{row}
	}
	return rows, nil
}

func exactDecimal(value *big.Rat) (string, error) {
	denominator := new(big.Int).Set(value.Denom())
	twos, fives := 0, 0
	for new(big.Int).Mod(denominator, big.NewInt(2)).Sign() == 0 {
		denominator.Div(denominator, big.NewInt(2))
		twos++
	}
	for new(big.Int).Mod(denominator, big.NewInt(5)).Sign() == 0 {
		denominator.Div(denominator, big.NewInt(5))
		fives++
	}
	if denominator.Cmp(big.NewInt(1)) != 0 {
		return "", fmt.Errorf("cost is not a terminating decimal")
	}
	scale := twos
	if fives > scale {
		scale = fives
	}
	text := value.FloatString(scale)
	if strings.Contains(text, ".") {
		text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	}
	if text == "-0" || text == "" {
		return "0", nil
	}
	return text, nil
}

func defaultBillingPath(runID string) string {
	return filepath.Join("results", "runs", runID, "billing.json")
}
