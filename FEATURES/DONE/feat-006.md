# [006] Allow directories to be locked

- Status: Done
- Created: 2026-07-06
- Done: 2026-07-10
- Bump: minor

## What
Let `golden-lock lock <dir>` accept a directory path, not just individual
regular files. When a directory is given, recurse into it and lock every
regular file found underneath, reusing the existing per-file
freeze-then-hash-then-immutable flow. Each file is still recorded as its own
manifest entry.

## Why
Today you must enumerate every file by hand (or list them in a
proposal-locks file) to lock a test suite. Core functionality often lives in
a whole directory (e.g. a `testdata/` or `*.golden/` tree). Pointing the tool
at the directory is the obvious ergonomic and removes a class of "forgot to
lock the new file" gaps.

## Done When
- [x] `golden-lock lock <dir>` locks every regular file under `<dir>` recursively
- [x] Each file gets its own manifest entry (existing freeze → hash → immutable path reused per file)
- [x] Symlinks (leaf or any parent component) are still refused, consistent with the current file flow
- [x] `verify` and `unlock` correctly handle entries that originated from a directory lock
- [x] Empty directory or a directory with only skippable entries reports clearly, not a silent no-op

## Notes
- Implemented by the feat-006 series ending in PR #43, with recursive target
  expansion in `main.go` and unit plus root-gated end-to-end coverage in
  `dir_lock_test.go` and `dir_lock_integration_test.go`.
- Directories themselves are not frozen or hashed; each contained regular file
  follows the existing per-file lock pipeline and receives its own manifest entry.
