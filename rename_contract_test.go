package main

// rename_contract_test.go — regression tripwires guarding the golden-test →
// golden-lock rename (feat-003) and the "lock any file, not just tests"
// generalization.
//
// Two kinds of test live here:
//
//   - TRIPWIRES pin a CURRENT literal value (the program name, the manifest
//     filename, the exit-code integers). They are expected to fail loudly when
//     the rename touches that value — that is the point. A failure forces a
//     DELIBERATE edit here, with a migration note, instead of a silent drift
//     that breaks already-locked repos or CI scripts.
//
//   - CONTRACT GUARDS pin behavior that must survive the rename unchanged: the
//     dispatch routing matrix, help listing every subcommand, and — central to
//     feat-003 — that the verify/lock pipeline treats a NON-test file exactly
//     like a test file (nothing is gated on a `_test`-shaped name).
//
// All of these run with no privilege except TestContract_LockVerifyAnyFile,
// which needs real root (chown-to-root) and t.Skips otherwise, mirroring the
// existing write-path tests.

import (
	"io"
	"os"
	"strings"
	"testing"
)

// TestContract_ExitCodeValues pins the exact integer exit codes. These are the
// tool's external ABI: CI scripts branch on `verify` returning 0/1/2/3 and on
// lock/unlock returning 0/4/5/6. A rename/refactor must never renumber them.
func TestContract_ExitCodeValues(t *testing.T) {
	cases := []struct {
		name string
		got  int
		want int
	}{
		{"ExitWriteOK", ExitWriteOK, 0},
		{"ExitWriteNotRoot", ExitWriteNotRoot, 4},
		{"ExitWriteArgs", ExitWriteArgs, 5},
		{"ExitWriteIO", ExitWriteIO, 6},
		{"ExitVerifyOK", ExitVerifyOK, 0},
		{"ExitVerifyMismatch", ExitVerifyMismatch, 1},
		{"ExitVerifyMissing", ExitVerifyMissing, 2},
		{"ExitVerifyLockfile", ExitVerifyLockfile, 3},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (exit-code ABI must not change)", c.name, c.got, c.want)
		}
	}
}

// TestContract_DispatchMatrix pins the full routing contract: which argv[0]
// tokens are help (exit 0) and which are arg errors (exit 5). The subcommand
// names lock/unlock/verify and the help aliases are part of the user contract
// and must survive the rename intact.
func TestContract_DispatchMatrix(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"help-long", []string{"--help"}, ExitWriteOK},
		{"help-short", []string{"-h"}, ExitWriteOK},
		{"help-word", []string{"help"}, ExitWriteOK},
		{"empty", []string{}, ExitWriteArgs},
		{"unknown", []string{"frobnicate"}, ExitWriteArgs},
	}
	for _, c := range cases {
		// usage() writes to os.Stderr; silence it so the matrix stays readable.
		if code := withSilencedStderr(t, func() int { return dispatch(c.args) }); code != c.want {
			t.Errorf("dispatch(%q): code = %d, want %d", c.args, code, c.want)
		}
	}
}

// TestContract_ProgName is a TRIPWIRE on the user-facing program name. It pins
// the post-rename identity: base name "golden-lock" and the short alias "gl".
// Any future drift to these user-facing names must be a conscious edit here.
func TestContract_ProgName(t *testing.T) {
	savedArgs := os.Args
	t.Cleanup(func() { os.Args = savedArgs })

	cases := []struct {
		name  string
		argv0 []string
		want  string
	}{
		{"base", []string{"golden-lock"}, "golden-lock"},
		{"alias", []string{"gl"}, "gl"},
		{"full-path", []string{"/usr/local/bin/golden-lock"}, "golden-lock"},
		{"empty-argv", []string{}, "golden-lock"},
	}
	for _, c := range cases {
		os.Args = c.argv0
		if got := progName(); got != c.want {
			t.Errorf("%s: progName() = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestContract_LockfileNameStable is a TRIPWIRE on the manifest filename. This
// is the trust-anchor on disk: every locked repo carries a file by this exact
// name, and FindRepoRoot treats it as a repo-root marker. It was renamed once
// (golden-test.lock -> golden.lock) with no back-compat, since the tool had a
// single user and no repos to migrate. Any further change must be deliberate:
// flipping the name orphans every already-locked repo's manifest.
func TestContract_LockfileNameStable(t *testing.T) {
	const want = "golden.lock"
	if LockfileName != want {
		t.Errorf("LockfileName = %q, want %q — changing the manifest filename breaks "+
			"every already-locked repo; require an explicit migration before editing this", LockfileName, want)
	}
}

// TestContract_UsageListsAllCommands is branding-INDEPENDENT: whatever the tool
// is called, its help must still name all three subcommands and the manifest
// file. Guards against a rebrand that rewords usage() and accidentally drops a
// command from the listing.
func TestContract_UsageListsAllCommands(t *testing.T) {
	text := captureStderr(t, usage)
	for _, must := range []string{"lock", "unlock", "verify", LockfileName} {
		if !strings.Contains(text, must) {
			t.Errorf("usage() output missing %q; full text:\n%s", must, text)
		}
	}
}

// TestContract_VerifyIsFileAgnostic is the core feat-003 guard, root-free: the
// verify pipeline must treat an ordinary, non-test file identically to a test
// file. If any future change gates hashing/verification on a `_test`-shaped
// name, this config file would stop verifying OK and the test fails.
func TestContract_VerifyIsFileAgnostic(t *testing.T) {
	root := fakeRepo(t)
	const relPath = "config/production.yaml"
	const content = "replicas: 3\n"
	writeFile(t, root, relPath, content)
	writeManifest(t, root, goldenHash(relPath, content)+"  "+relPath+"\n")

	results, code := Verify(root)
	if code != ExitVerifyOK {
		t.Fatalf("Verify of a non-test file: code = %d, want %d (locking must be file-agnostic)", code, ExitVerifyOK)
	}
	if len(results) != 1 || results[0].Status != StatusOK {
		t.Fatalf("Verify results = %+v, want one StatusOK entry", results)
	}
}

// TestContract_LockVerifyAnyFile is the full-fidelity feat-003 guard: actually
// lock a non-test file end-to-end and verify it. Needs real root (chown-to-root)
// and t.Skips otherwise, like the other write-path tests.
func TestContract_LockVerifyAnyFile(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("not root: lock's chown-to-root requires real root privilege")
	}
	root := fakeRepo(t)
	const relPath = "config/production.yaml"
	const content = "replicas: 3\n"
	writeFile(t, root, relPath, content)

	chdirTo(t, root)
	if code := runLock([]string{relPath}); code != ExitWriteOK {
		t.Fatalf("runLock of a non-test file: code = %d, want %d", code, ExitWriteOK)
	}
	// lock makes the file + manifest immutable; clear that before TempDir cleanup.
	t.Cleanup(func() { _ = runUnlock([]string{relPath}) })

	results, code := Verify(root)
	if code != ExitVerifyOK {
		t.Fatalf("Verify after locking a non-test file: code = %d, want %d", code, ExitVerifyOK)
	}
	if len(results) != 1 || results[0].Status != StatusOK {
		t.Fatalf("Verify results = %+v, want one StatusOK entry", results)
	}
}

// ---------------------------------------------------------------------------
// stderr-capture helpers (usage/dispatch write to os.Stderr directly)
// ---------------------------------------------------------------------------

// captureStderr redirects os.Stderr for the duration of fn and returns whatever
// fn wrote to it.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	saved := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = saved
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatalf("read captured stderr: %v", err)
	}
	return string(out)
}

// withSilencedStderr runs fn with os.Stderr discarded and returns fn's result,
// keeping noisy usage() output out of the test log.
func withSilencedStderr(t *testing.T, fn func() int) int {
	t.Helper()
	saved := os.Stderr
	devNull, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stderr = devNull
	defer func() {
		os.Stderr = saved
		devNull.Close()
	}()
	return fn()
}
