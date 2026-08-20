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
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/memory/trpc-agent-go-impl/evaluation/scenarios"
	"trpc.group/trpc-go/trpc-agent-go/event"
	"trpc.group/trpc-go/trpc-agent-go/memory"
	memorymem0 "trpc.group/trpc-go/trpc-agent-go/memory/mem0"
	"trpc.group/trpc-go/trpc-agent-go/model"
	"trpc.group/trpc-go/trpc-agent-go/session"
)

func TestLMEMem0OSSReadMemoriesUsesObservableLimit(t *testing.T) {
	tests := []struct {
		name      string
		count     int
		wantError bool
	}{
		{name: "below cap"},
		{name: "at cap", count: lmeMem0MemoryReadLimit, wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: lmeRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
				if got := r.URL.Query().Get("top_k"); got != fmt.Sprint(lmeMem0MemoryReadLimit) {
					t.Errorf("top_k = %q, want %d", got, lmeMem0MemoryReadLimit)
				}
				results := make([]map[string]any, test.count)
				for i := range results {
					results[i] = map[string]any{
						"id":       fmt.Sprintf("memory-%d", i),
						"memory":   "content",
						"metadata": map[string]any{lmeMem0MetadataAppName: lmeAppMem0},
					}
				}
				data, err := json.Marshal(map[string]any{"results": results})
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(strings.NewReader(string(data))),
					Request:    r,
				}, nil
			})}

			inner, err := memorymem0.NewService(
				memorymem0.WithHost("http://mem0.example.test"),
				memorymem0.WithSelfHostedOSS(),
				memorymem0.WithHTTPClient(client),
			)
			if err != nil {
				t.Fatalf("NewService() error = %v", err)
			}
			defer inner.Close()
			service := &lmeMem0OSSService{inner: inner}
			_, err = service.ReadMemories(
				context.Background(),
				memory.UserKey{AppName: lmeAppMem0, UserID: "case-1"},
				lmeMemoryReadLimit,
			)
			if test.wantError && (err == nil || !strings.Contains(err.Error(), "observable limit")) {
				t.Fatalf("ReadMemories() error = %v, want observable limit", err)
			}
			if !test.wantError && err != nil {
				t.Fatalf("ReadMemories() error = %v", err)
			}
		})
	}
}

type lmeMem0CapturedRequest struct {
	Request lmeMem0OSSCreateRequest
	Raw     map[string]json.RawMessage
	APIKey  string
	Type    string
}

func TestLMEMem0OSSIngestSessionUsesProductionPayloadSynchronously(t *testing.T) {
	captured := make(chan lmeMem0CapturedRequest, 1)
	client := &http.Client{Transport: lmeRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			return nil, err
		}
		var request lmeMem0OSSCreateRequest
		if err := json.Unmarshal(data, &request); err != nil {
			return nil, err
		}
		var raw map[string]json.RawMessage
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, err
		}
		captured <- lmeMem0CapturedRequest{
			Request: request,
			Raw:     raw,
			APIKey:  r.Header.Get("X-API-Key"),
			Type:    r.Header.Get("Content-Type"),
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("")),
			Request:    r,
		}, nil
	})}

	service := &lmeMem0OSSService{
		host:   "http://mem0.example.test",
		apiKey: "test-key",
		client: client,
	}
	sess := session.NewSession(lmeAppMem0, "case-1", "session-1")
	first := time.Date(2025, time.January, 2, 3, 4, 5, 0, time.UTC)
	appendTestLMEReplayEvent(sess, model.RoleUser, "old user")
	setLastLMEEventTime(sess, first)
	appendTestLMEReplayEvent(sess, model.RoleAssistant, "new assistant")
	latest := first.Add(time.Second)
	setLastLMEEventTime(sess, latest)
	sess.SetState(
		memory.SessionStateKeyAutoMemoryLastExtractAt,
		[]byte(first.UTC().Format(time.RFC3339Nano)),
	)
	observationTime := first.UTC().Format(time.RFC3339Nano)
	metadata := lmeMem0BuildMetadata("case-1")(
		lmeBuildSessionPlan{
			SessionID:       "session-1",
			ObservationTime: observationTime,
		},
		lmeBuildPairPlan{PairID: "pair-1"},
		lmeBuildChunkPlan{ChunkID: "chunk-1"},
	)

	err := service.IngestSession(
		context.Background(),
		sess,
		session.WithIngestAgentID("seed-agent"),
		session.WithIngestRunID("seed-run"),
		session.WithIngestMetadata(metadata),
	)
	if err != nil {
		t.Fatalf("IngestSession() error = %v", err)
	}
	request := <-captured
	if request.APIKey != "test-key" || request.Type != "application/json" {
		t.Fatalf("request headers = key %q type %q", request.APIKey, request.Type)
	}
	if len(request.Request.Messages) != 1 ||
		request.Request.Messages[0].Role != "assistant" ||
		request.Request.Messages[0].Content != "new assistant" {
		t.Fatalf("request messages = %+v", request.Request.Messages)
	}
	if request.Request.UserID != "case-1" || request.Request.AgentID != "seed-agent" ||
		request.Request.RunID != "seed-run" || !request.Request.Infer {
		t.Fatalf("request identity = %+v", request.Request)
	}
	if request.Request.Metadata[lmeMem0MetadataAppName] != lmeAppMem0 ||
		request.Request.Metadata[lmeMem0MetadataObservationTime] != observationTime {
		t.Fatalf("request metadata = %+v", request.Request.Metadata)
	}
	if _, ok := request.Raw["prompt"]; !ok {
		t.Fatal("request does not contain the Mem0 OSS prompt field")
	}
	if !strings.Contains(request.Request.Prompt, "authoritative observation date") ||
		!strings.Contains(request.Request.Prompt, "2025-01-02") ||
		!strings.Contains(request.Request.Prompt, "server wall-clock date") {
		t.Fatalf("request prompt = %q", request.Request.Prompt)
	}
	got, err := lmeSessionWatermark(sess)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(latest) {
		t.Fatalf("watermark = %s, want %s", got, latest)
	}
}

func TestLMEMem0OSSIngestSessionDoesNotAdvanceWatermarkOnFailure(t *testing.T) {
	service := &lmeMem0OSSService{
		host: "http://mem0.example.test",
		client: &http.Client{Transport: lmeRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusInternalServerError,
				Body:       io.NopCloser(strings.NewReader("provider secret")),
				Request:    r,
			}, nil
		})},
	}
	sess := session.NewSession(lmeAppMem0, "case-2", "session-2")
	original := time.Date(2025, time.January, 1, 0, 0, 0, 0, time.UTC)
	sess.SetState(
		memory.SessionStateKeyAutoMemoryLastExtractAt,
		[]byte(original.Format(time.RFC3339Nano)),
	)
	appendTestLMEReplayEvent(sess, model.RoleUser, "new user")
	setLastLMEEventTime(sess, original.Add(time.Second))

	err := service.IngestSession(
		context.Background(),
		sess,
		session.WithIngestMetadata(map[string]any{
			lmeMem0MetadataObservationTime: original.Format(time.RFC3339Nano),
		}),
	)
	if err == nil {
		t.Fatal("IngestSession() error = nil, want provider failure")
	}
	if got := err.Error(); got == "" || strings.Contains(got, "provider secret") {
		t.Fatalf("IngestSession() error leaks provider content: %q", got)
	}
	got, watermarkErr := lmeSessionWatermark(sess)
	if watermarkErr != nil {
		t.Fatal(watermarkErr)
	}
	if !got.Equal(original) {
		t.Fatalf("watermark = %s, want unchanged %s", got, original)
	}
}

func TestLMEMem0OSSIngestSessionHonorsTimeout(t *testing.T) {
	requestStarted := make(chan struct{})
	service := &lmeMem0OSSService{
		host: "http://mem0.example.test",
		client: &http.Client{Transport: lmeRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
			close(requestStarted)
			<-req.Context().Done()
			return nil, req.Context().Err()
		})},
		timeout: 10 * time.Millisecond,
	}
	sess := session.NewSession(lmeAppMem0, "case-timeout", "session-timeout")
	appendTestLMEReplayEvent(sess, model.RoleUser, "new user")
	setLastLMEEventTime(sess, time.Now().UTC())

	err := service.IngestSession(
		context.Background(),
		sess,
		session.WithIngestMetadata(map[string]any{
			lmeMem0MetadataObservationTime: time.Now().UTC().Format(time.RFC3339Nano),
		}),
	)
	if err == nil {
		t.Fatal("IngestSession() error = nil, want timeout")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("IngestSession() error = %v, want context deadline", err)
	}
	select {
	case <-requestStarted:
	default:
		t.Fatal("Mem0 request did not reach the server")
	}
	got, watermarkErr := lmeSessionWatermark(sess)
	if watermarkErr != nil {
		t.Fatal(watermarkErr)
	}
	if !got.IsZero() {
		t.Fatalf("watermark = %s, want zero after timeout", got)
	}
}

func TestLMEMem0OSSIngestSessionRejectsMalformedWatermark(t *testing.T) {
	service := &lmeMem0OSSService{host: "http://mem0.example.test"}
	sess := session.NewSession(lmeAppMem0, "case-bad-watermark", "session-bad-watermark")
	sess.SetState(memory.SessionStateKeyAutoMemoryLastExtractAt, []byte("not-a-time"))
	appendTestLMEReplayEvent(sess, model.RoleUser, "new user")
	setLastLMEEventTime(sess, time.Now().UTC())

	err := service.IngestSession(context.Background(), sess)
	if err == nil || !strings.Contains(err.Error(), "ingestion watermark") {
		t.Fatalf("IngestSession() error = %v, want malformed watermark", err)
	}
}

func TestLMEMem0OSSIngestSessionRejectsInvalidObservationTime(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		want     string
	}{
		{name: "missing", want: "no observation time metadata"},
		{
			name: "wrong type",
			metadata: map[string]any{
				lmeMem0MetadataObservationTime: 123,
			},
			want: "not a string",
		},
		{
			name: "malformed",
			metadata: map[string]any{
				lmeMem0MetadataObservationTime: "not-a-time",
			},
			want: "parse mem0_oss benchmark observation time",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requestCount := 0
			service := &lmeMem0OSSService{
				host: "http://mem0.example.test",
				client: &http.Client{Transport: lmeRoundTripperFunc(func(
					r *http.Request,
				) (*http.Response, error) {
					requestCount++
					return nil, errors.New("unexpected request")
				})},
			}
			sess := session.NewSession(lmeAppMem0, "case-date", "session-date")
			appendTestLMEReplayEvent(sess, model.RoleUser, "new user")
			setLastLMEEventTime(sess, time.Now().UTC())

			err := service.IngestSession(
				context.Background(),
				sess,
				session.WithIngestMetadata(test.metadata),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("IngestSession() error = %v, want %q", err, test.want)
			}
			if requestCount != 0 {
				t.Fatalf("request count = %d, want 0", requestCount)
			}
			got, watermarkErr := lmeSessionWatermark(sess)
			if watermarkErr != nil {
				t.Fatal(watermarkErr)
			}
			if !got.IsZero() {
				t.Fatalf("watermark = %s, want zero", got)
			}
		})
	}
}

func TestLoadLMEMem0Preflight(t *testing.T) {
	path := writeTestLMEMem0Preflight(t, testLMEMem0PreflightDocument())
	summary, err := loadLMEMem0Preflight(path)
	if err != nil {
		t.Fatalf("loadLMEMem0Preflight() error = %v", err)
	}
	if summary.ServiceURL != "http://localhost:8888" ||
		summary.SourceCommit != strings.Repeat("b", 40) ||
		summary.Version != "2.0.11" ||
		summary.LLMModel != "gpt-4o-mini" ||
		summary.EmbedModel != "text-embedding-3-small" {
		t.Fatalf("summary = %+v", summary)
	}
	if !strings.HasPrefix(summary.Digest, "sha256:") ||
		summary.EnvironmentLockDigest != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("summary digests = %+v", summary)
	}
}

func TestLoadLMEMem0PreflightRejectsInvalidInput(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
		want   string
	}{
		{
			name: "failed status",
			mutate: func(document map[string]any) {
				document["status"] = "failed"
			},
			want: "status is not ok",
		},
		{
			name: "missing prompt capability",
			mutate: func(document map[string]any) {
				capabilities := document["capabilities"].(map[string]any)
				delete(capabilities, "observation_prompt")
			},
			want: "observation_prompt",
		},
		{
			name: "mutable revision",
			mutate: func(document map[string]any) {
				runtime := document["runtime"].(map[string]any)
				runtime["source"].(map[string]any)["commit"] = "main"
			},
			want: "not immutable",
		},
		{
			name: "invalid environment digest",
			mutate: func(document map[string]any) {
				document["environment_lock"].(map[string]any)["sha256"] = "not-a-digest"
			},
			want: "environment lock digest is invalid",
		},
		{
			name: "missing runtime model",
			mutate: func(document map[string]any) {
				runtime := document["runtime"].(map[string]any)
				runtime["runtime"].(map[string]any)["llm_model"] = ""
			},
			want: "runtime identity is incomplete",
		},
		{
			name: "invalid service URL",
			mutate: func(document map[string]any) {
				document["service_url"] = "not-a-url"
			},
			want: "service URL is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := testLMEMem0PreflightDocument()
			test.mutate(document)
			_, err := loadLMEMem0Preflight(writeTestLMEMem0Preflight(t, document))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("loadLMEMem0Preflight() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestReadLMEMem0PreflightRejectsUnsafeFile(t *testing.T) {
	validPath := writeTestLMEMem0Preflight(t, testLMEMem0PreflightDocument())
	symlinkPath := filepath.Join(t.TempDir(), "preflight-link.json")
	if err := os.Symlink(validPath, symlinkPath); err != nil {
		t.Fatal(err)
	}
	oversizedPath := filepath.Join(t.TempDir(), "oversized.json")
	file, err := os.Create(oversizedPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(lmeMem0MaxPreflightSize + 1); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "directory", path: t.TempDir()},
		{name: "symlink", path: symlinkPath},
		{name: "oversized", path: oversizedPath},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := readLMEMem0Preflight(test.path); err == nil {
				t.Fatal("readLMEMem0Preflight() error = nil")
			}
		})
	}
}

func TestCompleteLMEMem0Preflight(t *testing.T) {
	path := writeTestLMEMem0Preflight(t, testLMEMem0PreflightDocument())
	cfg := &lmeRunConfig{
		ModelName:         "gpt-4o-mini",
		EmbedModelName:    "text-embedding-3-small",
		Mem0Host:          "http://localhost:8888/",
		Mem0PreflightPath: path,
	}
	if err := completeLMEMem0Preflight(cfg); err != nil {
		t.Fatalf("completeLMEMem0Preflight() error = %v", err)
	}
	if cfg.Mem0Version != "2.0.11" || cfg.Mem0Revision != strings.Repeat("b", 40) ||
		!cfg.Mem0ObservationPromptVerified {
		t.Fatalf("completed config = %+v", cfg)
	}
	if err := validateLMEPrerequisites(
		*cfg,
		[]scenarios.ScenarioType{scenarios.ScenarioMem0OSS},
	); err != nil {
		t.Fatalf("validateLMEPrerequisites() error = %v", err)
	}

	cfg.ModelName = "different-model"
	if err := completeLMEMem0Preflight(cfg); err == nil || !strings.Contains(err.Error(), "LLM model") {
		t.Fatalf("completeLMEMem0Preflight() error = %v, want model mismatch", err)
	}
}

func testLMEMem0PreflightDocument() map[string]any {
	return map[string]any{
		"status":      "ok",
		"service_url": "http://localhost:8888",
		"environment_lock": map[string]any{
			"sha256": strings.Repeat("a", 64),
		},
		"runtime": map[string]any{
			"source": map[string]any{
				"commit": strings.Repeat("b", 40),
			},
			"distribution": map[string]any{
				"version": "2.0.11",
			},
			"runtime": map[string]any{
				"llm_model":      "gpt-4o-mini",
				"embedder_model": "text-embedding-3-small",
			},
		},
		"capabilities": map[string]any{
			"bm25_scoring":       true,
			"configuration":      true,
			"entity_scoring":     true,
			"llm_generation":     true,
			"memory_create":      true,
			"memory_search":      true,
			"memory_delete":      true,
			"observation_prompt": true,
			"search_explain":     true,
		},
	}
}

func writeTestLMEMem0Preflight(t *testing.T, document map[string]any) string {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "preflight.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func setLastLMEEventTime(sess *session.Session, value time.Time) {
	sess.EventMu.Lock()
	defer sess.EventMu.Unlock()
	sess.Events[len(sess.Events)-1].Timestamp = value
}

func appendTestLMEReplayEvent(sess *session.Session, role model.Role, content string) {
	author := lmeSeedAgentName
	if role == model.RoleUser {
		author = "user"
	}
	evt := event.NewResponseEvent(
		"test-invocation",
		author,
		&model.Response{
			Choices: []model.Choice{{
				Message: model.Message{
					Role:    role,
					Content: content,
				},
			}},
		},
	)
	sess.Events = append(sess.Events, *evt)
}
