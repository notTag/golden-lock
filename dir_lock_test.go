package main

import (
	"os"
	"path/filepath"
	"testing"
)

// A directory input expands to every regular file under it (recursively),
// skipping dot-directories (.git) and dotfiles, consistent with the
// proposal-locks listing rules (feat-006).
func TestExpandLockTargetsRecursesAndSkipsDotEntries(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "core/a.go", "package a")
	writeFile(t, root, "core/sub/b.go", "package b")
	writeFile(t, root, "core/.hidden", "secret")
	writeFile(t, root, "core/.cache/junk", "junk")

	got, err := expandLockTargets([]string{filepath.Join(root, "core")})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	want := []string{"core/a.go", "core/sub/b.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}
}

// A symlink — whether an input itself or found during the walk — is skipped,
// never locked, matching the per-file flow's symlink refusal.
func TestExpandLockTargetsSkipsSymlinks(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "core/real.go", "package a")
	target := writeFile(t, root, "core/target.go", "package t")
	if err := os.Symlink(target, filepath.Join(root, "core", "link.go")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	got, err := expandLockTargets([]string{filepath.Join(root, "core")})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	// real.go and target.go are collected; link.go (the symlink) is not.
	want := []string{"core/real.go", "core/target.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}

	// A symlink passed directly as an input is also skipped, not followed.
	fromLink, err := expandLockTargets([]string{filepath.Join(root, "core", "link.go")})
	if err != nil {
		t.Fatalf("expandLockTargets(link): %v", err)
	}
	if len(fromLink) != 0 {
		t.Fatalf("symlink input should be skipped, got %v", fromLink)
	}
}

// An empty directory (or one holding only skippable entries) yields no files,
// so runLock can report a clear error instead of a silent no-op.
func TestExpandLockTargetsEmptyDirYieldsNothing(t *testing.T) {
	root := fakeRepo(t)
	if err := os.MkdirAll(filepath.Join(root, "empty"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	writeFile(t, root, "dotsonly/.only", "x")

	for _, dir := range []string{"empty", "dotsonly"} {
		got, err := expandLockTargets([]string{filepath.Join(root, dir)})
		if err != nil {
			t.Fatalf("expandLockTargets(%s): %v", dir, err)
		}
		if len(got) != 0 {
			t.Fatalf("%s should yield no files, got %v", dir, got)
		}
	}
}

// A file reached both explicitly and via a directory is de-duplicated to a
// single entry, so it is frozen and reported once.
func TestExpandLockTargetsDedupes(t *testing.T) {
	root := fakeRepo(t)
	file := writeFile(t, root, "core/a.go", "package a")

	got, err := expandLockTargets([]string{filepath.Join(root, "core"), file})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	want := []string{"core/a.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}
}
