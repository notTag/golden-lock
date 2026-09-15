package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Outside version control, init writes an empty manifest that anchors the root
// for nested dirs, verifies as OK, and is never overwritten by a re-run.
func TestInit_AnchorsNonGitDir(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)

	if code := runInit(nil); code != ExitWriteOK {
		t.Fatalf("init exit = %d, want %d", code, ExitWriteOK)
	}

	nested := filepath.Join(dir, "a", "b")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	root, err := FindRepoRoot(nested)
	if err != nil {
		t.Fatalf("FindRepoRoot after init: %v", err)
	}
	if !sameDir(t, root, dir) {
		t.Fatalf("FindRepoRoot(%q) = %q, want %q", nested, root, dir)
	}

	if _, code, _ := Verify(dir); code != ExitVerifyOK {
		t.Fatalf("verify on fresh manifest = %d, want %d", code, ExitVerifyOK)
	}

	manifestPath := LockfilePath(dir)
	const handEdited = "# kept\n"
	if err := os.WriteFile(manifestPath, []byte(handEdited), 0o600); err != nil {
		t.Fatalf("rewrite manifest: %v", err)
	}
	if code := runInit(nil); code != ExitWriteOK {
		t.Fatalf("second init exit = %d, want %d", code, ExitWriteOK)
	}
	got, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if string(got) != handEdited {
		t.Fatalf("init overwrote an existing manifest; got %q", got)
	}
}

// Inside a git repo `.git` wins root discovery, so a manifest there would be
// ignored — init must refuse and create nothing.
func TestInit_RefusesInsideGitRepo(t *testing.T) {
	root := fakeRepo(t)
	sub := filepath.Join(root, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}
	chdirTo(t, sub)

	if code := runInit(nil); code != ExitWriteArgs {
		t.Fatalf("init in git repo exit = %d, want %d", code, ExitWriteArgs)
	}
	if _, err := os.Stat(filepath.Join(sub, GoldenLockDir)); !os.IsNotExist(err) {
		t.Fatalf("init created %s/ inside a git repo (err=%v)", GoldenLockDir, err)
	}
}
