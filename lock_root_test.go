package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory with no .git and no manifest above it is not a project: lock falls
// back to the working directory so the manifest is created there, rather than
// failing with "repo root not found".
func TestRootForNewManifest_LooseDirFallsBackToCwd(t *testing.T) {
	dir := t.TempDir()

	root, looseDir := rootForNewManifest(dir)
	if !looseDir {
		t.Errorf("looseDir = false for %q, want true (no .git, no manifest)", dir)
	}
	if !sameDir(t, root, dir) {
		t.Errorf("rootForNewManifest(%q) root = %q, want the dir itself", dir, root)
	}
}

// Inside a repo the fallback must never engage: a file locked from a
// subdirectory belongs to the repo's manifest, not to a second one beside it.
func TestRootForNewManifest_RepoRootWinsFromSubdir(t *testing.T) {
	repoRoot := fakeRepo(t)
	sub := filepath.Join(repoRoot, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	root, looseDir := rootForNewManifest(sub)
	if looseDir {
		t.Errorf("looseDir = true inside a repo at %q, want false", repoRoot)
	}
	if !sameDir(t, root, repoRoot) {
		t.Errorf("rootForNewManifest(%q) root = %q, want repo root %q", sub, root, repoRoot)
	}
}

// Outside a project, setup must scaffold in the working directory rather than
// failing on root discovery — otherwise the no-arg proposal-locks flow is
// unreachable there: setup cannot create the directory, and `lock` with no paths
// finds nothing listed.
func TestSetup_LooseDirScaffoldsInCwd(t *testing.T) {
	dir := t.TempDir()
	chdirTo(t, dir)

	if code := runSetup(nil); code != ExitWriteOK {
		t.Fatalf("setup in a loose dir exit = %d, want %d", code, ExitWriteOK)
	}
	proposalDir := filepath.Join(dir, GoldenLockDir, ProposalLocksDir)
	if info, err := os.Stat(proposalDir); err != nil || !info.IsDir() {
		t.Fatalf("proposal-locks dir not created in a loose dir: %v", err)
	}
	// Still a first-lock artifact, never a setup one.
	if _, err := os.Stat(LockfilePath(dir)); !os.IsNotExist(err) {
		t.Fatalf("setup must not create %s (got err=%v)", LockfileName, err)
	}
}

// A manifest created by an earlier loose-directory lock anchors every later
// lock, so a second file locked from a subdirectory joins the same manifest
// instead of starting a new one.
func TestRootForNewManifest_EarlierManifestAnchorsLaterLocks(t *testing.T) {
	dir := t.TempDir()
	manifest := &Manifest{Root: dir, Path: LockfilePath(dir)}
	if err := WriteManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatalf("mkdir sub: %v", err)
	}

	root, looseDir := rootForNewManifest(sub)
	if looseDir {
		t.Errorf("looseDir = true with a manifest at %q, want false", dir)
	}
	if !sameDir(t, root, dir) {
		t.Errorf("rootForNewManifest(%q) root = %q, want manifest root %q", sub, root, dir)
	}
}
