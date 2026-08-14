//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package longmemeval

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/dataset"
	"trpc.group/trpc-go/trpc-agent-go/agent"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/tool"
)

const lmeReplayVersion = 1

type lmeSeedAgent struct {
	mu               sync.Mutex
	assistantMessage model.Message
}

func (s *lmeSeedAgent) Run(
	ctx context.Context,
	invocation *agent.Invocation,
) (<-chan *event.Event, error) {
	ch := make(chan *event.Event, 1)
	s.mu.Lock()
	assistantMessage := s.assistantMessage
	s.mu.Unlock()
	go func() {
		defer close(ch)
		if invocation == nil {
			return
		}
		_ = event.EmitEvent(ctx, ch, event.NewResponseEvent(
			invocation.InvocationID,
			lmeSeedAgentName,
			&model.Response{
				Done: true,
				Choices: []model.Choice{{
					Message: assistantMessage,
				}},
			},
		))
	}()
	return ch, nil
}

func (s *lmeSeedAgent) setAssistantMessage(message model.Message) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if message.Role == "" {
		message.Role = model.RoleAssistant
	}
	s.assistantMessage = message
}

func (*lmeSeedAgent) Tools() []tool.Tool { return nil }

func (*lmeSeedAgent) Info() agent.Info {
	return agent.Info{Name: lmeSeedAgentName, Description: "LongMemEval seed agent."}
}

func (*lmeSeedAgent) SubAgents() []agent.Agent { return nil }

func (*lmeSeedAgent) FindSubAgent(_ string) agent.Agent { return nil }

type lmeReplayIndex struct {
	Version         int                `json:"version"`
	DatasetDigest   string             `json:"dataset_digest"`
	ManifestDigest  string             `json:"manifest_digest,omitempty"`
	SelectionDigest string             `json:"selection_digest"`
	ReplayDigest    string             `json:"replay_digest"`
	Cases           []lmeArtifactEntry `json:"cases"`
}

type lmeArtifactEntry struct {
	CaseID string `json:"case_id"`
	File   string `json:"file"`
	Digest string `json:"digest"`
}

type lmeReplayCase struct {
	Version  int                `json:"version"`
	CaseID   string             `json:"case_id"`
	Sessions []lmeReplaySession `json:"sessions"`
}

type lmeReplaySession struct {
	SessionIndex    int             `json:"session_index"`
	SessionID       string          `json:"session_id"`
	ObservationTime string          `json:"observation_time"`
	Turns           []lmeReplayTurn `json:"turns"`
}

type lmeReplayTurn struct {
	TurnIndex int    `json:"turn_index"`
	TurnID    string `json:"turn_id"`
	Role      string `json:"role"`
	Content   string `json:"content"`
}

type lmeReplayCorpus struct {
	Index *lmeReplayIndex
	Cases map[string]*lmeReplayCase
}

func buildLMEReplayCases(
	instances []*dataset.LongMemEvalInstance,
) ([]*lmeReplayCase, error) {
	cases := make([]*lmeReplayCase, 0, len(instances))
	seen := make(map[string]struct{}, len(instances))
	for _, inst := range instances {
		if inst == nil {
			return nil, fmt.Errorf("LongMemEval replay contains nil case")
		}
		if inst.QuestionID == "" {
			return nil, fmt.Errorf("LongMemEval replay case ID is required")
		}
		if _, ok := seen[inst.QuestionID]; ok {
			return nil, fmt.Errorf("duplicate LongMemEval replay case ID %q", inst.QuestionID)
		}
		seen[inst.QuestionID] = struct{}{}
		replayCase, err := buildLMEReplayCase(inst)
		if err != nil {
			return nil, fmt.Errorf("build replay case %s: %w", inst.QuestionID, err)
		}
		cases = append(cases, replayCase)
	}
	return cases, nil
}

func buildLMEReplayCase(inst *dataset.LongMemEvalInstance) (*lmeReplayCase, error) {
	if len(inst.HaystackSessions) != len(inst.HaystackSessionIDs) ||
		len(inst.HaystackSessions) != len(inst.HaystackDates) {
		return nil, fmt.Errorf(
			"haystack length mismatch: sessions=%d ids=%d dates=%d",
			len(inst.HaystackSessions),
			len(inst.HaystackSessionIDs),
			len(inst.HaystackDates),
		)
	}
	indexes, err := lmeChronologicalSessionIndexes(inst)
	if err != nil {
		return nil, err
	}
	replayCase := &lmeReplayCase{
		Version:  lmeReplayVersion,
		CaseID:   inst.QuestionID,
		Sessions: make([]lmeReplaySession, 0, len(indexes)),
	}
	seenSessionIDs := make(map[string]struct{}, len(indexes))
	for sessionOrder, sourceIndex := range indexes {
		replaySession, err := buildLMEReplaySession(inst, sourceIndex, sessionOrder)
		if err != nil {
			return nil, err
		}
		if _, ok := seenSessionIDs[replaySession.SessionID]; ok {
			return nil, fmt.Errorf("duplicate LongMemEval replay session ID %q", replaySession.SessionID)
		}
		seenSessionIDs[replaySession.SessionID] = struct{}{}
		replayCase.Sessions = append(replayCase.Sessions, replaySession)
	}
	return replayCase, nil
}

func buildLMEReplaySession(
	inst *dataset.LongMemEvalInstance,
	sourceIndex int,
	sessionOrder int,
) (lmeReplaySession, error) {
	sessionID := inst.HaystackSessionIDs[sourceIndex]
	if sessionID == "" {
		return lmeReplaySession{}, fmt.Errorf("session %d has empty ID", sourceIndex)
	}
	observedAt, ok := parseLMETime(inst.HaystackDates[sourceIndex])
	if !ok {
		return lmeReplaySession{}, fmt.Errorf(
			"session %s has invalid observation time %q",
			sessionID,
			inst.HaystackDates[sourceIndex],
		)
	}
	turns := make([]lmeReplayTurn, 0, len(inst.HaystackSessions[sourceIndex]))
	for turnIndex, turn := range inst.HaystackSessions[sourceIndex] {
		if turn.Role != "user" && turn.Role != "assistant" {
			return lmeReplaySession{}, fmt.Errorf(
				"session %s turn %d has unsupported role %q",
				sessionID,
				turnIndex,
				turn.Role,
			)
		}
		if !utf8.ValidString(turn.Content) {
			return lmeReplaySession{}, fmt.Errorf(
				"session %s turn %d contains invalid UTF-8",
				sessionID,
				turnIndex,
			)
		}
		turns = append(turns, lmeReplayTurn{
			TurnIndex: turnIndex,
			TurnID: lmeStableArtifactID(
				"turn",
				inst.QuestionID,
				sessionID,
				strconv.Itoa(turnIndex),
			),
			Role:    turn.Role,
			Content: turn.Content,
		})
	}
	return lmeReplaySession{
		SessionIndex:    sessionOrder,
		SessionID:       sessionID,
		ObservationTime: observedAt.Format(time.RFC3339Nano),
		Turns:           turns,
	}, nil
}

func lmeChronologicalSessionIndexes(
	inst *dataset.LongMemEvalInstance,
) ([]int, error) {
	indexes := make([]int, len(inst.HaystackSessions))
	times := make([]time.Time, len(inst.HaystackSessions))
	for i := range indexes {
		indexes[i] = i
		parsed, ok := parseLMETime(inst.HaystackDates[i])
		if !ok {
			return nil, fmt.Errorf("invalid haystack date at index %d: %q", i, inst.HaystackDates[i])
		}
		times[i] = parsed
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		leftIndex := indexes[i]
		rightIndex := indexes[j]
		if times[leftIndex].Equal(times[rightIndex]) {
			return leftIndex < rightIndex
		}
		return times[leftIndex].Before(times[rightIndex])
	})
	return indexes, nil
}

type lmeReplayPayload struct {
	event   event.Event
	choice  model.Choice
	role    string
	content string
}

func lmeReplayEventPayloads(evt event.Event) []lmeReplayPayload {
	if evt.Response == nil || len(evt.Response.Choices) == 0 {
		return nil
	}
	payloads := make([]lmeReplayPayload, 0, len(evt.Response.Choices))
	for _, choice := range evt.Response.Choices {
		msg := choice.Message
		if !isLMEReplayPayloadMessage(msg) {
			continue
		}
		payloads = append(payloads, lmeReplayPayload{
			event:   evt,
			choice:  choice,
			role:    string(msg.Role),
			content: msg.Content,
		})
	}
	return payloads
}

func isLMEReplayPayloadMessage(msg model.Message) bool {
	if msg.Role != model.RoleUser && msg.Role != model.RoleAssistant {
		return false
	}
	if msg.Content == "" {
		return false
	}
	return msg.ToolID == "" && len(msg.ToolCalls) == 0
}

func cleanLMEReplayEvent(
	evt event.Event,
	choice model.Choice,
	turn lmeReplayTurn,
	eventTime time.Time,
) (event.Event, error) {
	cloned := evt.Clone()
	if cloned == nil {
		return event.Event{}, fmt.Errorf("clone replay event: nil event")
	}
	cloned.ID = evt.ID
	cloned.Timestamp = eventTime
	cleanChoice := choice
	cleanChoice.Index = 0
	cleanChoice.Delta = model.Message{}
	if cloned.Response == nil {
		cloned.Response = &model.Response{}
	}
	cloned.Response.Choices = []model.Choice{cleanChoice}
	if err := event.SetExtension(cloned, dataset.EventExtensionLongMemEvalTurnID, turn.TurnID); err != nil {
		return event.Event{}, fmt.Errorf("set replay turn extension: %w", err)
	}
	return *cloned, nil
}

const lmeReplayIndexFile = "index.json"

func prepareLMEInputArtifacts(
	cfg lmeRunConfig,
	instances []*dataset.LongMemEvalInstance,
) (lmeRunConfig, error) {
	corpus, err := ensureLMEReplayArtifacts(cfg, instances)
	if err != nil {
		return lmeRunConfig{}, err
	}
	plan, planRoot, err := ensureLMEBuildPlan(cfg, corpus)
	if err != nil {
		return lmeRunConfig{}, err
	}
	cfg.ReplayDigest = corpus.Index.ReplayDigest
	cfg.BuildPlanRoot = planRoot
	cfg.BuildPlanDigest = plan.Index.BuildPlanDigest
	cfg.BuildTokenizer = plan.Index.Config.Tokenizer
	cfg.BuildStats = plan.Index.Stats
	return cfg, nil
}

// ensureLMEReplayArtifacts creates the canonical replay once. Existing
// artifacts are validated against their source digests and never overwritten.
func ensureLMEReplayArtifacts(
	cfg lmeRunConfig,
	instances []*dataset.LongMemEvalInstance,
) (*lmeReplayCorpus, error) {
	datasetDigest, err := lmeFileDigest(cfg.DatasetPath)
	if err != nil {
		return nil, fmt.Errorf("digest LongMemEval dataset: %w", err)
	}
	manifestDigest := ""
	if cfg.ManifestPath != "" {
		manifestDigest, err = lmeFileDigest(cfg.ManifestPath)
		if err != nil {
			return nil, fmt.Errorf("digest LongMemEval manifest: %w", err)
		}
	}
	cases, err := buildLMEReplayCases(instances)
	if err != nil {
		return nil, err
	}
	expected, err := newLMEReplayCorpus(datasetDigest, manifestDigest, cases)
	if err != nil {
		return nil, err
	}
	indexPath := filepath.Join(cfg.ReplayRoot, lmeReplayIndexFile)
	if _, err := os.Stat(indexPath); err == nil {
		actual, loadErr := loadLMEReplayCorpus(cfg.ReplayRoot)
		if loadErr != nil {
			return nil, loadErr
		}
		if err := verifyLMEReplaySource(actual.Index, expected.Index); err != nil {
			return nil, err
		}
		return actual, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("stat replay index: %w", err)
	}
	if err := writeLMEReplayCorpus(cfg.ReplayRoot, expected); err != nil {
		return nil, err
	}
	return loadLMEReplayCorpus(cfg.ReplayRoot)
}

func newLMEReplayCorpus(
	datasetDigest string,
	manifestDigest string,
	cases []*lmeReplayCase,
) (*lmeReplayCorpus, error) {
	corpus := &lmeReplayCorpus{
		Index: &lmeReplayIndex{
			Version:        lmeReplayVersion,
			DatasetDigest:  datasetDigest,
			ManifestDigest: manifestDigest,
			Cases:          make([]lmeArtifactEntry, 0, len(cases)),
		},
		Cases: make(map[string]*lmeReplayCase, len(cases)),
	}
	caseIDs := make([]string, 0, len(cases))
	for _, replayCase := range cases {
		data, err := lmeMarshalArtifact(replayCase)
		if err != nil {
			return nil, fmt.Errorf("marshal replay case %s: %w", replayCase.CaseID, err)
		}
		entry := lmeArtifactEntry{
			CaseID: replayCase.CaseID,
			File:   lmeCaseArtifactFile(replayCase.CaseID),
			Digest: lmeDigestBytes(data),
		}
		corpus.Index.Cases = append(corpus.Index.Cases, entry)
		corpus.Cases[replayCase.CaseID] = replayCase
		caseIDs = append(caseIDs, replayCase.CaseID)
	}
	selectionData, err := json.Marshal(caseIDs)
	if err != nil {
		return nil, fmt.Errorf("marshal replay selection: %w", err)
	}
	corpus.Index.SelectionDigest = lmeDigestBytes(selectionData)
	corpus.Index.ReplayDigest, err = lmeReplayIndexDigest(corpus.Index)
	if err != nil {
		return nil, err
	}
	return corpus, nil
}

func writeLMEReplayCorpus(root string, corpus *lmeReplayCorpus) error {
	if corpus == nil || corpus.Index == nil {
		return fmt.Errorf("nil replay corpus")
	}
	if err := os.MkdirAll(filepath.Join(root, "cases"), 0755); err != nil {
		return fmt.Errorf("create replay cases directory: %w", err)
	}
	for _, entry := range corpus.Index.Cases {
		replayCase := corpus.Cases[entry.CaseID]
		if replayCase == nil {
			return fmt.Errorf("replay case %s missing before write", entry.CaseID)
		}
		if err := lmeWriteImmutableJSON(filepath.Join(root, filepath.FromSlash(entry.File)), replayCase); err != nil {
			return err
		}
	}
	return lmeWriteImmutableJSON(filepath.Join(root, lmeReplayIndexFile), corpus.Index)
}

func loadLMEReplayCorpus(root string) (*lmeReplayCorpus, error) {
	index, err := loadLMEReplayIndex(root)
	if err != nil {
		return nil, err
	}
	cases, err := loadLMEReplayCases(root, index)
	if err != nil {
		return nil, err
	}
	return &lmeReplayCorpus{Index: index, Cases: cases}, nil
}

func loadLMEReplayIndex(root string) (*lmeReplayIndex, error) {
	var index lmeReplayIndex
	if err := lmeReadStrictJSON(filepath.Join(root, lmeReplayIndexFile), &index); err != nil {
		return nil, fmt.Errorf("read replay index: %w", err)
	}
	if index.Version != lmeReplayVersion {
		return nil, fmt.Errorf("replay version %d does not match %d", index.Version, lmeReplayVersion)
	}
	if index.DatasetDigest == "" || index.SelectionDigest == "" || index.ReplayDigest == "" {
		return nil, fmt.Errorf("replay index is missing required digests")
	}
	digest, err := lmeReplayIndexDigest(&index)
	if err != nil {
		return nil, err
	}
	if digest != index.ReplayDigest {
		return nil, fmt.Errorf("replay index digest mismatch: got %s want %s", digest, index.ReplayDigest)
	}
	return &index, nil
}

func loadLMEReplayCases(root string, index *lmeReplayIndex) (map[string]*lmeReplayCase, error) {
	cases := make(map[string]*lmeReplayCase, len(index.Cases))
	seen := make(map[string]struct{}, len(index.Cases))
	for _, entry := range index.Cases {
		if entry.CaseID == "" || entry.File == "" || entry.Digest == "" {
			return nil, fmt.Errorf("replay index contains incomplete case entry")
		}
		if _, ok := seen[entry.CaseID]; ok {
			return nil, fmt.Errorf("replay index contains duplicate case %s", entry.CaseID)
		}
		seen[entry.CaseID] = struct{}{}
		replayCase, err := loadLMEReplayCase(root, entry)
		if err != nil {
			return nil, err
		}
		cases[entry.CaseID] = replayCase
	}
	return cases, nil
}

func loadLMEReplayCase(root string, entry lmeArtifactEntry) (*lmeReplayCase, error) {
	path, err := lmeArtifactPath(root, entry.File)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read replay case %s: %w", entry.CaseID, err)
	}
	if got := lmeDigestBytes(data); got != entry.Digest {
		return nil, fmt.Errorf(
			"replay case %s digest mismatch: got %s want %s",
			entry.CaseID,
			got,
			entry.Digest,
		)
	}
	var replayCase lmeReplayCase
	if err := lmeDecodeStrict(data, &replayCase); err != nil {
		return nil, fmt.Errorf("decode replay case %s: %w", entry.CaseID, err)
	}
	if replayCase.Version != lmeReplayVersion || replayCase.CaseID != entry.CaseID {
		return nil, fmt.Errorf("replay case %s identity mismatch", entry.CaseID)
	}
	if entry.File != lmeCaseArtifactFile(entry.CaseID) {
		return nil, fmt.Errorf("replay case %s has non-canonical artifact path", entry.CaseID)
	}
	if err := validateLMEReplayCase(&replayCase); err != nil {
		return nil, fmt.Errorf("validate replay case %s: %w", entry.CaseID, err)
	}
	return &replayCase, nil
}

func validateLMEReplayCase(replayCase *lmeReplayCase) error {
	seenSessions := make(map[string]struct{}, len(replayCase.Sessions))
	for sessionIndex, replaySession := range replayCase.Sessions {
		if replaySession.SessionIndex != sessionIndex {
			return fmt.Errorf(
				"session %s index is %d, want %d",
				replaySession.SessionID,
				replaySession.SessionIndex,
				sessionIndex,
			)
		}
		if replaySession.SessionID == "" {
			return fmt.Errorf("session %d has empty ID", sessionIndex)
		}
		if _, ok := seenSessions[replaySession.SessionID]; ok {
			return fmt.Errorf("duplicate session ID %s", replaySession.SessionID)
		}
		seenSessions[replaySession.SessionID] = struct{}{}
		if _, err := time.Parse(time.RFC3339Nano, replaySession.ObservationTime); err != nil {
			return fmt.Errorf("session %s observation time: %w", replaySession.SessionID, err)
		}
		for turnIndex, turn := range replaySession.Turns {
			if turn.TurnIndex != turnIndex {
				return fmt.Errorf(
					"session %s turn index is %d, want %d",
					replaySession.SessionID,
					turn.TurnIndex,
					turnIndex,
				)
			}
			wantID := lmeStableArtifactID(
				"turn",
				replayCase.CaseID,
				replaySession.SessionID,
				strconv.Itoa(turnIndex),
			)
			if turn.TurnID != wantID {
				return fmt.Errorf("session %s turn %d has unstable ID", replaySession.SessionID, turnIndex)
			}
			if turn.Role != "user" && turn.Role != "assistant" {
				return fmt.Errorf("session %s turn %d has unsupported role %q", replaySession.SessionID, turnIndex, turn.Role)
			}
			if !utf8.ValidString(turn.Content) {
				return fmt.Errorf("session %s turn %d contains invalid UTF-8", replaySession.SessionID, turnIndex)
			}
		}
	}
	return nil
}

func verifyLMEReplaySource(actual *lmeReplayIndex, expected *lmeReplayIndex) error {
	if actual.DatasetDigest != expected.DatasetDigest ||
		actual.ManifestDigest != expected.ManifestDigest ||
		actual.SelectionDigest != expected.SelectionDigest ||
		actual.ReplayDigest != expected.ReplayDigest {
		return fmt.Errorf(
			"existing replay artifact does not match dataset, manifest, or selected cases",
		)
	}
	return nil
}

func lmeReplayIndexDigest(index *lmeReplayIndex) (string, error) {
	copyIndex := *index
	copyIndex.ReplayDigest = ""
	data, err := json.Marshal(copyIndex)
	if err != nil {
		return "", fmt.Errorf("marshal replay index digest payload: %w", err)
	}
	return lmeDigestBytes(data), nil
}

func lmeWriteImmutableJSON(path string, value any) error {
	data, err := lmeMarshalArtifact(value)
	if err != nil {
		return fmt.Errorf("marshal immutable artifact %s: %w", path, err)
	}
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create immutable artifact temporary file for %s: %w", path, err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0644); err != nil {
		_ = file.Close()
		return fmt.Errorf("set immutable artifact permissions for %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write immutable artifact %s: %w", path, err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("sync immutable artifact %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close immutable artifact %s: %w", path, err)
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			info, statErr := os.Lstat(path)
			if statErr != nil {
				return fmt.Errorf("inspect existing immutable artifact %s: %w", path, statErr)
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("immutable artifact already exists as a non-regular file: %s", path)
			}
			existing, readErr := os.ReadFile(path)
			if readErr != nil {
				return fmt.Errorf("read existing immutable artifact %s: %w", path, readErr)
			}
			if bytes.Equal(existing, data) {
				return nil
			}
			return fmt.Errorf("immutable artifact already exists with different content: %s", path)
		}
		return fmt.Errorf("publish immutable artifact %s: %w", path, err)
	}
	directory, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open immutable artifact directory %s: %w", dir, err)
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return fmt.Errorf("sync immutable artifact directory %s: %w", dir, err)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close immutable artifact directory %s: %w", dir, err)
	}
	return nil
}

func lmeMarshalArtifact(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func lmeReadStrictJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return lmeDecodeStrict(data, target)
}

func lmeDecodeStrict(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("JSON contains multiple values")
		}
		return err
	}
	return nil
}

func lmeArtifactPath(root string, relative string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(relative))
	if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("invalid artifact path %q", relative)
	}
	return filepath.Join(root, clean), nil
}

func lmeFileDigest(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return lmeDigestBytes(data), nil
}

func lmeDigestBytes(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func lmeStableArtifactID(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write([]byte(part))
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func lmeCaseArtifactFile(caseID string) string {
	fileName := lmeStableArtifactID("case-file", caseID) + ".json"
	return filepath.ToSlash(filepath.Join("cases", fileName))
}
