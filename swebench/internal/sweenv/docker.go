//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package sweenv

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const imagePrefix = "docker.io/swebench/sweb.eval.x86_64."

var unsafeContainerName = regexp.MustCompile(`[^a-zA-Z0-9_.-]+`)

// Commander makes Docker lifecycle behavior testable without a daemon.
type Commander interface {
	Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error)
}

type osCommander struct{}

func (osCommander) Run(ctx context.Context, env []string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	return cmd.CombinedOutput()
}

// DockerFactory creates official SWE-Bench per-instance containers.
type DockerFactory struct {
	Config                Config
	DockerHost            string
	CommandTimeout        time.Duration
	CaseTimeout           time.Duration
	Commander             Commander
	Labels                map[string]string
	EnableOfflineServices bool
	OfflineAssetsDir      string
	SanitizeGitHistory    bool
	HTTPBinCerts          *OfflineHTTPBinCerts
}

// ImageForInstance returns the official SWE-Bench image name.
func ImageForInstance(instanceID string) string {
	name := strings.ToLower(strings.ReplaceAll(instanceID, "__", "_1776_"))
	return imagePrefix + name + ":latest"
}

// Start launches a sleeping testbed container rooted at /testbed.
func (f DockerFactory) Start(ctx context.Context, instanceID string) (Environment, error) {
	commander := f.Commander
	if commander == nil {
		commander = osCommander{}
	}
	caseTimeout := f.CaseTimeout
	if caseTimeout <= 0 {
		caseTimeout = 2 * time.Hour
	}
	name := unsafeContainerName.ReplaceAllString("tag-swe-"+instanceID, "-")
	suffix := "-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	maxBase := 63 - len(suffix)
	if len(name) > maxBase {
		name = name[:maxBase]
	}
	name += suffix
	args := []string{
		"run", "-d", "--rm",
		"--pull=never",
		"--network=none",
		"--name", name,
	}
	useHTTPBin := f.EnableOfflineServices && usesOfflineHTTPBin(instanceID)
	if useHTTPBin {
		args = append(args, "--add-host", offlineHTTPBinHost+":127.0.0.1")
	}
	if useHTTPBin && usesOfflineTarpit(instanceID) {
		args = append(
			args,
			"--cap-add=NET_ADMIN",
			"--device=/dev/net/tun:/dev/net/tun",
		)
	}
	labelKeys := make([]string, 0, len(f.Labels))
	for key := range f.Labels {
		labelKeys = append(labelKeys, key)
	}
	sort.Strings(labelKeys)
	for _, key := range labelKeys {
		args = append(args, "--label", key+"="+f.Labels[key])
	}
	args = append(args, "-w", "/testbed", ImageForInstance(instanceID), "sleep", strconv.Itoa(int(caseTimeout.Seconds())+60))
	out, err := commander.Run(ctx, dockerEnv(f.DockerHost), "docker", args...)
	if err != nil {
		return nil, fmt.Errorf("start Docker testbed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	environment := &dockerEnvironment{
		name:           name,
		config:         f.Config,
		dockerHost:     f.DockerHost,
		commandTimeout: f.CommandTimeout,
		commander:      commander,
	}
	if f.SanitizeGitHistory {
		if err := f.sanitizeGitRepository(ctx, environment); err != nil {
			_ = environment.Close(context.Background())
			return nil, err
		}
	}
	if useHTTPBin {
		if err := f.prepareOfflineRequestAssets(ctx, environment, instanceID); err != nil {
			_ = environment.Close(context.Background())
			return nil, err
		}
		if err := f.startOfflineHTTPBin(ctx, environment); err != nil {
			_ = environment.Close(context.Background())
			return nil, err
		}
	}
	return environment, nil
}

type dockerEnvironment struct {
	name           string
	config         Config
	dockerHost     string
	commandTimeout time.Duration
	commander      Commander
	sidecars       []string
	extraEnv       map[string]string
}

func (e *dockerEnvironment) Execute(ctx context.Context, command string) CommandResult {
	timeout := e.commandTimeout
	if timeout <= 0 {
		timeout = time.Minute
	}
	commandCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{"exec", "-w", "/testbed"}
	environment := make(map[string]string, len(e.config.Environment.Env)+len(e.extraEnv))
	for key, value := range e.config.Environment.Env {
		environment[key] = value
	}
	for key, value := range e.extraEnv {
		environment[key] = value
	}
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := environment[key]
		args = append(args, "-e", key+"="+value)
	}
	args = append(args, e.name)
	args = append(args, e.config.Environment.Interpreter...)
	args = append(args, command)
	out, err := e.commander.Run(commandCtx, dockerEnv(e.dockerHost), "docker", args...)
	result := CommandResult{Output: strings.ToValidUTF8(string(out), "�")}
	if err == nil {
		return result
	}
	if errors.Is(commandCtx.Err(), context.DeadlineExceeded) {
		result.ReturnCode = -1
		result.ExceptionInfo = fmt.Sprintf("An error occurred while executing the command: command timed out after %s", timeout)
		result.TimedOut = true
		return result
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ReturnCode = exitErr.ExitCode()
		return result
	}
	result.ReturnCode = 1
	result.ExceptionInfo = err.Error()
	return result
}

func (e *dockerEnvironment) SnapshotWorkspace(ctx context.Context, destination string) error {
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return fmt.Errorf("create workspace snapshot destination: %w", err)
	}
	out, err := e.commander.Run(
		ctx,
		dockerEnv(e.dockerHost),
		"docker",
		"cp",
		e.name+":/testbed/.",
		destination,
	)
	if err != nil {
		return fmt.Errorf("snapshot Docker testbed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func (e *dockerEnvironment) Close(ctx context.Context) error {
	containers := append(append([]string(nil), e.sidecars...), e.name)
	var closeErrs []error
	for _, name := range containers {
		out, err := e.commander.Run(ctx, dockerEnv(e.dockerHost), "docker", "rm", "-f", name)
		if err != nil {
			closeErrs = append(closeErrs, fmt.Errorf("remove Docker container %s: %w: %s", name, err, strings.TrimSpace(string(out))))
		}
	}
	return errors.Join(closeErrs...)
}

func dockerEnv(host string) []string {
	if strings.TrimSpace(host) == "" {
		return nil
	}
	return []string{"DOCKER_HOST=" + host}
}
