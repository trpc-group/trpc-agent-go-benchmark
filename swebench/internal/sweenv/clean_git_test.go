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
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanRoomPolicySHA256CanonicalAndFailClosed(t *testing.T) {
	first, err := CleanRoomPolicySHA256([]string{"nested/", "exact.file"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := CleanRoomPolicySHA256([]string{"exact.file", "nested/"})
	if err != nil {
		t.Fatal(err)
	}
	if first != second || len(first) != 64 {
		t.Fatalf("policy hashes = %q and %q", first, second)
	}

	for _, values := range [][]string{
		{""}, {" whitespace "}, {"/absolute"}, {"../escape"}, {"a/../b"},
		{"back\\slash"}, {"line\nbreak"}, {"tab\tpath"},
		{"duplicate", "duplicate"}, {"directory/", "directory/"},
	} {
		if _, err := CleanRoomPolicySHA256(values); err == nil {
			t.Fatalf("CleanRoomPolicySHA256(%q) unexpectedly succeeded", values)
		}
	}
}

func TestCleanGitRepositoryScriptSanitizesNestedSubmodules(t *testing.T) {
	fixture := newNestedGitFixture(t)
	writeFile(t, filepath.Join(fixture.root, "allowed.secret"), "keep")
	writeFile(t, filepath.Join(fixture.root, "remove.secret"), "remove")
	middle := filepath.Join(fixture.root, "modules", "middle")
	leaf := filepath.Join(middle, "nested", "leaf")
	writeFile(t, filepath.Join(middle, "allowed.secret"), "keep")
	writeFile(t, filepath.Join(middle, "remove.secret"), "remove")
	writeFile(t, filepath.Join(leaf, "remove.secret"), "remove")
	writeGitObject(t, fixture.root, "unreachable-root")
	writeGitObject(t, middle, "unreachable-middle")
	writeGitObject(t, leaf, "unreachable-leaf")

	runCleanGitScript(t, fixture.root, fixture.base,
		"allowed.secret", "modules/middle/allowed.secret")

	if got := strings.TrimSpace(runGit(t, fixture.root, "rev-parse", "HEAD")); got != fixture.base {
		t.Fatalf("root HEAD = %s, want %s", got, fixture.base)
	}
	for _, file := range []string{
		filepath.Join(fixture.root, "allowed.secret"),
		filepath.Join(middle, "allowed.secret"),
	} {
		if _, err := os.Stat(file); err != nil {
			t.Fatalf("allowlisted file %s missing: %v", file, err)
		}
	}
	for _, file := range []string{
		filepath.Join(fixture.root, "remove.secret"),
		filepath.Join(middle, "remove.secret"),
		filepath.Join(leaf, "remove.secret"),
	} {
		if _, err := os.Lstat(file); !os.IsNotExist(err) {
			t.Fatalf("unapproved ignored file %s survived: %v", file, err)
		}
	}
	for _, repository := range []string{fixture.root, middle, leaf} {
		assertSanitizedGitRepository(t, repository)
	}
	runVerifyGitScript(t, fixture.root, fixture.base,
		"allowed.secret", "modules/middle/allowed.secret")
}

func TestVerifyCleanGitRepositoryScriptRejectsPostSetupMutation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*testing.T, nestedGitFixture)
		wantErr string
	}{
		{
			name: "tracked file",
			mutate: func(t *testing.T, fixture nestedGitFixture) {
				writeFile(t, filepath.Join(fixture.root, "root.txt"), "changed\n")
			},
			wantErr: "repository is not clean after clean-room setup",
		},
		{
			name: "untracked file",
			mutate: func(t *testing.T, fixture nestedGitFixture) {
				writeFile(t, filepath.Join(fixture.root, "untracked.txt"), "changed\n")
			},
			wantErr: "repository is not clean after clean-room setup",
		},
		{
			name: "unapproved ignored file",
			mutate: func(t *testing.T, fixture nestedGitFixture) {
				writeFile(t, filepath.Join(fixture.root, "unexpected.secret"), "changed\n")
			},
			wantErr: "unapproved ignored path after clean-room setup",
		},
		{
			name: "new ref in nested submodule",
			mutate: func(t *testing.T, fixture nestedGitFixture) {
				leaf := filepath.Join(fixture.root, "modules", "middle", "nested", "leaf")
				runGit(t, leaf, "branch", "unexpected")
			},
			wantErr: "unexpected refs after setup",
		},
		{
			name: "dangling remote head",
			mutate: func(t *testing.T, fixture nestedGitFixture) {
				ref := filepath.Join(fixture.root, ".git", "refs", "remotes", "origin", "HEAD")
				writeFile(t, ref, "ref: refs/remotes/origin/missing\n")
			},
			wantErr: "failed Git object verification after setup",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newNestedGitFixture(t)
			runCleanGitScript(t, fixture.root, fixture.base)
			test.mutate(t, fixture)
			output, err := executeVerifyGitScript(fixture.root, fixture.base)
			if err == nil || !strings.Contains(output, test.wantErr) {
				t.Fatalf("error = %v, output = %q, want %q", err, output, test.wantErr)
			}
		})
	}
}

func TestCleanGitRepositoryScriptRejectsDirtyOrWrongGitlink(t *testing.T) {
	t.Run("tracked changes", func(t *testing.T) {
		fixture := newNestedGitFixture(t)
		tracked := filepath.Join(fixture.root, "root.txt")
		writeFile(t, tracked, "modified")
		output, err := executeCleanGitScript(fixture.root, fixture.base)
		if err == nil || !strings.Contains(output, "tracked changes") {
			t.Fatalf("error = %v, output = %q", err, output)
		}
		if got := readFile(t, tracked); got != "modified" {
			t.Fatalf("dirty tracked file changed to %q", got)
		}
	})

	t.Run("submodule gitlink mismatch", func(t *testing.T) {
		fixture := newNestedGitFixture(t)
		leaf := filepath.Join(fixture.root, "modules", "middle", "nested", "leaf")
		runGit(t, leaf, "checkout", "-q", "HEAD~1")
		output, err := executeCleanGitScript(fixture.root, fixture.base)
		if err == nil || !strings.Contains(output, "tracked changes") {
			t.Fatalf("error = %v, output = %q", err, output)
		}
	})

	t.Run("base mismatch", func(t *testing.T) {
		fixture := newNestedGitFixture(t)
		output, err := executeCleanGitScript(fixture.root, strings.Repeat("0", 40))
		if err == nil || !strings.Contains(output, "is not expected base") {
			t.Fatalf("error = %v, output = %q", err, output)
		}
	})
}

func TestCleanGitRepositoryScriptResetsOfficialSetupCommitToBase(t *testing.T) {
	fixture := newNestedGitFixture(t)
	writeFile(t, filepath.Join(fixture.root, "root.txt"), "synthetic setup\n")
	runGit(t, fixture.root, "add", "root.txt")
	runGit(t, fixture.root, "commit", "-qm", "SWE-bench")
	setupCommit := strings.TrimSpace(runGit(t, fixture.root, "rev-parse", "HEAD"))
	if setupCommit == fixture.base {
		t.Fatal("official setup commit unexpectedly equals case base")
	}

	runCleanGitScript(t, fixture.root, fixture.base)

	if got := strings.TrimSpace(runGit(t, fixture.root, "rev-parse", "HEAD")); got != fixture.base {
		t.Fatalf("root HEAD = %s, want %s", got, fixture.base)
	}
	if got := readFile(t, filepath.Join(fixture.root, "root.txt")); got != "root\n" {
		t.Fatalf("root.txt = %q, want case-base contents", got)
	}
	assertSanitizedGitRepository(t, fixture.root)
	runVerifyGitScript(t, fixture.root, fixture.base)
}

func TestCleanGitRepositoryScriptRejectsNonOfficialCommitAfterBase(t *testing.T) {
	fixture := newNestedGitFixture(t)
	runGit(t, fixture.root, "commit", "--allow-empty", "-qm", "unexpected wrapper")

	output, err := executeCleanGitScript(fixture.root, fixture.base)
	if err == nil || !strings.Contains(output, "is not expected base") {
		t.Fatalf("error = %v, output = %q", err, output)
	}
	if got := strings.TrimSpace(runGit(t, fixture.root, "rev-parse", "HEAD")); got == fixture.base {
		t.Fatal("rejected wrapper was unexpectedly reset")
	}
}

func TestCleanGitRepositoryScriptRejectsIgnoredSymlink(t *testing.T) {
	fixture := newNestedGitFixture(t)
	if err := os.Symlink("root.txt", filepath.Join(fixture.root, "bad.secret")); err != nil {
		t.Fatal(err)
	}
	output, err := executeCleanGitScript(fixture.root, fixture.base)
	if err == nil || !strings.Contains(output, "ignored path is not a regular file: bad.secret") {
		t.Fatalf("error = %v, output = %q", err, output)
	}
}

func TestSanitizeGitRepositoryPassesExplicitContainerRoot(t *testing.T) {
	commander := &fakeCommander{}
	factory := DockerFactory{Commander: commander, GitIgnoredAllowlist: []string{"cache/"}}
	environment := &dockerEnvironment{name: "case", commander: commander}
	if err := factory.sanitizeGitRepository(context.Background(), environment, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(commander.commands))
	}
	args := commander.commands[0].args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "swebench-clean-room "+strings.Repeat("a", 40)+" /testbed cache/") {
		t.Fatalf("sanitize args = %q", joined)
	}
}

func TestVerifyGitRepositoryPassesExplicitContainerRoot(t *testing.T) {
	commander := &fakeCommander{}
	factory := DockerFactory{Commander: commander, GitIgnoredAllowlist: []string{"cache/"}}
	environment := &dockerEnvironment{name: "case", commander: commander}
	if err := factory.verifyGitRepository(context.Background(), environment, strings.Repeat("a", 40)); err != nil {
		t.Fatal(err)
	}
	if len(commander.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(commander.commands))
	}
	args := commander.commands[0].args
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "swebench-clean-room "+strings.Repeat("a", 40)+" /testbed cache/") {
		t.Fatalf("verify args = %q", joined)
	}
}

type nestedGitFixture struct {
	root string
	base string
}

func newNestedGitFixture(t *testing.T) nestedGitFixture {
	t.Helper()
	workspace := t.TempDir()
	leafSource := filepath.Join(workspace, "leaf-source")
	initGitRepository(t, leafSource, map[string]string{
		".gitignore": "*.secret\n",
		"leaf.txt":   "first\n",
	})
	writeFile(t, filepath.Join(leafSource, "leaf.txt"), "second\n")
	runGit(t, leafSource, "add", "leaf.txt")
	runGit(t, leafSource, "commit", "-qm", "second")
	runGit(t, leafSource, "tag", "source-tag")

	middleSource := filepath.Join(workspace, "middle-source")
	initGitRepository(t, middleSource, map[string]string{
		".gitignore": "*.secret\n",
		"middle.txt": "middle\n",
	})
	runGit(t, middleSource, "submodule", "add", "-q", leafSource, "nested/leaf")
	runGit(t, middleSource, "commit", "-qam", "add nested leaf")
	runGit(t, middleSource, "tag", "source-tag")

	root := filepath.Join(workspace, "root")
	initGitRepository(t, root, map[string]string{
		".gitignore": "*.secret\n",
		"root.txt":   "root\n",
	})
	runGit(t, root, "submodule", "add", "-q", middleSource, "modules/middle")
	runGit(t, root, "commit", "-qam", "add middle")
	runGit(t, root, "submodule", "update", "-q", "--init", "--recursive")
	runGit(t, root, "tag", "root-tag")
	runGit(t, root, "remote", "add", "unused", leafSource)
	return nestedGitFixture{
		root: root,
		base: strings.TrimSpace(runGit(t, root, "rev-parse", "HEAD")),
	}
}

func initGitRepository(t *testing.T, directory string, files map[string]string) {
	t.Helper()
	if err := os.MkdirAll(directory, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, directory, "init", "-q")
	for relative, content := range files {
		writeFile(t, filepath.Join(directory, relative), content)
	}
	runGit(t, directory, "add", ".")
	runGit(t, directory, "commit", "-qm", "initial")
}

func runCleanGitScript(t *testing.T, root, base string, allowlist ...string) {
	t.Helper()
	output, err := executeCleanGitScript(root, base, allowlist...)
	if err != nil {
		t.Fatalf("clean Git repository: %v\n%s", err, output)
	}
}

func executeCleanGitScript(root, base string, allowlist ...string) (string, error) {
	args := []string{"-c", cleanGitRepositoryScript, "swebench-clean-room-test", base, root}
	args = append(args, allowlist...)
	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=file")
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func runVerifyGitScript(t *testing.T, root, base string, allowlist ...string) {
	t.Helper()
	output, err := executeVerifyGitScript(root, base, allowlist...)
	if err != nil {
		t.Fatalf("verify Git repository: %v\n%s", err, output)
	}
}

func executeVerifyGitScript(root, base string, allowlist ...string) (string, error) {
	args := []string{"-c", verifyCleanGitRepositoryScript, "swebench-clean-room-test", base, root}
	args = append(args, allowlist...)
	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=file")
	output, err := cmd.CombinedOutput()
	return string(output), err
}

func assertSanitizedGitRepository(t *testing.T, repository string) {
	t.Helper()
	if got := strings.TrimSpace(runGit(t, repository, "status", "--porcelain")); got != "" {
		t.Fatalf("repository %s is dirty: %q", repository, got)
	}
	if got := strings.TrimSpace(runGit(t, repository, "remote")); got != "" {
		t.Fatalf("repository %s has remotes: %q", repository, got)
	}
	if got := strings.TrimSpace(runGit(t, repository, "tag", "--list")); got != "" {
		t.Fatalf("repository %s has tags: %q", repository, got)
	}
	refs := strings.Fields(runGit(t, repository, "for-each-ref", "--format=%(refname)"))
	if len(refs) != 1 || refs[0] != "refs/heads/swebench-clean-base" {
		t.Fatalf("repository %s refs = %q", repository, refs)
	}
	if _, err := os.Lstat(filepath.Join(repository, ".git", "logs")); !os.IsNotExist(err) {
		t.Fatalf("repository %s has reflogs: %v", repository, err)
	}
	if _, err := os.Lstat(filepath.Join(repository, ".git", "objects", "info", "alternates")); !os.IsNotExist(err) {
		t.Fatalf("repository %s has alternates: %v", repository, err)
	}
	if got := strings.TrimSpace(runGit(t, repository, "fsck", "--unreachable", "--no-reflogs")); got != "" {
		t.Fatalf("repository %s has unreachable objects: %q", repository, got)
	}
}

func runGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	config := []string{
		"-c", "protocol.file.allow=always",
		"-c", "user.name=SWE-Bench Test",
		"-c", "user.email=swebench-test@example.invalid",
	}
	cmd := exec.Command("git", append(config, args...)...)
	cmd.Dir = directory
	cmd.Env = append(os.Environ(), "GIT_ALLOW_PROTOCOL=file")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s in %s: %v\n%s", strings.Join(args, " "), directory, err, output)
	}
	return string(output)
}

func writeGitObject(t *testing.T, repository, content string) {
	t.Helper()
	cmd := exec.Command("git", "hash-object", "-w", "--stdin")
	cmd.Dir = repository
	cmd.Stdin = strings.NewReader(content)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("write unreachable object: %v\n%s", err, output)
	}
}

func writeFile(t *testing.T, file, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, file string) string {
	t.Helper()
	content, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return string(content)
}
