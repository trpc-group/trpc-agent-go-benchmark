//
// Tencent is pleased to support the open source community by making
// trpc-agent-go available.
//
// Copyright (C) 2025 Tencent.  All rights reserved.
//
// trpc-agent-go is licensed under the Apache License Version 2.0.
//

package artifact

import (
	"reflect"
	"testing"
)

func TestComputePatchStatsIncludesAddedDeletedAndRenamedFiles(t *testing.T) {
	patch := `diff --git a/added.go b/added.go
--- /dev/null
+++ b/added.go
+added
diff --git a/deleted.go b/deleted.go
--- a/deleted.go
+++ /dev/null
-deleted
diff --git a/old.go b/new.go
similarity index 100%
rename from old.go
rename to new.go
`
	got := ComputePatchStats(patch)
	wantFiles := []string{"added.go", "deleted.go", "new.go", "old.go"}
	if !reflect.DeepEqual(got.ChangedFiles, wantFiles) {
		t.Fatalf("ChangedFiles = %#v, want %#v", got.ChangedFiles, wantFiles)
	}
	if got.AddedLines != 1 || got.DeletedLines != 1 {
		t.Fatalf("line stats = +%d/-%d, want +1/-1", got.AddedLines, got.DeletedLines)
	}
}
