//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package main

import (
	"fmt"
	"os"
	"os/exec"
)

func runDoctor(cfg cliConfig) error {
	checks := []doctorCheck{
		checkFile("dataset", cfg.DatasetPath, false),
		checkCommand("git", "git", true),
		checkCommand("docker", "docker", false),
		checkCommand("sb-cli", cfg.SBCLIBin, false),
		checkEnv("DOCKER_HOST", cfg.DockerHost, false),
		checkEnv("GLM5_API_BASE", cfg.GLMAPIBase, true),
		checkEnv("GLM5_API_KEY", cfg.GLMAPIKey, true),
		checkEnv("GLM5_ROUTING_KEY", cfg.GLMRoutingKey, false),
		checkEnv("GLM5_AGENT_NAME", cfg.GLMAgentName, false),
	}
	failedRequired := false
	for _, check := range checks {
		ok, detail := check.run()
		status := "ok"
		if !ok {
			if check.required {
				status = "missing"
				failedRequired = true
			} else {
				status = "optional-missing"
			}
		}
		fmt.Printf("%-18s %-16s %s\n", check.name, status, detail)
	}
	if failedRequired {
		return fmt.Errorf("required checks failed")
	}
	return nil
}

type doctorCheck struct {
	name     string
	required bool
	run      func() (bool, string)
}

func checkFile(name, path string, required bool) doctorCheck {
	return doctorCheck{
		name:     name,
		required: required,
		run: func() (bool, string) {
			if path == "" {
				return false, "empty path"
			}
			info, err := os.Stat(path)
			if err != nil {
				return false, path
			}
			if info.IsDir() {
				return false, path + " is a directory"
			}
			return true, path
		},
	}
}

func checkCommand(name, bin string, required bool) doctorCheck {
	return doctorCheck{
		name:     name,
		required: required,
		run: func() (bool, string) {
			if bin == "" {
				return false, "empty command"
			}
			path, err := exec.LookPath(bin)
			if err != nil {
				return false, bin
			}
			return true, path
		},
	}
}

func checkEnv(name, override string, required bool) doctorCheck {
	return doctorCheck{
		name:     name,
		required: required,
		run: func() (bool, string) {
			if override != "" {
				return true, "provided by flag"
			}
			if os.Getenv(name) != "" {
				return true, "provided by env"
			}
			return false, "not set"
		},
	}
}
