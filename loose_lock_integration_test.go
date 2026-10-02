package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestLockLooseDir_SymlinkParentDoesNotDiscardSuccesses(t *testing.T) {
	requireRoot(t)
	root := t.TempDir()
	writeFile(t, root, "a.txt", "a")
	writeFile(t, root, "c.txt", "c")
	writeFile(t, root, "real/b.txt", "b")
	if err := os.Symlink("real", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	t.Cleanup(func() { _ = runUnlock([]string{"--uid=0", "a.txt", "c.txt"}) })
	if code := runLock([]string{"a.txt", "linked/b.txt", "c.txt"}); code != ExitWriteArgs {
		t.Fatalf("runLock = %d, want %d", code, ExitWriteArgs)
	}
	if got := verifiedOKPaths(t, root); !equalStrings(got, []string{"a.txt", "c.txt"}) {
		t.Fatal(got)
	}
	if code := runUnlock([]string{"--uid=0", "a.txt", "c.txt"}); code != ExitWriteOK {
		t.Fatal(code)
	}
}

func TestLockBatchPrivilegedRollback(t *testing.T) {
	requireRoot(t)
	for _, stage := range []string{"hash", "publish"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			path := writeFile(t, root, "a.txt", "a")
			f, err := os.OpenFile(path, os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			// An ordinary owner's executable mode must survive the rollback.
			if err := f.Chown(1234, 1234); err != nil {
				t.Fatal(err)
			}
			if err := f.Chmod(0o751); err != nil {
				t.Fatal(err)
			}
			before, err := captureLockState(f)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = restoreLockState(f, before); _ = f.Close() })
			ops := defaultLockOps()
			injected := errors.New("injected " + stage + " failure")
			if stage == "hash" {
				ops.hash = func(string, io.Reader) (string, error) { return "", injected }
			} else {
				ops.publish = func(*Manifest) error { return injected }
			}
			m := &Manifest{Root: root, Path: LockfilePath(root)}
			failures := lockBatch(m, []string{path}, ops)
			if lockFailureCode(failures) != ExitWriteIO {
				t.Fatal(failures)
			}
			after, err := captureLockState(f)
			if err != nil {
				t.Fatal(err)
			}
			if after != before {
				t.Fatalf("state = %+v, want %+v", after, before)
			}
			if _, err := os.Stat(m.Path); !os.IsNotExist(err) {
				t.Fatalf("manifest should be absent: %v", err)
			}
		})
	}
}

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

// An invalid path is reported while valid files are published and recoverable.
func TestLockLooseDir_RejectedPathKeepsSuccessfulLocks(t *testing.T) {
	requireRoot(t)
	looseDir := t.TempDir()
	writeFile(t, looseDir, "a.txt", "lock me\n")
	outsideDir := t.TempDir()
	writeFile(t, outsideDir, "b.txt", "outside the root\n")
	chdirTo(t, looseDir)
	t.Cleanup(func() { _ = runUnlock([]string{"--uid=0", "a.txt"}) })
	if code := runLock([]string{"a.txt", filepath.Join(outsideDir, "b.txt")}); code != ExitWriteArgs {
		t.Fatalf("runLock = %d, want %d", code, ExitWriteArgs)
	}
	if got := verifiedOKPaths(t, looseDir); !equalStrings(got, []string{"a.txt"}) {
		t.Fatalf("verified %v, want [a.txt]", got)
	}
	if fileIsFrozen(t, filepath.Join(outsideDir, "b.txt")) {
		t.Fatal("outside file was frozen")
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
