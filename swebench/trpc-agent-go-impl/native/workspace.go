//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package native

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"trpc.group/trpc-go/trpc-agent-go-benchmark/swebench/trpc-agent-go-impl/dataset"
)

const (
	nativeEnvDockerTestbed = "docker-testbed"
	nativeEnvLocalClone    = "local-clone"
	testbedCWD             = "/testbed"
)

type workspace struct {
	Dir           string
	ContainerName string
	Image         string
	DockerHost    string
}

type commandResult struct {
	Command         string `json:"command"`
	Output          string `json:"output"`
	ExitCode        int    `json:"exit_code"`
	DurationMs      int64  `json:"duration_ms"`
	Rejected        bool   `json:"rejected,omitempty"`
	OutputTruncated bool   `json:"output_truncated,omitempty"`
	OutputBytes     int    `json:"output_bytes,omitempty"`
}

func prepareWorkspace(ctx context.Context, root string, inst dataset.Instance, req RunRequest) (workspace, error) {
	if nativeEnvironment(req.Environment) == nativeEnvDockerTestbed {
		return prepareDockerWorkspace(ctx, inst, req)
	}
	return prepareLocalWorkspace(ctx, root, inst)
}

func nativeEnvironment(env string) string {
	env = strings.TrimSpace(env)
	if env == "" {
		return nativeEnvDockerTestbed
	}
	return env
}

func prepareLocalWorkspace(ctx context.Context, root string, inst dataset.Instance) (workspace, error) {
	if inst.Repo == "" {
		return workspace{}, fmt.Errorf("instance %s missing repo", inst.InstanceID)
	}
	if inst.BaseCommit == "" {
		return workspace{}, fmt.Errorf("instance %s missing base_commit", inst.InstanceID)
	}
	dir := filepath.Join(root, sanitize(inst.InstanceID))
	if err := os.RemoveAll(dir); err != nil {
		return workspace{}, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0755); err != nil {
		return workspace{}, err
	}
	repoURL := inst.Repo
	if !strings.HasPrefix(repoURL, "http://") && !strings.HasPrefix(repoURL, "https://") && !strings.HasPrefix(repoURL, "git@") {
		repoURL = "https://github.com/" + strings.TrimSuffix(repoURL, ".git") + ".git"
	}
	if out, err := runProgram(ctx, "", 15*time.Minute, 2000, "git", "clone", repoURL, dir); err != nil {
		return workspace{}, fmt.Errorf("git clone failed: %w: %s", err, out.Output)
	}
	if out, err := runProgram(ctx, dir, 5*time.Minute, 2000, "git", "checkout", inst.BaseCommit); err != nil {
		return workspace{}, fmt.Errorf("git checkout failed: %w: %s", err, out.Output)
	}
	return workspace{Dir: dir}, nil
}

func prepareDockerWorkspace(ctx context.Context, inst dataset.Instance, req RunRequest) (workspace, error) {
	image := swebenchImage(inst)
	if image == "" {
		return workspace{}, fmt.Errorf("instance %s missing instance_id", inst.InstanceID)
	}
	name := "trpc-native-" + sanitize(inst.InstanceID) + "-" + fmt.Sprint(time.Now().UnixNano())
	args := []string{
		"run", "-d",
		"--name", name,
		"-w", testbedCWD,
		"--rm",
		"-e", "PAGER=cat",
		"-e", "MANPAGER=cat",
		"-e", "LESS=-R",
		"-e", "PIP_PROGRESS_BAR=off",
		"-e", "TQDM_DISABLE=1",
		"-e", "BASH_ENV=/root/.bashrc",
		"-e", "OPENBLAS_NUM_THREADS=1",
		"-e", "OMP_NUM_THREADS=1",
		"-e", "MKL_NUM_THREADS=1",
		"-e", "NUMEXPR_NUM_THREADS=1",
		"-e", "GIT_CONFIG_COUNT=2",
		"-e", "GIT_CONFIG_KEY_0=core.preloadIndex",
		"-e", "GIT_CONFIG_VALUE_0=false",
		"-e", "GIT_CONFIG_KEY_1=core.fscache",
		"-e", "GIT_CONFIG_VALUE_1=false",
		image,
		"sleep", "2h",
	}
	out, err := runDockerProgram(ctx, req.DockerHost, 15*time.Minute, 16*1024, args...)
	if err != nil {
		return workspace{}, fmt.Errorf("start testbed container %s: %w: %s", image, err, out.Output)
	}
	ws := workspace{
		Dir:           testbedCWD,
		ContainerName: name,
		Image:         image,
		DockerHost:    req.DockerHost,
	}
	// Some images require safe.directory before git commands work under Docker.
	_ = ws.exec(ctx, "git config --global --add safe.directory /testbed", 30*time.Second, 2048)
	return ws, nil
}

func (w workspace) exec(ctx context.Context, command string, timeout time.Duration, maxOutput int) commandResult {
	start := time.Now()
	if reason := unsafeCommandReason(command); reason != "" {
		return commandResult{
			Command:    command,
			Output:     "command rejected: " + reason,
			ExitCode:   126,
			DurationMs: time.Since(start).Milliseconds(),
			Rejected:   true,
		}
	}
	var out commandResult
	var err error
	if w.ContainerName != "" {
		out, err = runDockerProgram(ctx, w.DockerHost, timeout, maxOutput, "exec", w.ContainerName, "bash", "-lc", command)
		out.Command = command
	} else {
		out, err = runProgram(ctx, w.Dir, timeout, maxOutput, "bash", "-lc", command)
	}
	if err != nil && out.ExitCode == 0 {
		out.ExitCode = 1
	}
	return out
}

func (w workspace) diff(ctx context.Context) (string, error) {
	out := w.exec(ctx, "git diff --binary", 2*time.Minute, 32*1024*1024)
	var err error
	if out.ExitCode != 0 {
		err = fmt.Errorf("git diff exited with %d", out.ExitCode)
	}
	return out.Output, err
}

func (w workspace) changedFiles(ctx context.Context) ([]string, error) {
	out := w.exec(ctx, "git diff --name-only", 2*time.Minute, 2*1024*1024)
	if out.ExitCode != 0 {
		return nil, fmt.Errorf("git diff --name-only exited with %d: %s", out.ExitCode, out.Output)
	}
	lines := strings.Split(out.Output, "\n")
	files := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}
	return files, nil
}

func (w workspace) revertFiles(ctx context.Context, files []string) error {
	if len(files) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(files))
	for _, file := range files {
		quoted = append(quoted, shellQuote(file))
	}
	out := w.exec(ctx, "git checkout -- "+strings.Join(quoted, " "), 2*time.Minute, 2*1024*1024)
	if out.ExitCode != 0 {
		return fmt.Errorf("revert forbidden files failed: %s", out.Output)
	}
	return nil
}

func (w workspace) cleanup(ctx context.Context) error {
	if w.ContainerName != "" {
		out, err := runDockerProgram(ctx, w.DockerHost, 2*time.Minute, 4096, "stop", w.ContainerName)
		if err != nil {
			return fmt.Errorf("stop container %s: %w: %s", w.ContainerName, err, out.Output)
		}
		return nil
	}
	return os.RemoveAll(w.Dir)
}

func runProgram(ctx context.Context, dir string, timeout time.Duration, maxOutput int, name string, args ...string) (commandResult, error) {
	start := time.Now()
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, name, args...)
	if dir != "" {
		cmd.Dir = dir
	}
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	if cmdCtx.Err() == context.DeadlineExceeded {
		err = cmdCtx.Err()
		exitCode = 124
	}
	limited, truncated, bytes := limitOutput(string(output), maxOutput)
	return commandResult{
		Command:         strings.Join(append([]string{name}, args...), " "),
		Output:          limited,
		ExitCode:        exitCode,
		DurationMs:      time.Since(start).Milliseconds(),
		OutputTruncated: truncated,
		OutputBytes:     bytes,
	}, err
}

func runDockerProgram(ctx context.Context, dockerHost string, timeout time.Duration, maxOutput int, args ...string) (commandResult, error) {
	start := time.Now()
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "docker", args...)
	cmd.Env = os.Environ()
	if dockerHost != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+dockerHost)
	}
	output, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		exitCode = 1
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	if cmdCtx.Err() == context.DeadlineExceeded {
		err = cmdCtx.Err()
		exitCode = 124
	}
	limited, truncated, bytes := limitOutput(string(output), maxOutput)
	return commandResult{
		Command:         "docker " + strings.Join(args, " "),
		Output:          limited,
		ExitCode:        exitCode,
		DurationMs:      time.Since(start).Milliseconds(),
		OutputTruncated: truncated,
		OutputBytes:     bytes,
	}, err
}

func swebenchImage(inst dataset.Instance) string {
	id := strings.TrimSpace(inst.InstanceID)
	if id == "" {
		return ""
	}
	namespace := strings.ReplaceAll(id, "__", "_1776_")
	return "docker.io/swebench/sweb.eval.x86_64." + namespace + ":latest"
}

func unsafeCommandReason(command string) string {
	trimmed := strings.TrimSpace(strings.ToLower(command))
	switch {
	case strings.Contains(trimmed, "rm -rf /"):
		return "refuses rm -rf /"
	case strings.Contains(trimmed, "sudo "):
		return "refuses sudo"
	case strings.Contains(trimmed, "mkfs"):
		return "refuses mkfs"
	case strings.Contains(trimmed, "shutdown"):
		return "refuses shutdown"
	case strings.Contains(trimmed, "reboot"):
		return "refuses reboot"
	default:
		return ""
	}
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func limitOutput(s string, max int) (string, bool, int) {
	size := len(s)
	if max <= 0 || len(s) <= max {
		return s, false, size
	}
	head := max / 2
	tail := max - head
	return s[:head] + "\n... output truncated ...\n" + s[len(s)-tail:], true, size
}
