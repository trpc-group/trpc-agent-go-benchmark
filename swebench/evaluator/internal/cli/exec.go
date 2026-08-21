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
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type commandResult struct {
	Command    []string      `json:"command"`
	Env        []string      `json:"env,omitempty"`
	Dir        string        `json:"dir,omitempty"`
	StartedAt  time.Time     `json:"started_at"`
	FinishedAt time.Time     `json:"finished_at"`
	DurationMS int64         `json:"duration_ms"`
	ExitCode   int           `json:"exit_code"`
	Stdout     string        `json:"stdout,omitempty"`
	Stderr     string        `json:"stderr,omitempty"`
	Error      string        `json:"error,omitempty"`
	LogPath    string        `json:"log_path,omitempty"`
	Elapsed    time.Duration `json:"-"`
}

func runCapture(ctx context.Context, dir string, env []string, name string, args ...string) commandResult {
	start := time.Now()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), env)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	finish := time.Now()

	res := commandResult{
		Command:    append([]string{name}, args...),
		Env:        safeEnv(env),
		Dir:        dir,
		StartedAt:  start.UTC(),
		FinishedAt: finish.UTC(),
		DurationMS: finish.Sub(start).Milliseconds(),
		ExitCode:   exitCode(err),
		Stdout:     stdout.String(),
		Stderr:     stderr.String(),
		Elapsed:    finish.Sub(start),
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func runLogged(ctx context.Context, dir string, env []string, logPath string, name string, args ...string) commandResult {
	start := time.Now()
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	logFile, err := os.Create(logPath)
	if err != nil {
		return commandResult{
			Command:    append([]string{name}, args...),
			Env:        safeEnv(env),
			Dir:        dir,
			StartedAt:  start.UTC(),
			FinishedAt: time.Now().UTC(),
			ExitCode:   -1,
			Error:      fmt.Sprintf("create log: %v", err),
			LogPath:    logPath,
		}
	}
	defer logFile.Close()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), env)
	writer := io.MultiWriter(os.Stdout, logFile)
	cmd.Stdout = writer
	cmd.Stderr = writer

	_, _ = fmt.Fprintf(logFile, "command: %s\n", shellQuote(append([]string{name}, args...)))
	if len(env) > 0 {
		_, _ = fmt.Fprintf(logFile, "env: %s\n", strings.Join(safeEnv(env), " "))
	}
	_, _ = fmt.Fprintf(logFile, "started_at: %s\n\n", start.UTC().Format(time.RFC3339Nano))

	err = cmd.Run()
	finish := time.Now()
	_, _ = fmt.Fprintf(logFile, "\nfinished_at: %s\n", finish.UTC().Format(time.RFC3339Nano))
	_, _ = fmt.Fprintf(logFile, "duration: %s\n", finish.Sub(start).Round(time.Millisecond))
	if err != nil {
		_, _ = fmt.Fprintf(logFile, "error: %v\n", err)
	}

	res := commandResult{
		Command:    append([]string{name}, args...),
		Env:        safeEnv(env),
		Dir:        dir,
		StartedAt:  start.UTC(),
		FinishedAt: finish.UTC(),
		DurationMS: finish.Sub(start).Milliseconds(),
		ExitCode:   exitCode(err),
		LogPath:    logPath,
		Elapsed:    finish.Sub(start),
	}
	if err != nil {
		res.Error = err.Error()
	}
	return res
}

func exitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if ok := errorAs(err, &exitErr); ok {
		return exitErr.ExitCode()
	}
	return -1
}

func mergeEnv(base, extra []string) []string {
	values := make(map[string]string, len(base)+len(extra))
	order := make([]string, 0, len(base)+len(extra))
	add := func(kv string) {
		key, _, ok := strings.Cut(kv, "=")
		if !ok {
			return
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = kv
	}
	for _, kv := range base {
		add(kv)
	}
	for _, kv := range extra {
		add(kv)
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, values[key])
	}
	return out
}

func safeEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if isSecretKey(key) {
			val = "<redacted>"
		}
		out = append(out, key+"="+val)
	}
	return out
}

func shellQuote(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "" {
			quoted = append(quoted, "''")
			continue
		}
		if strings.IndexFunc(arg, func(r rune) bool {
			return !(r == '-' || r == '_' || r == '/' || r == '.' || r == ':' || r == '=' ||
				r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z')
		}) == -1 {
			quoted = append(quoted, arg)
			continue
		}
		quoted = append(quoted, "'"+strings.ReplaceAll(arg, "'", "'\"'\"'")+"'")
	}
	return strings.Join(quoted, " ")
}

func errorAs(err error, target any) bool {
	switch t := target.(type) {
	case **exec.ExitError:
		if e, ok := err.(*exec.ExitError); ok {
			*t = e
			return true
		}
	}
	return false
}
