package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// dir_lock_integration_test.go — root-gated end-to-end coverage for feat-006
// directory lock/unlock. Unlike dir_lock_test.go (which unit-tests the pure
// expandLockTargets/expandUnlockTargets path), these drive the REAL privileged
// pipeline — freeze → immutable flag → manifest publish, then Verify — against
// directory arguments. They t.Skip when not root, the same convention as
// TestContract_LockVerifyAnyFile, so `go test` stays green for ordinary users
// and the security mechanism is exercised under `sudo go test` or a root CI job.

// requireRoot skips the calling test unless the process is really root, since
// lock's chown-to-root and the immutable flag both need the privilege.
func requireRoot(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		t.Skip("not root: lock/unlock chown-to-root requires real root privilege")
	}
}

// verifiedOKPaths runs Verify and returns the sorted paths it reports OK,
// failing the test on any non-OK status or a non-zero verify code.
func verifiedOKPaths(t *testing.T, root string) []string {
	t.Helper()
	results, code, _ := Verify(root)
	if code != ExitVerifyOK {
		t.Fatalf("Verify code = %d, want %d", code, ExitVerifyOK)
	}
	paths := make([]string, 0, len(results))
	for _, r := range results {
		if r.Status != StatusOK {
			t.Fatalf("entry %s status = %v, want OK", r.Path, r.Status)
		}
		paths = append(paths, r.Path)
	}
	sort.Strings(paths)
	return paths
}

// Locking a directory freezes every regular file under it (recursively) and
// nothing else — the dotfile and the symlink are skipped — and each becomes its
// own OK manifest entry (feat-006, end to end).
func TestLockDir_LocksTreeSkippingDotAndSymlink(t *testing.T) {
	requireRoot(t)
	root := fakeRepo(t)
	writeFile(t, root, "testdata/a.go", "package a\n")
	writeFile(t, root, "testdata/sub/b.go", "package b\n")
	writeFile(t, root, "testdata/.hidden", "ignore me\n")
	if err := os.Symlink("a.go", filepath.Join(root, "testdata", "link.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	chdirTo(t, root)

	if code := runLock([]string{"testdata"}); code != ExitWriteOK {
		t.Fatalf("runLock(testdata) = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{"testdata"}) }) // clear immutable before TempDir cleanup

	got := verifiedOKPaths(t, root)
	want := []string{"testdata/a.go", "testdata/sub/b.go"}
	if !equalStrings(got, want) {
		t.Fatalf("locked %v, want %v (dotfile + symlink must be skipped)", got, want)
	}
}

// `lock .` from the repo root locks user files but never Golden Lock's own state
// directory; otherwise it would freeze the manifest as user content and break
// verify (feat-006 / Codex review P1), verified end to end.
func TestLockDot_ExcludesStateDir(t *testing.T) {
	requireRoot(t)
	root := fakeRepo(t)
	writeFile(t, root, "core.go", "package core\n")
	writeFile(t, root, "golden-lock/proposal-locks/list.txt", "core.go\n")
	chdirTo(t, root)

	if code := runLock([]string{"."}); code != ExitWriteOK {
		t.Fatalf("runLock(.) = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{"."}) })

	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	sawCore := false
	for _, e := range m.Entries {
		if strings.HasPrefix(e.Path, GoldenLockDir+"/") {
			t.Errorf("manifest lists internal state file %q; %s/ must be excluded", e.Path, GoldenLockDir)
		}
		if e.Path == "core.go" {
			sawCore = true
		}
	}
	if !sawCore {
		t.Errorf("manifest missing core.go; lock . should still lock real files")
	}
}

// A large directory sweep freezes without the interactive prompt when -y/--yes
// is passed, so scripted or CI locks of big trees proceed instead of blocking
// on a confirmation no one is there to answer (feat-006 sweep guard).
func TestLockDir_LargeSweepYesFlagSkipsPrompt(t *testing.T) {
	requireRoot(t)
	root := fakeRepo(t)
	const total = lockSweepWarnThreshold + 5 // over the threshold, so the guard engages
	for i := 0; i < total; i++ {
		writeFile(t, root, fmt.Sprintf("big/f%03d.txt", i), "x\n")
	}
	chdirTo(t, root)

	if code := runLock([]string{"-y", "big"}); code != ExitWriteOK {
		t.Fatalf("runLock(-y big) = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{"big"}) })

	if got := verifiedOKPaths(t, root); len(got) != total {
		t.Fatalf("locked %d files, want %d", len(got), total)
	}
}

// Unlocking a directory restores every file it locked; emptying the manifest
// removes it, so a follow-up verify reports the anchor absent (feat-006).
func TestUnlockDir_RestoresAndRemovesManifest(t *testing.T) {
	requireRoot(t)
	root := fakeRepo(t)
	writeFile(t, root, "testdata/a.go", "package a\n")
	writeFile(t, root, "testdata/sub/b.go", "package b\n")
	chdirTo(t, root)

	if code := runLock([]string{"testdata"}); code != ExitWriteOK {
		t.Fatalf("runLock(testdata) = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{"testdata"}) }) // safety net if an assert below fails first

	if code := runUnlock([]string{"testdata"}); code != ExitWriteOK {
		t.Fatalf("runUnlock(testdata) = %d, want %d", code, ExitWriteOK)
	}
	if _, code, _ := Verify(root); code != ExitVerifyLockfile {
		t.Errorf("post dir-unlock verify = %d, want %d (manifest removed)", code, ExitVerifyLockfile)
	}
}
