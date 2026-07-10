# [003] Rename project to golden-lock and generalize to any file

- Status: Done
- Created: 2026-06-20
- Done: 2026-07-10
- Priority: high
- Bump: major

## What
Rename the project from `golden-test` to `golden-lock`, and broaden its scope
from locking only test files to locking *any* file the end user chooses. The
core mechanism (root-owned 444 + filesystem immutable flag + SHA-256 manifest)
stays the same; what changes is the name and the framing — a file no longer has
to be a test to become a locked "golden" artifact.

## Why
The locking machinery is already file-agnostic; only the surrounding naming,
docs, and CLI framing assume "tests". Users want to lock any critical file
(configs, fixtures, schemas, key source files), not just tests. The name
`golden-test` no longer describes what the tool does, so it becomes
`golden-lock`.

## Done When
- [x] Binary/CLI renamed `golden-test` → `golden-lock` (build output + any
      embedded usage/help strings)
- [x] Go module path and package identifiers updated to `golden-lock`
- [x] Repo directory + git remote / module references updated
- [x] User-facing CLI accepts an arbitrary file path to lock (not gated to
      detected test files)
- [x] Docs (README, PRD, ARCH, FLOW, definitions) reworded from "test files"
      to "any file"; locking-model semantics unchanged
- [x] Existing golden-test invariants (immutable flag, 444 perms, manifest,
      verify) still pass on a locked non-test file
- [x] `.golden.<ext>` sibling naming convention generalized beyond test files

## Notes
- Mechanism is already file-agnostic (see hash.go, immutable.go, lockfile.go) —
  this is primarily a rename + reframe, not a re-architecture.
- The `/golden-test` skill (description, triggers, sibling-extraction logic) and
  any agent-integration docs referencing "golden tests" need updating too.
- Large blast radius across docs and identifiers — consider a dedicated branch
  per the project's GSD workflow rules.
