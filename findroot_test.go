package main

import (
	"os"
	"path/filepath"
	"strings"
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

// TestVettedGitChildPath_CoversEveryCandidateDir pins #21: the pinned PATH given
// to the git child must contain the directory of EVERY vetted git candidate, so
// whichever git vettedGitPath could select is always reachable on that PATH. The
// two are derived from the same list and therefore cannot drift.
func TestVettedGitChildPath_CoversEveryCandidateDir(t *testing.T) {
	childPath := vettedGitChildPath()
	pathDirs := strings.Split(childPath, string(os.PathListSeparator))
	present := make(map[string]bool, len(pathDirs))
	for _, dir := range pathDirs {
		present[dir] = true
	}
	for _, cand := range vettedGitCandidates {
		candDir := filepath.Dir(cand)
		if !present[candDir] {
			t.Errorf("candidate %q dir %q missing from child PATH %q", cand, candDir, childPath)
		}
	}
}

// The child PATH must not contain duplicate directories.
func TestVettedGitChildPath_NoDuplicateDirs(t *testing.T) {
	pathDirs := strings.Split(vettedGitChildPath(), string(os.PathListSeparator))
	seen := make(map[string]bool, len(pathDirs))
	for _, dir := range pathDirs {
		if seen[dir] {
			t.Errorf("child PATH %q contains duplicate dir %q", vettedGitChildPath(), dir)
		}
		seen[dir] = true
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
