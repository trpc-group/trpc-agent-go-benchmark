//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package environment

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

type fakeCommander struct {
	commands []recordedCommand
}

func (f *fakeCommander) Run(_ context.Context, env []string, name string, args ...string) ([]byte, error) {
	f.commands = append(f.commands, recordedCommand{env: env, name: name, args: append([]string(nil), args...)})
	if len(args) > 0 && args[0] == "exec" {
		return []byte("ok"), nil
	}
	return []byte("container-id"), nil
}

func TestImageForInstance(t *testing.T) {
	got := ImageForInstance("Astropy__Astropy-12907")
	want := "docker.io/swebench/sweb.eval.x86_64.astropy_1776_astropy-12907:latest"
	if got != want {
		t.Fatalf("ImageForInstance() = %q, want %q", got, want)
	}
}

func TestDockerFactoryLifecycleBuildsMiniCompatibleCommands(t *testing.T) {
	commander := &fakeCommander{}
	var cfg Config
	cfg.Environment.Env = map[string]string{"OMP_NUM_THREADS": "1"}
	cfg.Environment.Interpreter = []string{"bash", "-lc", `eval "$@"`, "mini-swe-agent-command"}
	factory := DockerFactory{
		Config:         cfg,
		DockerHost:     "unix:///tmp/docker.sock",
		CommandTimeout: time.Minute,
		CaseTimeout:    time.Hour,
		Commander:      commander,
		Labels:         map[string]string{"trpc-agent-go.run_id": "run-1"},
	}
	env, err := factory.Start(context.Background(), "repo__repo-1")
	if err != nil {
		t.Fatal(err)
	}
	result := env.Execute(context.Background(), "pwd")
	if result.ReturnCode != 0 || result.Output != "ok" {
		t.Fatalf("Execute() = %+v", result)
	}
	if err := env.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 3 {
		t.Fatalf("commands = %d, want 3", len(commander.commands))
	}
	start := strings.Join(commander.commands[0].args, " ")
	if !strings.Contains(start, "--label trpc-agent-go.run_id=run-1") || !strings.Contains(start, "-w /testbed") || !strings.Contains(start, ImageForInstance("repo__repo-1")) {
		t.Fatalf("start command = %q", start)
	}
	execute := strings.Join(commander.commands[1].args, " ")
	if !strings.Contains(execute, "-e OMP_NUM_THREADS=1") || !strings.HasSuffix(execute, "mini-swe-agent-command pwd") {
		t.Fatalf("execute command = %q", execute)
	}
	if got := commander.commands[0].env; len(got) != 1 || got[0] != "DOCKER_HOST=unix:///tmp/docker.sock" {
		t.Fatalf("docker env = %#v", got)
	}
}
