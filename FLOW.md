# golden-test — End-to-End Flow

The lifecycle of protecting core tests with `golden-test`, from install to CI gate.

---

## 1. Install

User installs the CLI (brew / clone + `go build -o golden-test .`) and makes the
`golden-test` binary available on PATH.

## 2. Discover golden tests

User runs the `/golden-test` skill against their repo. The skill:

- scans every test file,
- classifies each test **Core** vs **Supporting**,
- emits a table marking which tests should be immutable ("golden").

## 3. Confirm core functionality

User reviews the table and confirms the recommended core tests — the immutable
functionality that must never change.

## 4. Break out into golden files

Each confirmed core test is extracted into its own sibling file
(`<name>.golden.<ext>`, runner-valid per language). The build is kept green
before anything is locked.

## 5. Lock (root)

User runs:

```sh
sudo golden-test lock <file_test1> <file_test2> ...
```

Every listed file is hashed (SHA-256), recorded in `golden-test.lock`, and
root-locked (root-owned, `chmod 444`) — along with the `golden-test.lock`
manifest itself, which is the trust anchor.

## 6. Add the CI/commit-hook gate

User adds the verify step to their CI/CD or commit hooks:

```sh
golden-test verify
```

Then continues development as normal.

## 7. Enforcement during development (EACCES)

If an agent attempts to change a golden test, the write fails at the OS level
with a **permission-denied (`EACCES`)** error — the file is root-owned `444`.

The agent treats this denial as the signal: **the test is the spec.** It does
not edit the locked test; it fixes the implementation and continues down the
correct path, leaving existing functionality unchanged.

> v1 relies on the raw OS `EACCES` denial. There is no custom interception
> message — the immutability is enforced by file ownership + permissions, which
> holds against any tool or agent, not just one runner.

## 8. Verify at push (CI gate)

When the code is ready to push, the commit hook / pipeline fires
`golden-test verify`, which recomputes the hash of every locked test and
compares it to `golden-test.lock`:

- **hashes match** → pipeline step **passes**.
- **hashes mismatch** (or a locked file is missing/manifest absent) → pipeline
  **fails**.

### verify exit codes

| Code | Meaning |
|------|---------|
| 0 | all hashes match |
| 1 | hash mismatch |
| 2 | locked file missing |
| 3 | `golden-test.lock` absent or malformed |
