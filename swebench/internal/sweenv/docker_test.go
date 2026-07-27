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
	f.commands = append(f.commands, recordedCommand{env: env, name: name, args: append([]string(nil), args...)})
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

func TestImageForInstance(t *testing.T) {
	got := ImageForInstance("Astropy__Astropy-12907")
	want := "docker.io/swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest"
	if got != want {
		t.Fatalf("ImageForInstance() = %q, want %q", got, want)
	}
}

func TestDockerFactoryLifecycle(t *testing.T) {
	commander := &fakeCommander{}
	var cfg Config
	cfg.Environment.Env = map[string]string{"OMP_NUM_THREADS": "1"}
	cfg.Environment.Interpreter = []string{"bash", "-lc", `eval "$@"`, "tag-swebench-command"}
	factory := DockerFactory{
		Config: cfg, DockerHost: "unix:///tmp/docker.sock", CommandTimeout: time.Minute,
		CaseTimeout: time.Hour, Commander: commander, Labels: map[string]string{"tag-swebench.run_id": "run-1"},
	}
	environment, err := factory.Start(context.Background(), "repo__repo-1")
	if err != nil {
		t.Fatal(err)
	}
	if result := environment.Execute(context.Background(), "pwd"); result.ReturnCode != 0 || result.Output != "ok" {
		t.Fatalf("Execute() = %+v", result)
	}
	snapshotter, ok := environment.(WorkspaceSnapshotter)
	if !ok {
		t.Fatal("Docker environment does not implement WorkspaceSnapshotter")
	}
	if err := snapshotter.SnapshotWorkspace(context.Background(), t.TempDir()); err != nil {
		t.Fatal(err)
	}
	if err := environment.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 4 {
		t.Fatalf("commands = %d, want 4", len(commander.commands))
	}
	start := strings.Join(commander.commands[0].args, " ")
	if !strings.Contains(start, "--pull=never") || !strings.Contains(start, "--network=none") ||
		!strings.Contains(start, "--label tag-swebench.run_id=run-1") || !strings.Contains(start, "-w /testbed") ||
		!strings.Contains(start, ImageForInstance("repo__repo-1")) {
		t.Fatalf("start command = %q", start)
	}
	execute := strings.Join(commander.commands[1].args, " ")
	if !strings.Contains(execute, "-e OMP_NUM_THREADS=1") || !strings.HasSuffix(execute, "tag-swebench-command pwd") {
		t.Fatalf("execute command = %q", execute)
	}
	snapshot := strings.Join(commander.commands[2].args, " ")
	if !strings.Contains(snapshot, "cp tag-swe-repo__repo-1") || !strings.Contains(snapshot, ":/testbed/.") {
		t.Fatalf("snapshot command = %q", snapshot)
	}
	if got := commander.commands[0].env; len(got) != 1 || got[0] != "DOCKER_HOST=unix:///tmp/docker.sock" {
		t.Fatalf("docker env = %#v", got)
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
	if result.ReturnCode != -1 || !result.TimedOut || result.Output != "partial" || !strings.HasPrefix(result.ExceptionInfo, "An error occurred while executing the command:") {
		t.Fatalf("result = %#v", result)
	}
}
