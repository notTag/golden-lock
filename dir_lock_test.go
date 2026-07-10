package main

import (
	"os"
	"path/filepath"
	"sort"
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

	got, err := expandLockTargets(root, []string{filepath.Join(root, "core")})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	want := []string{"core/a.go", "core/sub/b.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}
}

// A repo-root `lock .` must not sweep in Golden Lock's own state directory
// (golden-lock/manifest + proposal-locks); locking the manifest as user content
// would leave the rewritten live manifest inconsistent with its recorded hash
// and break verify (Codex review P1).
func TestExpandLockTargetsExcludesStateDir(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "core/a.go", "package a")
	writeFile(t, root, "golden-lock/golden.lock", "sha256  core/a.go")
	writeFile(t, root, "golden-lock/proposal-locks/core.txt", "core/a.go")

	got, err := expandLockTargets(root, []string{root})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	// Only the real source file; nothing under golden-lock/.
	want := []string{"core/a.go"}
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

	got, err := expandLockTargets(root, []string{filepath.Join(root, "core")})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	// real.go and target.go are collected; link.go (the symlink) is not.
	want := []string{"core/real.go", "core/target.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}

	// A symlink passed directly as an input is also skipped, not followed.
	fromLink, err := expandLockTargets(root, []string{filepath.Join(root, "core", "link.go")})
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
		got, err := expandLockTargets(root, []string{filepath.Join(root, dir)})
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

	got, err := expandLockTargets(root, []string{filepath.Join(root, "core"), file})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	want := []string{"core/a.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}
}

// The same file reached via a relative directory and an absolute argument must
// collapse to one target, or the second pass reopens an already-frozen file and
// can leave it frozen but unpublished (Codex review P1).
func TestExpandLockTargetsDedupesRelAndAbs(t *testing.T) {
	root := fakeRepo(t)
	abs := writeFile(t, root, "core/a.go", "package a")

	t.Chdir(root) // so "core" resolves relative to the repo root
	got, err := expandLockTargets(root, []string{"core", abs})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	want := []string{"core/a.go"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}
}

// An explicitly named dot-directory with a trailing separator (`lock .github/`)
// must behave like `lock .github` — the root is not mistaken for a nested
// dot-entry and skipped (Codex review P2).
func TestExpandLockTargetsDotDirTrailingSlash(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, ".github/workflows/ci.yml", "on: push")

	t.Chdir(root)
	got, err := expandLockTargets(root, []string{".github/"})
	if err != nil {
		t.Fatalf("expandLockTargets: %v", err)
	}

	want := []string{".github/workflows/ci.yml"}
	if gotRel := relForRoot(t, root, got); !equalStrings(gotRel, want) {
		t.Fatalf("expanded %v, want %v", gotRel, want)
	}
}

// A directory unlock argument expands to exactly the manifest entries beneath
// it — untracked files on disk and entries outside the directory are ignored
// (feat-006).
func TestExpandUnlockTargetsDirExpandsFromManifest(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "core/a.go", "package a")
	writeFile(t, root, "core/sub/b.go", "package b")
	writeFile(t, root, "core/untracked.go", "package u") // on disk, NOT locked
	m := &Manifest{Root: root}
	m.Upsert("core/a.go", "h1")
	m.Upsert("core/sub/b.go", "h2")
	m.Upsert("other/c.go", "h3") // outside core — must not be unlocked

	got, err := expandUnlockTargets(m, root, []string{filepath.Join(root, "core")})
	if err != nil {
		t.Fatalf("expandUnlockTargets: %v", err)
	}
	sort.Strings(got)
	want := []string{"core/a.go", "core/sub/b.go"}
	if !equalStrings(got, want) {
		t.Fatalf("expanded %v, want %v", got, want)
	}
}

// A directory with no locked files under it is skipped, not an error, so it
// yields nothing (the caller turns an all-empty result into a clear message).
func TestExpandUnlockTargetsEmptyDirSkipped(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "core/a.go", "package a") // exists on disk, but not locked
	m := &Manifest{Root: root}
	m.Upsert("other/c.go", "h1")

	got, err := expandUnlockTargets(m, root, []string{filepath.Join(root, "core")})
	if err != nil {
		t.Fatalf("expandUnlockTargets: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("dir with no locked files should yield nothing, got %v", got)
	}
}

// An explicitly-named file that is not locked is still a hard argument error —
// unlock never fabricates work for a path it did not lock.
func TestExpandUnlockTargetsUntrackedFileErrors(t *testing.T) {
	root := fakeRepo(t)
	file := writeFile(t, root, "core/a.go", "package a")
	m := &Manifest{Root: root} // empty — a.go is not locked

	if _, err := expandUnlockTargets(m, root, []string{file}); err == nil {
		t.Fatal("expected an error for an untracked file argument, got nil")
	}
}

// A directory argument that points at the repo root (e.g. `unlock .`) expands
// to every manifest entry — the symmetric inverse of a repo-wide `lock .` —
// even though NormalizePath rejects the root itself (Codex review P2).
func TestExpandUnlockTargetsRepoRootExpandsAll(t *testing.T) {
	root := fakeRepo(t)
	m := &Manifest{Root: root}
	m.Upsert("core/a.go", "h1")
	m.Upsert("other/c.go", "h2")

	t.Chdir(root) // so "." lstats as the repo root
	got, err := expandUnlockTargets(m, root, []string{"."})
	if err != nil {
		t.Fatalf("expandUnlockTargets: %v", err)
	}
	sort.Strings(got)
	want := []string{"core/a.go", "other/c.go"}
	if !equalStrings(got, want) {
		t.Fatalf("expanded %v, want %v", got, want)
	}
}

// A path named both directly and via its parent directory is unlocked once.
func TestExpandUnlockTargetsDedupes(t *testing.T) {
	root := fakeRepo(t)
	file := writeFile(t, root, "core/a.go", "package a")
	m := &Manifest{Root: root}
	m.Upsert("core/a.go", "h1")

	got, err := expandUnlockTargets(m, root, []string{filepath.Join(root, "core"), file})
	if err != nil {
		t.Fatalf("expandUnlockTargets: %v", err)
	}
	want := []string{"core/a.go"}
	if !equalStrings(got, want) {
		t.Fatalf("expanded %v, want %v", got, want)
	}
}
