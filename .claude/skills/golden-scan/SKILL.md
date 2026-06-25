---
name: golden-scan
version: 1.0.0
description: |
  Scan the current project (source AND tests) for the logic that constitutes
  core functionality — security invariants, auth/isolation, money/idempotency,
  data integrity, public-API contracts — and emit a ranked proposal of lock
  candidates. For each candidate, say why it is core, whether a test already
  guards it, and whether it warrants extraction into a `*.golden.*` sibling
  before locking. The skill RECOMMENDS only: it stops at the proposal and never
  locks or mutates anything. Hand the chosen candidates to `/golden-test` to
  extract and lock. Trigger on "/golden-scan", "scan for core functionality",
  "propose lock candidates", "what should I lock".
allowed-tools:
  - Read
  - Bash
  - Glob
  - Grep
  - AskUserQuestion
---

# golden-scan: propose what is core enough to lock

Companion to `golden-test`. Where `golden-test` takes a *known* set of core
tests and extracts + freezes them, `golden-scan` answers the question that comes
first: **out of everything in this repo, what is core enough to protect?**

It widens the lens from tests to **all** code. A security invariant can live in
a source function with no test guarding it at all — that gap is exactly what a
lock candidate scan should surface, because unguarded core logic is the most
dangerous kind.

`golden-scan` is **read-only and non-destructive by construction**. It produces a
ranked proposal and stops. Nothing is moved, written, or locked. The user reads
the proposal, picks candidates, and runs `/golden-test` to act.

The rule the whole repo enforces still holds: **a golden test is the spec. Fix
the implementation, never the test.** `golden-scan` decides *what deserves to
become* that spec.

## Pipeline

1. SCAN — enumerate source files and test files across the project tree.
2. CLASSIFY — score each item Core vs Supporting on core-functionality signals.
3. COVERAGE — for each Core item, determine if a test already guards it.
4. RANK — order Core candidates by risk (severity × exposure × coverage gap).
5. PROPOSE — emit the ranked table + extraction recommendations, then STOP.

Steps 1–5 are all read-only. The skill never advances past PROPOSE on its own.

---

## 1. SCAN — source and tests

Find the repo root first: `git rev-parse --show-toplevel` (fall back to walking
up for `.git`). All paths in the proposal are repo-root-relative.

Enumerate two sets, by language convention (a repo may mix several):

**Test files** (same conventions as `golden-test`):

| Language | Test file glob | Test-case marker |
|---|---|---|
| Go | `*_test.go` | `func Test…`, `func Fuzz…` |
| JS/TS | `*.test.*`, `*.spec.*`, `__tests__/**` | `test(`, `it(`, `describe(` |
| Python | `test_*.py`, `*_test.py`, `tests/**` | `def test_`, `class Test` |
| Rust | `tests/**`, inline `#[cfg(test)]` | `#[test]`, `#[tokio::test]` |
| Ruby | `*_spec.rb`, `*_test.rb` | `it `, `describe ` |
| Java/Kotlin | `*Test.*`, `*Tests.*` | `@Test` |

**Source files**: every non-test source file for the detected language(s) —
`*.go`, `*.ts`/`*.js`, `*.py`, `*.rs`, `*.rb`, `*.java`/`*.kt`, etc. The unit of
a candidate is a **function / method / class**, not the whole file.

Use `Glob` for files and `Grep` (or fff `grep`) for markers. Skip vendored and
generated dirs (`vendor/`, `node_modules/`, `.git/`, `dist/`, `build/`,
`target/`, `.next/`, `__pycache__/`) and obvious generated files (`*.pb.go`,
`*_generated.*`, `*.min.js`). Build a list of `{file, symbol, kind, line}` where
`kind ∈ {source, test}`.

## 2. CLASSIFY — is it core?

A **core** item is one where a silently broken or weakened invariant is a
security or correctness incident. Score each item on the signals below; 2+
strong signals → Core. These mirror `golden-test`'s categories so the two
skills agree on what "core" means.

**Strong signals (name or body):**
- Security / access control: `auth`, `authz`, `permission`, `isolation`,
  `tenant`, `rbac`, `privilege`, `sandbox`, `escalat`, `token`, `session`.
- Money / correctness-critical: `payment`, `idempoten`, `balance`, `ledger`,
  `charge`, `refund`, `tax`, `invoice`.
- Data & crypto integrity: `hash`, `digest`, `checksum`, `signature`, `verify`,
  `sha256`, `merkle`, `immutab`, `encrypt`, `sign`.
- Contract surface: public/exported API handlers, serialization formats, wire
  protocols, schema/migration boundaries, exact-output assertions.
- Regression guards explicitly tied to a past incident / CVE / bug number.

**Weak signals (Supporting, churn-prone):** UI rendering, formatting/log
strings, timing, fixtures, flaky network, scaffolding, getters/setters, helpers,
`main`/wiring/glue.

For each item record: **Classification** (Core / Supporting), the matched
**signal category**, and a one-line **Rationale** for why it is core.

## 3. COVERAGE — is it already guarded?

This is what separates `golden-scan` from a plain test classifier. For each
**Core source** item, decide whether an existing test exercises it:

- Grep test files for the symbol name, and for its package/module path.
- A Core source symbol referenced by name in a test file → **Covered**.
- A Core source symbol with no test reference → **Uncovered** (the dangerous
  case — core logic with no guarding test).

Tag every Core item with one of:
- `Covered` — a test already asserts on it (lock the *test*).
- `Uncovered` — core logic, no guarding test (write a test first, then lock it).
- `Is-test` — the item is itself a Core test (the `golden-test` case).

## 4. RANK — order by risk

Rank Core candidates high→low by a simple risk read:

- **Severity** of the category (security/crypto/money > contract > integrity-misc).
- **Exposure** — exported/public symbol or external entry point > internal.
- **Coverage gap** — `Uncovered` ranks ABOVE `Covered` at equal severity, because
  unguarded core logic is the bigger hole.

No need for a precise score — a stable, defensible ordering is enough. Note ties
rather than inventing precision.

## 5. PROPOSE — the table, then stop

Print one row per Core candidate, ranked:

```
| Rank | Candidate | File:Line | Kind | Category | Coverage | Extract? | Rationale |
|------|-----------|-----------|------|----------|----------|----------|-----------|
```

- **Kind** — source / test.
- **Coverage** — Covered / Uncovered / Is-test.
- **Extract?** — recommend extraction into a `<name>.golden.<ext>` sibling when:
  - the item is a Core **test sharing a file with Supporting tests** (the file
    gets frozen whole, so the churny neighbors must move out — same reasoning as
    `golden-test`), or
  - the item is `Uncovered` core logic — recommend **"write a guarding golden
    test first"**, naming the sibling that test would live in.
  Mark "lock in place" for a Core test whose file is already 100% core.

Follow the table with a short, plain-language plan:
- which **Covered** core tests are ready to hand to `/golden-test` as-is,
- which **Uncovered** items need a test written before anything can be locked,
- the eventual `golden-lock lock` command for the ready set.

Then **STOP**. End with an explicit handoff line, e.g.:

> Proposal only — nothing has been changed or locked. To act on a subset, run
> `/golden-test` (it will re-confirm before extracting or locking).

## Confirmation & guardrails

- `golden-scan` performs **no** mutation, ever. It does not write, move, `chmod`,
  `chown`, `chflags`, `chattr`, or `sudo`. If asked to "just lock it too," decline
  and route the user to `/golden-test` — keeping scan and lock in separate skills
  is the whole point of this design.
- A single `AskUserQuestion` is allowed at the END — only to ask whether to hand
  the chosen candidates to `/golden-test` now. Picking candidates there does not
  lock anything; it just frames the next skill's input.
- Be honest about uncertainty: a 2-signal match is a *candidate*, not a verdict.
  Flag borderline items as `Review` rather than silently promoting or dropping
  them.
- Coverage detection is name-based and therefore approximate; say so. A symbol
  shadowed by a same-named test in an unrelated package is a false `Covered` —
  note when a match is weak.
