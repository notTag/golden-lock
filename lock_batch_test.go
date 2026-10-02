package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Exercise real path resolution, hashing, chmod and manifest I/O without root.
// Only ownership and immutable flags are simulated in these transaction tests.
func testLockOps(t *testing.T) (lockOps, map[string]bool) {
	t.Helper()
	flags := make(map[string]bool)
	ops := defaultLockOps()
	ops.capture = func(f *os.File) (fileLockState, error) {
		info, err := f.Stat()
		if err != nil {
			return fileLockState{}, err
		}
		return fileLockState{mode: info.Mode(), immutable: flags[f.Name()]}, nil
	}
	ops.restore = func(f *os.File, before fileLockState) error {
		flags[f.Name()] = before.immutable
		return f.Chmod(before.mode)
	}
	ops.freeze = func(f *os.File) error {
		flags[f.Name()] = false
		return f.Chmod(0o444)
	}
	ops.immutable = func(f *os.File) (bool, error) {
		flags[f.Name()] = true
		return true, nil
	}
	ops.publish = WriteManifest
	return ops, flags
}

func TestLockBatchContinuesAfterInvalidPaths(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	c := writeFile(t, root, "c.txt", "c")
	writeFile(t, root, "real/b.txt", "b")
	if err := os.Symlink("real", filepath.Join(root, "linked")); err != nil {
		t.Fatal(err)
	}
	outside := writeFile(t, t.TempDir(), "outside.txt", "outside")
	ops, _ := testLockOps(t)
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	failures := lockBatch(m, []string{a, filepath.Join(root, "linked/b.txt"), outside, c}, ops)
	if len(failures) != 2 || lockFailureCode(failures) != ExitWriteArgs {
		t.Fatalf("failures = %v", failures)
	}
	if got := verifiedOKPaths(t, root); !equalStrings(got, []string{"a.txt", "c.txt"}) {
		t.Fatalf("verified %v", got)
	}
	if !fileIsFrozen(t, a) || !fileIsFrozen(t, c) || fileIsFrozen(t, outside) {
		t.Fatal("wrong files frozen")
	}
}

func TestExpandLockTargetsContinuesAfterMissingInput(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	c := writeFile(t, root, "c.txt", "c")
	missing := filepath.Join(root, "missing.txt")
	paths, failures := expandLockTargets(root, []string{a, missing, c})
	if !equalStrings(paths, []string{a, c}) || len(failures) != 1 || failures[0].path != missing {
		t.Fatalf("paths=%v failures=%v", paths, failures)
	}
}

func TestLockBatchRestoresFailedFileAndContinues(t *testing.T) {
	for _, stage := range []string{"freeze", "hash", "immutable"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			a := writeFile(t, root, "a.txt", "a")
			b := writeFile(t, root, "b.txt", "b")
			c := writeFile(t, root, "c.txt", "c")
			if err := os.Chmod(b, 0o751); err != nil {
				t.Fatal(err)
			}
			ops, flags := testLockOps(t)
			injected := errors.New("injected " + stage + " failure")
			switch stage {
			case "freeze":
				freeze := ops.freeze
				ops.freeze = func(f *os.File) error {
					if err := freeze(f); err != nil {
						return err
					}
					if f.Name() == b {
						return injected
					}
					return nil
				}
			case "hash":
				ops.hash = func(path string, r io.Reader) (string, error) {
					if path == "b.txt" {
						return "", injected
					}
					return hashReader(path, r)
				}
			case "immutable":
				immutable := ops.immutable
				ops.immutable = func(f *os.File) (bool, error) {
					if f.Name() == b {
						return false, injected
					}
					return immutable(f)
				}
			}
			m := &Manifest{Root: root, Path: LockfilePath(root)}
			failures := lockBatch(m, []string{a, b, c}, ops)
			if len(failures) != 1 || !errors.Is(failures[0].err, injected) || lockFailureCode(failures) != ExitWriteIO {
				t.Fatalf("failures = %v", failures)
			}
			info, err := os.Stat(b)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o751 || flags[b] {
				t.Fatal("failed file not restored")
			}
			if got := verifiedOKPaths(t, root); !equalStrings(got, []string{"a.txt", "c.txt"}) {
				t.Fatalf("verified %v", got)
			}
		})
	}
}

func TestLockBatchPublishFailureRestoresNewAndExistingFiles(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	b := writeFile(t, root, "b.txt", "b")
	if err := os.Chmod(b, 0o751); err != nil {
		t.Fatal(err)
	}
	ops, flags := testLockOps(t)
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	if failures := lockBatch(m, []string{a}, ops); len(failures) != 0 {
		t.Fatal(failures)
	}
	oldManifest, err := os.ReadFile(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	ops.publish = func(*Manifest) error { return errors.New("publish failed") }
	failures := lockBatch(m, []string{a, b}, ops)
	if lockFailureCode(failures) != ExitWriteIO {
		t.Fatalf("failures = %v", failures)
	}
	if !fileIsFrozen(t, a) || !flags[a] {
		t.Fatal("existing lock not preserved")
	}
	info, err := os.Stat(b)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o751 || flags[b] {
		t.Fatal("new file not restored")
	}
	manifest, err := os.ReadFile(m.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(manifest) != string(oldManifest) || m.Find("b.txt") >= 0 {
		t.Fatal("old manifest changed")
	}
}

func TestLockBatchPostPublishFailureKeepsRecordedFilesFrozen(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	ops, flags := testLockOps(t)
	ops.publish = func(m *Manifest) error {
		if err := WriteManifest(m); err != nil {
			return err
		}
		return ErrManifestPublished
	}
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	if failures := lockBatch(m, []string{a}, ops); lockFailureCode(failures) != ExitWriteIO {
		t.Fatal(failures)
	}
	if !fileIsFrozen(t, a) || !flags[a] {
		t.Fatal("published file thawed")
	}
	if got := verifiedOKPaths(t, root); !equalStrings(got, []string{"a.txt"}) {
		t.Fatal(got)
	}
}

func TestLockBatchAllFailuresDoNotPublish(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	ops, _ := testLockOps(t)
	ops.hash = func(string, io.Reader) (string, error) { return "", errors.New("read failed") }
	ops.publish = func(*Manifest) error { t.Fatal("published with no successful files"); return nil }
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	if failures := lockBatch(m, []string{a}, ops); len(failures) != 1 {
		t.Fatal(failures)
	}
	if fileIsFrozen(t, a) {
		t.Fatal("failed file frozen")
	}
}

func TestLockBatchReportsRollbackFailure(t *testing.T) {
	root := t.TempDir()
	a := writeFile(t, root, "a.txt", "a")
	ops, _ := testLockOps(t)
	ops.hash = func(string, io.Reader) (string, error) { return "", errors.New("read failed") }
	restoreErr := errors.New("restore denied")
	ops.restore = func(*os.File, fileLockState) error { return restoreErr }
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	failures := lockBatch(m, []string{a}, ops)
	if len(failures) != 2 || !errors.Is(failures[1].err, restoreErr) {
		t.Fatalf("rollback error not reported: %v", failures)
	}
	// Check the user-facing report includes every failed path and the recovery
	// error, rather than allowing an incomplete rollback to appear successful.
	report, err := os.CreateTemp(t.TempDir(), "report")
	if err != nil {
		t.Fatal(err)
	}
	defer report.Close()
	old := os.Stderr
	os.Stderr = report
	reportLockFailures(failures)
	os.Stderr = old
	data, err := os.ReadFile(report.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), a) || !strings.Contains(string(data), "manual recovery required") || !strings.Contains(string(data), "restore denied") {
		t.Fatalf("incomplete error report: %s", data)
	}
}
