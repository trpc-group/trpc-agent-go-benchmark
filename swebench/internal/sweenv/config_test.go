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
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	data := []byte("environment:\n  env:\n    Z_VAR: last\n    A_VAR: \"first\"\n  interpreter:\n    - bash\n    - -lc\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Environment.Env, map[string]string{"A_VAR": "first", "Z_VAR": "last"}) {
		t.Fatalf("environment = %#v", cfg.Environment.Env)
	}
	if !reflect.DeepEqual(cfg.Environment.Interpreter, []string{"bash", "-lc"}) {
		t.Fatalf("interpreter = %#v", cfg.Environment.Interpreter)
	}
}

func TestLoadConfigRequiresInterpreter(t *testing.T) {
	path := filepath.Join(t.TempDir(), "environment.yaml")
	if err := os.WriteFile(path, []byte("environment:\n  env: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadConfig(path); err == nil {
		t.Fatal("LoadConfig() accepted a config without an interpreter")
	}
}
