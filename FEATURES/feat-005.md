# [005] golden-lock/ dir — bulk lock from proposal-locks lists

- Status: open
- Created: 2026-06-25

## What
Add a `golden-lock/` directory that holds golden-lock's state: the manifest
(`golden-lock/golden.lock`) and a `golden-lock/proposal-locks/` subdir. Each
file under proposal-locks/ is a plain list of paths, one per line (e.g.
`golden-lock/proposal-locks/tests.txt` → `testA.go\ntestB.go`,
`.../configs.txt` → `prod.yaml\nstaging.yaml`). Running `golden-lock lock` with
no arguments scans proposal-locks/, reads every file line by line, resolves each
line to a file, and locks all of them in one pass.

## Why
Lets the user declare what to lock declaratively, grouped by concern, instead of
passing paths on the command line one at a time. The dir becomes the source of
truth for the lock set — edit a list, re-run `golden-lock lock`. Co-locating the
manifest under golden-lock/ keeps all of the tool's state in one place.

## Done When
- [x] `golden-lock lock` with no params reads every file under `golden-lock/proposal-locks/`.
- [x] Each non-empty line is treated as a path and locked.
- [x] Multiple list files are all processed (no single-file assumption).
- [x] Blank lines are skipped; a line pointing at a missing file is reported, not fatal.
- [x] The manifest lives at `golden-lock/golden.lock`.

## Notes
Built on top of the golden-test → golden-lock rename (PR #9, landed on main via
PR #12). `LockfileName` is unchanged (`golden.lock`), so the name-stability
tripwire still holds; only the path gained a `golden-lock/` segment, which the
symlink-free dir-fd walk verifies like any other component.
