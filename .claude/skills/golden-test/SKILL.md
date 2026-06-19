---
name: golden-test
version: 1.0.0
description: |
  Scan a repo for its tests, classify which ones guard core functionality
  (security invariants, auth/isolation, money/idempotency, data integrity,
  public-API contracts), and emit a table marking which should be immutable
  "golden" tests. Recommends core tests that should be broken out into their
  own files, and on confirmation extracts each into a `<name>.golden.<ext>`
  sibling and locks it with the `golden-test` binary (root-owned 444 +
  filesystem immutable flag + SHA-256 manifest). Trigger on "/golden-test", "find golden tests",
  "which tests should be immutable", "lock my core tests".
allowed-tools:
  - Read
  - Write
  - Edit
  - Bash
  - Glob
  - Grep
  - AskUserQuestion
---

# golden-test: designate & enforce immutable core tests

Companion workflow for the `golden-test` CLI (this repo). The CLI makes a test
file immutable so an AI coding agent can't game "all tests pass" by editing the
assertion. This skill is the human/agent-facing front end: it **finds** the
tests worth protecting, **classifies** them, **breaks** the core ones into their
own sibling files, and **locks** them.

The hard rule the whole repo exists to enforce: **a golden test is the spec. Fix
the implementation, never the test.**

## Pipeline

1. SCAN — enumerate every test in the repo.
2. CLASSIFY — score each test Core vs Supporting.
3. REPORT — table of tests, immutability verdict, and break-out recommendations.
4. CONFIRM — ask the user which core tests to act on (nothing destructive before this).
5. BREAK OUT — extract each chosen core test into a `<name>.golden.<ext>` sibling, keep the build green.
6. LOCK — `sudo golden-test lock` the new files, then `golden-test verify`.

Run 1–3 read-only and always show the table before touching anything.

---

## 1. SCAN

Find the repo root first: `git rev-parse --show-toplevel` (fall back to walking
up for `.git`). All paths in the report are repo-root-relative.

Detect test files by language convention (a repo may mix several):

| Language | Test file glob | Test-case marker |
|---|---|---|
| Go | `*_test.go` | `func Test…`, `func Fuzz…`, `func Benchmark…` |
| JS/TS | `*.test.*`, `*.spec.*`, `__tests__/**` | `test(`, `it(`, `describe(` |
| Python | `test_*.py`, `*_test.py`, `tests/**` | `def test_`, `class Test` |
| Rust | `tests/**`, inline `#[cfg(test)]` | `#[test]`, `#[tokio::test]` |
| Ruby | `*_spec.rb`, `*_test.rb` | `it `, `describe `, `def test_` |
| Java/Kotlin | `*Test.*`, `*Tests.*` | `@Test` |

Use `Glob` for files and `Grep` (or fff `grep`) for case markers. Build a list of
`{file, test_name, line}`. Skip vendored/generated dirs (`vendor/`,
`node_modules/`, `.git/`, `dist/`, `build/`, `target/`).

## 2. CLASSIFY — is it core?

A **core** test guards an invariant where a silently-edited assertion is a
security or correctness incident. Score each test on these signals; 2+ strong
signals → Core.

**Strong signals (name or body):**
- Security / access control: `auth`, `authz`, `permission`, `isolation`,
  `tenant`, `rbac`, `privilege`, `sandbox`, `escalat`.
- Money / correctness-critical: `payment`, `idempoten`, `balance`, `ledger`,
  `charge`, `refund`, `tax`.
- Data & crypto integrity: `hash`, `digest`, `checksum`, `signature`, `verify`,
  `sha256`, `merkle`, `immutab`.
- Contract surface: public API responses, serialization formats, wire protocols,
  exact-output / snapshot assertions (`golden`, `snapshot`, exact-string `Equal`).
- Regression guards explicitly tied to a past incident / CVE / bug number.

**Weak signals (supporting, churn-prone):** UI rendering, formatting/log
strings, timing, fixtures-heavy integration, flaky network, scaffolding,
`TestMain`, helpers.

For each test record: **Classification** (Core / Supporting), and **Immutable?**
(Yes for Core, No for Supporting) plus a one-line **Rationale**.

## 3. REPORT — the table

Print one row per test:

```
| Test | File:Line | Classification | Immutable | Break out? | Rationale |
|------|-----------|----------------|-----------|------------|-----------|
```

Then a **Break-out recommendations** section. Recommend break-out when a **Core**
test shares a file with Supporting tests. Reason: the CLI locks whole files
(`chmod 444`, root-owned). You cannot keep iterating on churny supporting tests
in a file you've frozen — so each immutable test wants its own file. A file that
is already 100% core can be locked in place (note it as "lock in place, no
break-out needed").

End the report with a short plan: which files get created, which existing tests
move, and the eventual `golden-test lock` command.

## 4. CONFIRM

Use `AskUserQuestion` before any file mutation. Offer: break out + lock all
recommended / pick a subset / report only. Never write, move, or lock without an
explicit yes. Locking requires `sudo` — surface that up front.

## 5. BREAK OUT → `<name>.golden.<ext>`

For each chosen core test, create a sibling file next to its origin. The naming
rule is **insert `.golden` before the test extension**, and the result MUST stay
a valid test file for that language's runner:

| Language | Origin | Golden sibling |
|---|---|---|
| Go | `idempotency_test.go` | `idempotency.golden_test.go` (still ends `_test.go` ✅) |
| JS/TS | `auth.test.ts` | `auth.golden.test.ts` |
| Python | `test_hash.py` | `test_hash_golden.py` (still matches `test_*` ✅) |
| Rust | `tests/isolation.rs` | `tests/isolation.golden.rs` |
| Ruby | `payments_spec.rb` | `payments_golden_spec.rb` |

> The literal pattern is `<basename>.golden.<ext>`; for Go and Python the suffix
> token is adjusted (`.golden_test.go`, `_golden.py`) so the file is still picked
> up as a test. State the exact filename you'll use before creating it.

Extraction steps per test:
1. Create the golden sibling in the **same package/module** as the origin.
2. Move the test function(s) **and only the imports/helpers they need** into it.
   If a helper is shared with tests left behind, keep it in the origin (or a
   shared `_test` helper file) — don't duplicate it into a file you're about to
   freeze.
3. Remove the moved test(s) from the origin file.
4. Re-run the test suite (`go test ./...`, `npm test`, `pytest`, `cargo test`,
   etc.) to confirm both files compile and pass. Do not proceed on a red build —
   you'd be locking a broken or empty test.

## 6. LOCK & VERIFY

Once the golden siblings are green, freeze them with this repo's CLI:

```sh
sudo golden-test lock <each new .golden file>   # root-own + chmod 444 + immutable flag + record SHA-256 in golden-test.lock
golden-test verify                              # read-only gate, exit 0 == all hashes match
```

`lock` also sets the filesystem immutable flag (`chflags schg` / `chattr +i`),
which is what actually blocks tampering: `chmod 444` alone does not stop
replace-by-rename (the write-temp-then-rename most editors and agent file tools
use). On a filesystem that can't store the flag, `lock` prints a per-file warning
and falls back to detection-only (the manifest + CI `verify` still catch drift).

`lock` also locks `golden-test.lock` itself (root-owned 444) — that's the trust
anchor. After locking, remind the user to:
- commit `golden-test.lock`,
- add `golden-test verify` to CI as a hard gate,
- pin the golden paths in `.gitattributes` (`* text=auto eol=lf`) so checkout
  byte-rewrites don't cause a spurious mismatch.

If `golden-test` isn't on PATH, build it first: `go build -o golden-test .` in the
repo root (or point at `./golden-test`).

## Guardrails

- Read-only through step 3; nothing is moved or locked without the step-4 yes.
- Never `chmod`/`chown`/`chflags`/`chattr`/`sudo`-around a file the tool already
  locked — if you hit `EACCES` (in-place write) or `EPERM` (rename-replace) on a
  test, that test is the spec; fix the code.
- Don't lock a file with a red or empty suite. Verify green first.
- Keep the moved test byte-for-byte identical to the original assertion — the
  break-out relocates, it does not rewrite, the invariant.
