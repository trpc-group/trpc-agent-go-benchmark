//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package retrievalreplay

import (
	"slices"
	"testing"
)

func TestParsePatchTargets(t *testing.T) {
	patch := `diff --git a/pkg/service.py b/pkg/service.py
--- a/pkg/service.py
+++ b/pkg/service.py
@@ -1,4 +1,4 @@
 class Service:
-    def old(self):
+    def new(self):
         return 1
diff --git a/pkg/deleted.py b/pkg/deleted.py
--- a/pkg/deleted.py
+++ /dev/null
@@ -1 +0,0 @@
-obsolete = True
diff --git a/pkg/new.py b/pkg/new.py
--- /dev/null
+++ b/pkg/new.py
@@ -0,0 +1 @@
+created = True
`
	targets, err := parsePatchTargets(patch)
	if err != nil {
		t.Fatalf("parsePatchTargets() error = %v", err)
	}
	if !slices.Equal(targets.TargetFiles, []string{"pkg/deleted.py", "pkg/service.py"}) {
		t.Fatalf("target files = %v", targets.TargetFiles)
	}
	if !slices.Equal(targets.NewFiles, []string{"pkg/new.py"}) {
		t.Fatalf("new files = %v", targets.NewFiles)
	}
	if len(targets.Anchors) != 2 {
		t.Fatalf("anchors = %#v, want one per base-side hunk", targets.Anchors)
	}
	if targets.Anchors[1].Text != "class Service: def old(self): return 1" {
		t.Fatalf("service anchor = %q", targets.Anchors[1].Text)
	}
}

func TestNormalizePatchPath(t *testing.T) {
	for input, want := range map[string]string{
		"a/pkg/a.py":       "pkg/a.py",
		"b/pkg/a.py\tdate": "pkg/a.py",
		"   ":              "",
		"/dev/null":        "",
		"/dev/null\tdate":  "",
	} {
		if got := normalizePatchPath(input); got != want {
			t.Errorf("normalizePatchPath(%q) = %q, want %q", input, got, want)
		}
	}
}
