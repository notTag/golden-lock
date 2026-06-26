package main

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// relForRoot turns the absolute paths gatherProposalLocks returns back into
// repo-root-relative slash form, so assertions read in terms of the listed
// paths rather than temp-dir absolutes.
func relForRoot(t *testing.T, root string, absPaths []string) []string {
	t.Helper()
	out := make([]string, 0, len(absPaths))
	for _, abs := range absPaths {
		relPath, err := NormalizePath(root, abs)
		if err != nil {
			t.Fatalf("normalize %q: %v", abs, err)
		}
		out = append(out, relPath)
	}
	sort.Strings(out)
	return out
}

// gatherProposalLocks should read every list file under golden-lock/proposal-locks/,
// return the union of existing listed paths, skip blanks/missing/dotfiles, and
// dedupe.
func TestGatherProposalLocks(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "coreA.go", "package a")
	writeFile(t, root, "coreB.go", "package b")
	writeFile(t, root, "testA.go", "package t")

	// Two list files, a duplicate across them, a blank line, and a line that
	// points at a file that does not exist (must be skipped, not fatal).
	writeFile(t, root, "golden-lock/proposal-locks/core.txt", "coreA.go\ncoreB.go\n")
	writeFile(t, root, "golden-lock/proposal-locks/tests.txt", "\ntestA.go\ncoreA.go\nghost.go\n")
	// A dotfile in the dir must be ignored entirely.
	writeFile(t, root, "golden-lock/proposal-locks/.DS_Store", "\x00\x01garbage")

	got, err := gatherProposalLocks(root)
	if err != nil {
		t.Fatalf("gatherProposalLocks: %v", err)
	}

	want := []string{"coreA.go", "coreB.go", "testA.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("gathered %v, want %v", gotRel, want)
	}
}

// An absent proposal-locks/ dir is not an error — it yields no paths so the
// caller can emit the "lists none" arg error.
func TestGatherProposalLocksAbsentDir(t *testing.T) {
	root := fakeRepo(t)
	got, err := gatherProposalLocks(root)
	if err != nil {
		t.Fatalf("absent dir should not error: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("absent dir should yield no paths, got %v", got)
	}
}

// A line that escapes the repo root must be skipped, never returned.
func TestGatherProposalLocksRejectsEscape(t *testing.T) {
	root := fakeRepo(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	writeFile(t, root, "golden-lock/proposal-locks/bad.txt", outside+"\n")

	got, err := gatherProposalLocks(root)
	if err != nil {
		t.Fatalf("gatherProposalLocks: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("escaping path should be skipped, got %v", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
