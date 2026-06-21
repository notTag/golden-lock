# Testing

How to run the test suite, how to write new tests, and which tests guard core behavior.

## Running

```sh
go test ./...          # whole suite
go test -v ./...       # verbose, per-test names
go test -run TestVerify ./...   # one group by name regex
```

The suite is plain `go test` — no build tags, no external fixtures. Every test
builds its own fake repo under `t.TempDir()`, so runs are hermetic and immune to
ambient git state.

### Root-only tests

The privilege model splits the suite in two:

- **Read path** (hashing, manifest parse, `Verify`) needs no privilege and runs
  as any user.
- **Write path** (`LockFile`/`UnlockFile`, which `chown` targets to root and set
  the immutable flag) needs **real root**. Those tests call `t.Skip` when
  `os.Geteuid() != 0`, so an unprivileged `go test` reports them as skipped, not
  failed.

To exercise the write path end to end, run as root:

```sh
sudo go test ./...
```

Note the non-root *exit code* (`4`) is still asserted without root — the dispatch
layer is reached and checked before any privileged syscall fires.

## Test files

| File | Scope |
| --- | --- |
| `goldenlock_test.go` | Happy-path + exit-code coverage: hashing, manifest round-trips, `Verify` precedence, symlink rejection, lock/unlock, dispatch routing. |
| `rename_contract_test.go` | Regression tripwires + contract guards for the `golden-test → golden-lock` rename and the "lock any file, not just tests" generalization. |

## Exit codes asserted

Tests pin these as part of the CLI contract — changing a value should force a
deliberate edit to `rename_contract_test.go`, not a silent drift.

**Verify (read):**

| Code | Meaning |
| --- | --- |
| 0 | all hashes match |
| 1 | at least one hash mismatch |
| 2 | at least one listed file missing |
| 3 | `golden.lock` absent or malformed |

Precedence: absent/malformed lockfile (3) dominates; among per-entry results,
missing (2) outranks mismatch (1).

**Lock / unlock (write):**

| Code | Meaning |
| --- | --- |
| 0 | success |
| 4 | not root |
| 5 | bad arguments |
| 6 | I/O failure |

## Tests that guard core behavior

`rename_contract_test.go` holds the immovable contract. Two kinds:

- **Tripwires** pin a current literal — the program name, the manifest filename
  (`golden.lock`), the exit-code integers. They are *meant* to fail loudly when a
  rename touches that value, forcing a deliberate migration note instead of
  silent breakage of already-locked repos or CI scripts.
- **Contract guards** pin behavior that must survive any rename: the dispatch
  routing matrix, help listing every subcommand, and — central to feat-003 —
  that the verify/lock pipeline treats a non-test file exactly like a test file
  (nothing is gated on a `_test`-shaped name).

Treat these as golden. If one fails, decide whether the contract genuinely
changed before editing the assertion.

## Writing new tests

- Build the world with the `fakeRepo` helper (or `t.TempDir()` directly): it
  drops a `.git` marker so any `FindRepoRoot` fallback anchors to the temp root
  rather than walking into the real checkout.
- Prefer calling `Verify` and the manifest API with an **explicit root** —
  that path takes no privilege and is fully deterministic.
- For write-path coverage, mirror the existing pattern: `t.Skip` unless
  `os.Geteuid() == 0`, and assert the non-root exit code through the dispatch
  layer separately.
- Reject-symlink and precedence cases are the easy ones to forget — when adding
  a path that resolves files, add a symlink-rejection test alongside it.
