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
	var cfg Config
	cfg.Environment.Interpreter = []string{"bash", "-lc"}
	factory := DockerFactory{
		Config:         cfg,
		CommandTimeout: 15 * time.Second,
		CaseTimeout:    2 * time.Minute,
		Labels:         map[string]string{"tag-swebench.integration": "offline-httpbin"},
	}
	environment, err := factory.Start(context.Background(), "psf__requests-6028")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := environment.Close(cleanupCtx); err != nil {
			t.Errorf("close Docker environment: %v", err)
		}
	})
	for _, command := range []string{
		"curl -fsS --connect-timeout 5 http://httpbin.org/get",
		"curl -fsS --connect-timeout 5 https://httpbin.org/get",
	} {
		result := environment.Execute(context.Background(), command)
		if result.ReturnCode != 0 || !strings.Contains(result.Output, "httpbin.org/get") {
			t.Fatalf("%s: %+v", command, result)
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
