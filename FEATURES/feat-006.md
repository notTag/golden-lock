# [006] Allow directories to be locked

- Status: open
- Created: 2026-07-06
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
- [ ] `golden-lock lock <dir>` locks every regular file under `<dir>` recursively
- [ ] Each file gets its own manifest entry (existing freeze → hash → immutable path reused per file)
- [ ] Symlinks (leaf or any parent component) are still refused, consistent with the current file flow
- [ ] `verify` and `unlock` correctly handle entries that originated from a directory lock
- [ ] Empty directory or a directory with only skippable entries reports clearly, not a silent no-op

## Notes
- Entry point: `runLock` in main.go:119 — currently treats each arg as a single writable file via `openWritableResolved`.
- Decide recursion vs single-level; recursion is the useful default. Skip `.git`/dotfiles consistent with existing listing logic (main.go:77).
- Directories themselves can't be frozen/hashed like files — only their contained regular files are locked.
