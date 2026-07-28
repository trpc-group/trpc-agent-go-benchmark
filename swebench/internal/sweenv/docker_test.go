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
	"os"
	"path/filepath"
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

func TestDockerFactoryStartsOfflineHTTPBinForRequests(t *testing.T) {
	certDir := t.TempDir()
	certs := OfflineHTTPBinCerts{
		CABundle:   filepath.Join(certDir, "ca.pem"),
		ServerCert: filepath.Join(certDir, "server.crt"),
		ServerKey:  filepath.Join(certDir, "server.key"),
	}
	for _, path := range []string{certs.CABundle, certs.ServerCert, certs.ServerKey} {
		if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	assetDir := t.TempDir()
	for path, contents := range map[string]string{
		filepath.Join(assetDir, offlineTarpitBinary):                       "binary",
		filepath.Join(assetDir, "requests-modern", "requirements.txt"):     "pytest==6.2.5\n",
		filepath.Join(assetDir, "requests-modern", "wheels", "pytest.whl"): "wheel",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	commander := &fakeCommander{}
	var cfg Config
	cfg.Environment.Env = map[string]string{"HTTPBIN_URL": "https://public.invalid"}
	cfg.Environment.Interpreter = []string{"bash", "-lc"}
	factory := DockerFactory{
		Config: cfg, CommandTimeout: time.Minute, CaseTimeout: time.Hour,
		Commander: commander, EnableOfflineServices: true, OfflineAssetsDir: assetDir,
		HTTPBinCerts: &certs,
		Labels:       map[string]string{"tag-swebench.run_id": "run-requests"},
	}
	environment, err := factory.Start(context.Background(), "psf__requests-6028")
	if err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 14 {
		t.Fatalf("startup commands = %d, want 14", len(commander.commands))
	}
	testbedStart := strings.Join(commander.commands[0].args, " ")
	if !strings.Contains(testbedStart, "--network=none") ||
		!strings.Contains(testbedStart, "--add-host httpbin.org:127.0.0.1") ||
		!strings.Contains(testbedStart, "--cap-add=NET_ADMIN") ||
		!strings.Contains(testbedStart, "--device=/dev/net/tun:/dev/net/tun") {
		t.Fatalf("testbed start command = %q", testbedStart)
	}
	dependencyInstall := strings.Join(commander.commands[3].args, " ")
	if !strings.Contains(dependencyInstall, "pip install") ||
		!strings.Contains(dependencyInstall, "--no-index") ||
		!strings.Contains(dependencyInstall, "requests-modern") {
		t.Fatalf("dependency install command = %q", dependencyInstall)
	}
	tarpitStart := strings.Join(commander.commands[5].args, " ")
	if !strings.Contains(tarpitStart, "exec -d") ||
		!strings.Contains(tarpitStart, offlineTarpitContainerPath) {
		t.Fatalf("tarpit start command = %q", tarpitStart)
	}
	sidecarStart := strings.Join(commander.commands[8].args, " ")
	if !strings.Contains(sidecarStart, "--pull=never") ||
		!strings.Contains(sidecarStart, "--network=container:tag-swe-psf__requests-6028") ||
		!strings.Contains(sidecarStart, "kennethreitz/httpbin") {
		t.Fatalf("sidecar start command = %q", sidecarStart)
	}
	launch := strings.Join(commander.commands[11].args, " ")
	if !strings.Contains(launch, "exec -d") || !strings.Contains(launch, "127.0.0.1:80") ||
		!strings.Contains(launch, "127.0.0.1:443") {
		t.Fatalf("sidecar launch command = %q", launch)
	}
	if result := environment.Execute(context.Background(), "pytest -q"); result.ReturnCode != 0 {
		t.Fatalf("Execute() = %+v", result)
	}
	execute := strings.Join(commander.commands[14].args, " ")
	for _, expected := range []string{
		"-e CURL_CA_BUNDLE=" + offlineHTTPBinCACertPath,
		"-e HTTPBIN_URL=http://httpbin.org",
		"-e NO_PROXY=localhost,127.0.0.1,httpbin.org,10.255.255.1",
		"-e PIP_FIND_LINKS=" + offlineWheelhouseRoot + "/requests-modern/wheels",
		"-e PIP_NO_INDEX=1",
		"-e REQUESTS_CA_BUNDLE=" + offlineHTTPBinCACertPath,
		"-e SSL_CERT_FILE=" + offlineHTTPBinCACertPath,
		"-e no_proxy=localhost,127.0.0.1,httpbin.org,10.255.255.1",
	} {
		if !strings.Contains(execute, expected) {
			t.Fatalf("execute command %q does not contain %q", execute, expected)
		}
	}
	if strings.Contains(execute, "https://public.invalid") {
		t.Fatalf("offline service environment was overridden: %q", execute)
	}
	if err := environment.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 17 {
		t.Fatalf("lifecycle commands = %d, want 17", len(commander.commands))
	}
	removeSidecar := strings.Join(commander.commands[15].args, " ")
	removeTestbed := strings.Join(commander.commands[16].args, " ")
	if !strings.Contains(removeSidecar, "rm -f") || !strings.Contains(removeSidecar, "-httpbin") {
		t.Fatalf("remove sidecar command = %q", removeSidecar)
	}
	if !strings.Contains(removeTestbed, "rm -f tag-swe-psf__requests-6028") {
		t.Fatalf("remove testbed command = %q", removeTestbed)
	}
}

func TestGenerateOfflineHTTPBinCerts(t *testing.T) {
	certs, err := generateOfflineHTTPBinCerts(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := validateOfflineHTTPBinCerts(certs); err != nil {
		t.Fatal(err)
	}
	caData, err := os.ReadFile(certs.CABundle)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(caData), "BEGIN CERTIFICATE") {
		t.Fatalf("CA bundle is not PEM: %q", caData)
	}
}
