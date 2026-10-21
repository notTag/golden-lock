package main

// end_of_options_test.go — pins `--` as the end-of-options contract for lock
// and unlock (#53).
//
// Callers that build commands from arbitrary filenames (golden-lock-menu) always
// emit `lock -- <name>...`. Two guarantees follow: a name after `--` is a path
// even when it looks like a flag, and a `--` with no names after it is an arg
// error rather than the no-arg proposal-locks sweep.

import (
	"fmt"
	"os"
	"reflect"
	"testing"
)

// proposalRepo builds a repo whose proposal-locks list names one real file, so
// a call that wrongly falls through to the no-arg sweep would have something to
// lock.
func proposalRepo(t *testing.T) string {
	t.Helper()
	root := fakeRepo(t)
	writeFile(t, root, "core.go", "package core\n")
	writeFile(t, root, "golden-lock/proposal-locks/core.txt", "core.go\n")
	return root
}

// withStdinNotATerminal points os.Stdin at /dev/null for fn, so the large-sweep
// guard takes its non-interactive refusal instead of prompting.
func withStdinNotATerminal(t *testing.T, fn func() int) int {
	t.Helper()
	saved := os.Stdin
	devNull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	os.Stdin = devNull
	defer func() {
		os.Stdin = saved
		devNull.Close()
	}()
	return fn()
}

// The check runs before the root check, so this holds — and discriminates —
// with or without privilege: unprivileged, a fall-through would exit 4; as
// root, it would lock core.go and exit 0.
func TestEndOfOptions_NoPathsAfterDashDashIsArgError(t *testing.T) {
	root := proposalRepo(t)
	t.Chdir(root)

	cases := map[string]func() int{
		"lock --":              func() int { return runLock([]string{"--"}) },
		"lock -y --":           func() int { return runLock([]string{"-y", "--"}) },
		"unlock --":            func() int { return runUnlock([]string{"--"}) },
		"unlock --uid=501 --":  func() int { return runUnlock([]string{"--uid=501", "--"}) },
		"dispatch lock --":     func() int { return dispatch([]string{"lock", "--"}) },
		"dispatch bare --":     func() int { return dispatch([]string{"--"}) },
		"dispatch lockfile --": func() int { return dispatch([]string{"--lockfile", LockfilePath(root), "lock", "--"}) },
	}
	for name, run := range cases {
		t.Run(name, func(t *testing.T) {
			code := withSilencedStderr(t, run)
			if code != ExitWriteArgs {
				t.Errorf("%s: code = %d, want %d", name, code, ExitWriteArgs)
			}
		})
	}
}

func TestEndOfOptions_DashYAfterDashDashIsAPath(t *testing.T) {
	requireRoot(t)
	root := proposalRepo(t)
	writeFile(t, root, "-y", "flag-shaped name\n")
	chdirTo(t, root)

	if code := runLock([]string{"--", "-y"}); code != ExitWriteOK {
		t.Fatalf("lock -- -y = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{"--", "-y"}) })

	locked := verifiedOKPaths(t, root)
	if want := []string{"-y"}; !reflect.DeepEqual(locked, want) {
		t.Fatalf("locked %v, want %v (only the file named -y, no proposal sweep)", locked, want)
	}

	// unlock has no -y flag, so parsing it as one would fail with exit 5.
	if code := runUnlock([]string{"--", "-y"}); code != ExitWriteOK {
		t.Fatalf("unlock -- -y = %d, want %d", code, ExitWriteOK)
	}
}

// A `-y` after `--` must not switch off the large-sweep confirmation for the
// directory beside it.
func TestEndOfOptions_DashYAfterDashDashKeepsSweepGuard(t *testing.T) {
	requireRoot(t)
	root := fakeRepo(t)
	writeFile(t, root, "-y", "flag-shaped name\n")
	const total = lockSweepWarnThreshold + 5
	for i := 0; i < total; i++ {
		writeFile(t, root, fmt.Sprintf("big/f%03d.txt", i), "x\n")
	}
	chdirTo(t, root)

	code := withSilencedStderr(t, func() int {
		return withStdinNotATerminal(t, func() int {
			return runLock([]string{"--", "-y", "big"})
		})
	})
	if code != ExitWriteArgs {
		t.Fatalf("lock -- -y big = %d, want %d (sweep refused without confirmation)", code, ExitWriteArgs)
	}
	if _, verifyCode, _ := Verify(root); verifyCode != ExitVerifyLockfile {
		t.Errorf("verify after refused sweep = %d, want %d (nothing locked, no manifest)", verifyCode, ExitVerifyLockfile)
	}
}

// `golden-lock -y` with a file named -y on disk is the bare-path shorthand, so
// it locks that file — not the proposal list with the sweep prompt skipped.
func TestDispatch_BarePathFlagShapedNameIsAPath(t *testing.T) {
	requireRoot(t)
	root := proposalRepo(t)
	writeFile(t, root, "-y", "flag-shaped name\n")
	chdirTo(t, root)

	if code := dispatch([]string{"-y"}); code != ExitWriteOK {
		t.Fatalf("dispatch(-y) = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{"--", "-y"}) })

	locked := verifiedOKPaths(t, root)
	if want := []string{"-y"}; !reflect.DeepEqual(locked, want) {
		t.Fatalf("locked %v, want %v", locked, want)
	}
}
