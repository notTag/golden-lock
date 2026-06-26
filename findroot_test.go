package main

import (
	"os"
	"path/filepath"
	"testing"
)

// FindRepoRoot's lockfile fallback must anchor on the manifest's home,
// golden-lock/golden.lock, discovered by walking upward from a nested start dir
// (no .git marker present).
func TestFindRepoRoot_GoldenLockAnchor(t *testing.T) {
	root := t.TempDir()
	manifestDir := filepath.Join(root, GoldenLockDir)
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		t.Fatalf("mkdir golden-lock: %v", err)
	}
	if err := os.WriteFile(filepath.Join(manifestDir, LockfileName), []byte("# manifest\n"), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	start := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(start, 0o755); err != nil {
		t.Fatalf("mkdir start: %v", err)
	}

	got, err := FindRepoRoot(start)
	if err != nil {
		t.Fatalf("FindRepoRoot: %v", err)
	}
	if !sameDir(t, got, root) {
		t.Fatalf("FindRepoRoot(%q) = %q, want %q", start, got, root)
	}
}

// A bare root-level golden.lock (not inside golden-lock/) must NOT anchor the
// repo root — the manifest lives under golden-lock/.
func TestFindRepoRoot_BareRootLockfileIgnored(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, LockfileName), []byte("# stray\n"), 0o644); err != nil {
		t.Fatalf("write stray manifest: %v", err)
	}
	got, err := FindRepoRoot(root)
	if err == nil && sameDir(t, got, root) {
		t.Fatalf("anchored on bare root-level %s at %q; manifest must live under %s/", LockfileName, got, GoldenLockDir)
	}
}

// sameDir compares two paths after resolving symlinks, since t.TempDir() on
// macOS hands back a /var → /private/var symlinked path.
func sameDir(t *testing.T, a, b string) bool {
	t.Helper()
	ra, err := filepath.EvalSymlinks(a)
	if err != nil {
		return false
	}
	rb, err := filepath.EvalSymlinks(b)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", b, err)
	}
	return ra == rb
}
