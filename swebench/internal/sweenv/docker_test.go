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
	"strings"
	"testing"
	"time"
)

type recordedCommand struct {
	env  []string
	name string
	args []string
}

type fakeCommander struct{ commands []recordedCommand }

func (f *fakeCommander) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.commands = append(f.commands, recordedCommand{
		env: append([]string(nil), env...), name: name, args: append([]string(nil), args...),
	})
	if len(args) > 0 && args[0] == "exec" {
		return []byte("ok"), nil
	}
	return []byte("container-id"), nil
}

type timeoutCommander struct{}

func (timeoutCommander) Run(ctx context.Context, _ []string, _ string, args ...string) ([]byte, error) {
	if len(args) == 0 || args[0] != "exec" {
		return []byte("container-id"), nil
	}
	<-ctx.Done()
	return []byte("partial"), ctx.Err()
}

type failingStartCommander struct {
	err error
}

func (c failingStartCommander) Run(_ context.Context, _ []string, _ string, args ...string) ([]byte, error) {
	if len(args) > 0 && args[0] == "run" {
		return []byte("daemon temporarily unavailable"), c.err
	}
	return nil, errors.New("unexpected command")
}

func TestImageForInstance(t *testing.T) {
	got := ImageForInstance("Astropy__Astropy-12907")
	want := "docker.io/swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest"
	if got != want {
		t.Fatalf("ImageForInstance() = %q, want %q", got, want)
	}
}

func TestDockerFactoryLifecycleAndDeterministicArguments(t *testing.T) {
	commander := &fakeCommander{}
	var cfg Config
	cfg.Environment.Env = map[string]string{"Z_VAR": "last", "A_VAR": "first"}
	cfg.Environment.Interpreter = []string{"bash", "-lc", `eval "$@"`, "swebench-command"}
	factory := DockerFactory{
		Config:              cfg,
		DockerHost:          "unix:///tmp/docker.sock",
		CommandTimeout:      time.Minute,
		CaseTimeout:         time.Hour,
		ContainerNamePrefix: "custom runner/",
		Commander:           commander,
		Labels:              map[string]string{"z.label": "2", "a.label": "1"},
	}
	environment, err := factory.Start(context.Background(), "repo__repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if result := environment.Execute(context.Background(), "pwd"); result.ReturnCode != 0 || result.Output != "ok" {
		t.Fatalf("Execute() = %+v", result)
	}
	if err := environment.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(commander.commands))
	}
	start := strings.Join(commander.commands[0].args, " ")
	if !strings.Contains(start, "--label a.label=1 --label z.label=2") ||
		!strings.Contains(start, "-w /testbed") ||
		!strings.Contains(start, ImageForInstance("repo__repo-1")) {
		t.Fatalf("start command = %q", start)
	}
	if !strings.Contains(commander.commands[0].args[4], "custom-runner-repo__repo-1-") {
		t.Fatalf("container name = %q", commander.commands[0].args[4])
	}
	execute := strings.Join(commander.commands[1].args, " ")
	if !strings.Contains(execute, "-e A_VAR=first -e Z_VAR=last") ||
		!strings.HasSuffix(execute, "swebench-command pwd") {
		t.Fatalf("execute command = %q", execute)
	}
	if got := commander.commands[0].env; len(got) != 1 || got[0] != "DOCKER_HOST=unix:///tmp/docker.sock" {
		t.Fatalf("docker env = %#v", got)
	}
}

func TestDockerFactoryUsesGenericContainerNamePrefixByDefault(t *testing.T) {
	commander := &fakeCommander{}
	var cfg Config
	cfg.Environment.Interpreter = []string{"bash", "-c"}
	factory := DockerFactory{Config: cfg, Commander: commander}
	if _, err := factory.Start(context.Background(), "repo__repo-1"); err != nil {
		t.Fatal(err)
	}
	name := commander.commands[0].args[4]
	if !strings.HasPrefix(name, defaultContainerNamePrefix+"repo__repo-1-") {
		t.Fatalf("container name = %q, want generic prefix %q", name, defaultContainerNamePrefix)
	}
	if len(name) > maxContainerNameLength {
		t.Fatalf("container name length = %d, want <= %d", len(name), maxContainerNameLength)
	}
}

func TestDockerFactoryMarksOnlyCleanRoomContainerStartFailureRetryable(t *testing.T) {
	want := errors.New("temporary Docker communication failure")
	instanceID := "repo__repo-1"
	reference := ImageForInstance(instanceID)
	factory := DockerFactory{
		CleanRoom:   true,
		Commander:   failingStartCommander{err: want},
		CaseTimeout: time.Hour,
		ResolvedImages: map[string]ImageIdentity{
			reference: {Reference: reference, ID: testImageID},
		},
	}
	_, err := factory.StartCase(context.Background(), CaseSpec{
		InstanceID: instanceID,
		Repo:       "repo/repo",
		BaseCommit: strings.Repeat("1", 40),
	})
	if err == nil || !IsStartErrorRetryable(err) || !errors.Is(err, want) {
		t.Fatalf("StartCase error = %v, retryable=%t", err, IsStartErrorRetryable(err))
	}
	if !strings.Contains(err.Error(), "start Docker testbed") ||
		!strings.Contains(err.Error(), "daemon temporarily unavailable") {
		t.Fatalf("StartCase error = %v", err)
	}

	online := DockerFactory{Commander: failingStartCommander{err: want}}
	_, err = online.StartCase(context.Background(), CaseSpec{InstanceID: instanceID})
	if err == nil || IsStartErrorRetryable(err) {
		t.Fatalf("online StartCase error = %v, retryable marker=%t", err, IsStartErrorRetryable(err))
	}
}

func TestDockerCommandTimeout(t *testing.T) {
	var cfg Config
	cfg.Environment.Interpreter = []string{"bash", "-c"}
	factory := DockerFactory{
		Config: cfg, CommandTimeout: time.Millisecond, CaseTimeout: time.Hour, Commander: timeoutCommander{},
	}
	environment, err := factory.Start(context.Background(), "repo__repo-1")
	if err != nil {
		t.Fatal(err)
	}
	result := environment.Execute(context.Background(), "sleep 10")
	if result.ReturnCode != -1 || !result.TimedOut || result.Output != "partial" ||
		!strings.HasPrefix(result.ExceptionInfo, "An error occurred while executing the command:") {
		t.Fatalf("result = %#v", result)
	}
}
