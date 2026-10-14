package main

// bare_path_dispatch_test.go — pins the `golden-lock <path>...` shorthand.
//
// A first argument that stats as an existing path is routed to lock; anything
// else (a mistyped verb, a flag, a nonexistent file) stays an unknown command.
// Non-root is the discriminator here: reaching runLock means exit 4, while the
// unknown-command arm returns exit 5 without ever touching the filesystem.

import (
	"os"
	"testing"
)

func TestDispatch_BarePathRunsLock(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: exit 4 no longer marks the lock path as reached")
	}
	root := fakeRepo(t)
	writeFile(t, root, "golden/expected.json", `{"answer":42}`)
	t.Chdir(root)

	code := withSilencedStderr(t, func() int {
		return dispatch([]string{"golden/expected.json"})
	})

	if code != ExitWriteNotRoot {
		t.Errorf("bare existing path: code = %d, want %d (lock reached, refused for lack of root)", code, ExitWriteNotRoot)
	}
}

func TestDispatch_BarePathRejectsNonPaths(t *testing.T) {
	root := fakeRepo(t)
	t.Chdir(root)

	cases := map[string][]string{
		"mistyped verb":    {"verfiy"},
		"unknown flag":     {"--dry-run"},
		"nonexistent file": {"golden/absent.json"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			code := withSilencedStderr(t, func() int { return dispatch(args) })
			if code != ExitWriteArgs {
				t.Errorf("dispatch(%q): code = %d, want %d", args, code, ExitWriteArgs)
			}
		})
	}
}
