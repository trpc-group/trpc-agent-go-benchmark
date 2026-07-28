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
	"strings"
	"testing"
	"time"
)

func TestDockerFactoryOfflineHTTPBinIntegration(t *testing.T) {
	if os.Getenv("TAG_SWEBENCH_DOCKER_INTEGRATION") != "1" {
		t.Skip("set TAG_SWEBENCH_DOCKER_INTEGRATION=1 to run Docker integration test")
	}
	offlineAssetsDir := os.Getenv("TAG_SWEBENCH_OFFLINE_ASSETS")
	cases := []struct {
		instanceID string
		imports    string
	}{
		{instanceID: "psf__requests-1142"},
	}
	if offlineAssetsDir != "" {
		cases = []struct {
			instanceID string
			imports    string
		}{
			{instanceID: "psf__requests-2931", imports: "import pytest_httpbin"},
			{instanceID: "psf__requests-6028", imports: "import pytest_httpbin, trustme, socks"},
		}
		instanceIDs := make([]string, 0, len(cases))
		for _, integrationCase := range cases {
			instanceIDs = append(instanceIDs, integrationCase.instanceID)
		}
		if err := ValidateOfflineAssets(offlineAssetsDir, instanceIDs); err != nil {
			t.Fatal(err)
		}
	}
	for _, integrationCase := range cases {
		t.Run(integrationCase.instanceID, func(t *testing.T) {
			testDockerFactoryOfflineHTTPBinIntegration(
				t,
				integrationCase.instanceID,
				integrationCase.imports,
				offlineAssetsDir,
			)
		})
	}
}

func testDockerFactoryOfflineHTTPBinIntegration(
	t *testing.T,
	instanceID string,
	imports string,
	offlineAssetsDir string,
) {
	t.Helper()
	var cfg Config
	cfg.Environment.Interpreter = []string{
		"bash", "-lc", `source /opt/miniconda3/bin/activate testbed && eval "$@"`, "tag-swebench-command",
	}
	factory := DockerFactory{
		Config: cfg, CommandTimeout: 15 * time.Second, CaseTimeout: 2 * time.Minute,
		Labels:                map[string]string{"tag-swebench.integration": "offline-httpbin"},
		EnableOfflineServices: true, OfflineAssetsDir: offlineAssetsDir,
	}
	environment, err := factory.Start(context.Background(), instanceID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := environment.Close(cleanupCtx); err != nil {
			t.Errorf("close Docker environment: %v", err)
		}
	}()
	for _, command := range []string{
		"curl -fsS --connect-timeout 5 http://httpbin.org/get",
		"curl -fsS --connect-timeout 5 https://httpbin.org/get",
	} {
		result := environment.Execute(context.Background(), command)
		if result.ReturnCode != 0 || !strings.Contains(result.Output, "httpbin.org/get") {
			t.Fatalf("%s: %+v", command, result)
		}
	}
	if imports != "" {
		for _, command := range []string{
			"python -c '" + imports + "'",
			"python - <<'PY'\nimport requests, time\nstarted = time.monotonic()\ntry:\n    requests.get('http://10.255.255.1', timeout=(0.2, None))\nexcept requests.exceptions.ConnectTimeout:\n    assert time.monotonic() - started >= 0.15\nelse:\n    raise AssertionError('tarpit unexpectedly connected')\nPY",
		} {
			result := environment.Execute(context.Background(), command)
			if result.ReturnCode != 0 {
				t.Fatalf("%s: %+v", command, result)
			}
		}
	}
	egress := environment.Execute(
		context.Background(),
		"curl -kfsS --connect-timeout 2 https://1.1.1.1",
	)
	if egress.ReturnCode == 0 {
		t.Fatalf("isolated testbed unexpectedly reached the public internet: %+v", egress)
	}
}
