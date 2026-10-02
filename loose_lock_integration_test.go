package main

import (
	"os"
	"path/filepath"
	"testing"
)

// loose_lock_integration_test.go — root-gated end-to-end coverage for locking
// OUTSIDE a project (no .git, no manifest above). These drive the real
// privileged pipeline, so they t.Skip when not root, the same convention as
// dir_lock_integration_test.go; run them with `sudo go test`.

// fileIsFrozen reports whether path has lock's 0444 permission bits. A file the
// freeze never reached still carries its owner-write bit.
func fileIsFrozen(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()&0o200 == 0
}

// A rejected path must freeze NOTHING. The freeze loop turns files immutable one
// at a time and publishes the manifest only at the end, so a path rejected
// mid-loop would leave earlier files frozen with no manifest recording them —
// and unlock resolves its root FROM the manifest, so in a loose directory those
// files could only be freed by hand as root. Paths are validated up front to
// keep that state unreachable.
func TestLockLooseDir_RejectedPathFreezesNothing(t *testing.T) {
	requireRoot(t)
	looseDir := t.TempDir()
	writeFile(t, looseDir, "a.txt", "keep me writable\n")
	outsideDir := t.TempDir()
	writeFile(t, outsideDir, "b.txt", "outside the root\n")
	chdirTo(t, looseDir)

	escaping := filepath.Join(outsideDir, "b.txt")
	code := runLock([]string{"a.txt", escaping})
	if code != ExitWriteArgs {
		// Undo a partial freeze so TempDir cleanup can remove the files.
		t.Cleanup(func() { _ = runUnlock([]string{"a.txt"}) })
		t.Fatalf("runLock with an escaping path = %d, want %d", code, ExitWriteArgs)
	}

	if fileIsFrozen(t, filepath.Join(looseDir, "a.txt")) {
		t.Errorf("a.txt was frozen before the escaping path was rejected; it would be unrecoverable (no manifest for unlock to anchor on)")
	}
	if _, err := os.Stat(LockfilePath(looseDir)); !os.IsNotExist(err) {
		t.Errorf("manifest exists after a rejected run (err=%v), want none", err)
	}
}

// The whole point of the fallback: locking a file in a plain directory works with
// no bootstrap step. The manifest is created in the working directory, verify
// passes against it, and unlock — which anchors on that new manifest — frees the
// file again.
func TestLockLooseDir_CreatesManifestAndRoundTrips(t *testing.T) {
	requireRoot(t)
	looseDir := t.TempDir()
	writeFile(t, looseDir, "notes.txt", "lock me\n")
	chdirTo(t, looseDir)

	if code := runLock([]string{"notes.txt"}); code != ExitWriteOK {
		t.Fatalf("runLock in a loose dir = %d, want %d", code, ExitWriteOK)
	}
	// Safety net for a mid-test failure: an immutable file would block TempDir
	// cleanup. The happy path unlocks explicitly below, after which this call
	// logs "cannot find repo root" (the manifest is gone) and is ignored.
	t.Cleanup(func() { _ = runUnlock([]string{"notes.txt"}) })

	if _, err := os.Stat(LockfilePath(looseDir)); err != nil {
		t.Fatalf("manifest not created in the working directory: %v", err)
	}
	if !fileIsFrozen(t, filepath.Join(looseDir, "notes.txt")) {
		t.Errorf("notes.txt is still writable after lock")
	}
	if got := verifiedOKPaths(t, looseDir); !equalStrings(got, []string{"notes.txt"}) {
		t.Fatalf("verified %v, want [notes.txt]", got)
	}

	// unlock must find the root via the manifest this lock created.
	if code := runUnlock([]string{"notes.txt"}); code != ExitWriteOK {
		t.Fatalf("runUnlock in a loose dir = %d, want %d", code, ExitWriteOK)
	}
	if fileIsFrozen(t, filepath.Join(looseDir, "notes.txt")) {
		t.Errorf("notes.txt still frozen after unlock")
	}
	if _, err := os.Stat(LockfilePath(looseDir)); !os.IsNotExist(err) {
		t.Errorf("emptied manifest was not removed (err=%v)", err)
	}
}
