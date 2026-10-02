package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// confirmLargeSweep accepts only an affirmative y/yes (case-insensitive); every
// other line — including empty input and EOF — is a No, so an accidental Enter
// or a closed pipe never green-lights a repo-wide freeze.
func TestConfirmLargeSweep(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"y\n", true},
		{"Y\n", true},
		{"yes\n", true},
		{"YES\n", true},
		{"  y  \n", true},
		{"n\n", false},
		{"no\n", false},
		{"\n", false}, // bare Enter
		{"", false},   // EOF / closed pipe
		{"maybe\n", false},
		{"yep\n", false},
	}
	for _, c := range cases {
		var out strings.Builder
		got := confirmLargeSweep(&out, strings.NewReader(c.input), []string{"."}, 1234)
		if got != c.want {
			t.Errorf("confirmLargeSweep(%q) = %v, want %v", c.input, got, c.want)
		}
		if !strings.Contains(out.String(), "1234 files") {
			t.Errorf("prompt for %q missing file count; got %q", c.input, out.String())
		}
	}
}

// lock_dot_selection_test.go — validates the SELECTION CONTRACT of `lock .`
// (feat-006 repo-root expansion): given a realistic project tree, pin exactly
// which files a bare `lock .` would freeze and which it leaves alone. This is
// the use-case validation for the repo-root form — it documents the behavior an
// operator is trusting when they point the tool at the whole working directory.
//
// The headline finding it locks in: expansion walks the FILESYSTEM, not git —
// it does NOT consult .gitignore. A non-dot ignored directory (node_modules/,
// build/) IS swept in. That is a genuine footgun for `lock .` and this test
// makes it explicit rather than incidental; if we ever decide `lock .` should
// honor .gitignore, this test is where the contract change gets recorded.
func TestLockDot_SelectionContract(t *testing.T) {
	root := fakeRepo(t) // creates the .git/ dir marker

	// Real source the operator means to lock.
	writeFile(t, root, "src/main.go", "package main\n")
	writeFile(t, root, "README.md", "# project\n")

	// Things that MUST be skipped.
	writeFile(t, root, ".env", "SECRET=1\n")                         // dotfile
	writeFile(t, root, ".gitignore", "node_modules/\nbuild/\n")      // dotfile
	writeFile(t, root, "golden-lock/proposal-locks/list.txt", "x\n") // internal state dir
	if err := os.Symlink("main.go", filepath.Join(root, "src", "link.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	// The FOOTGUN: gitignored, but non-dot, so they ARE swept in.
	writeFile(t, root, "node_modules/pkg/index.js", "module.exports={}\n")
	writeFile(t, root, "build/out.o", "\x00binary\n")

	t.Chdir(root) // so "." lstats as the repo root
	got, err := expandLockTargets(root, []string{"."})
	if len(err) != 1 || err[0].err != ErrSymlink {
		t.Fatalf("expected one reported symlink, got %v", err)
	}

	// Exact contract: nested source + top-level file + the gitignored trees,
	// but NEVER dotfiles/.git, the golden-lock/ state dir, or the symlink.
	want := []string{
		"README.md",
		"build/out.o",
		"node_modules/pkg/index.js",
		"src/main.go",
	}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("`lock .` selected %v,\n                want %v", gotRel, want)
	}
}
