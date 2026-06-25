# golden-lock: Immutable File Enforcement CLI

> Single source of truth. Consolidated from the original PRD + critique + revisions.
> Enforcement rests on a **root-owned trust anchor**: the locked files and the
> manifest (`golden.lock`) are root-owned `444`. Writes require sudo; the CI gate is read-only.

## Problem Statement

AI coding agents (Claude Code, Codex, etc.) optimize for "all tests pass." When an agent can modify test files, altering assertions is often the lowest-energy path to green — the agent games the reward function instead of fixing the implementation. The user discovers the infraction later, wastes a full cycle reverting and re-running, and loses trust in the workflow.

There is no standard tooling to designate certain files as immutable and enforce that immutability at both the local development layer (fast feedback for agents) and the CI layer (hard merge gate). Tests are the flagship case; the same need applies to any file that encodes an invariant.

## Solution

A standalone CLI — `golden-lock` — that:

1. Maintains a `golden.lock` manifest of protected files and their SHA-256 hashes.
2. Locks the protected files **and the manifest itself** as root-owned, read-only (`444`), so an unprivileged agent gets an immediate `EACCES` when it reaches for either — it cannot `chmod` around a file it does not own.
3. Provides a read-only, privilege-free `verify` command that exits non-zero on any tamper — the CI merge gate.

> **Architecture note:** the enforcement core is a generic immutable-file enforcer (SHA-256 manifest + root-`444` + read-only verify). Nothing in it is test-specific. Immutable test files were the first use case and remain the flagship one; the tool locks **any** file you choose — configs, schemas, migrations, fixtures. (The `/golden-test` skill that auto-discovers core *tests* is a convenience helper layered on top, not the tool's boundary.)

## Core Concepts

- **Locked files** encode invariants — if their contents change unexpectedly, something has gone wrong. Examples: a tenant-isolation test, a payment-idempotency test, a production config, a DB migration, a security policy.
- **The manifest is the trust anchor.** Security comes from `golden.lock` being un-writable by the agent, not from locking the files. An agent that modifies a locked file changes its hash; to hide it the agent must rewrite `golden.lock`, which it cannot. CI then catches the drift.
- **Two complementary layers:**
  - **Local (fast feedback):** the locked files + `golden.lock` are root-owned `444`. The agent hits permission denied mid-loop, before any commit, and redirects to fixing the implementation. Drift-prevention layer.
  - **CI (hard gate):** `verify` recomputes SHA-256 and compares to `golden.lock`. Read-only, no privileges. Authoritative cross-machine merge gate.
- **Complementary, not redundant.** Local lock = fast signal; CI verify = authoritative signal. Without the local layer you are still correct at merge time, but the agent burns a full iteration before CI says no — the exact wasted cycle this tool kills.
- **Agent auto-modes do not bypass OS permissions.** Auto modes skip the agent's confirmation UX, not kernel access controls. A root-owned `444` file is unwritable by a non-root agent regardless of autonomy settings.

## Threat Model

**Attacker:** an unprivileged local process (the agent) running as a normal user, distinct from the file owner (root).

**Guaranteed under that assumption:**
- Agent cannot modify a locked file (root-owned `444`).
- Agent cannot rewrite `golden.lock` to launder a hash (same).
- Any tampering therefore surfaces as a hash mismatch at CI `verify`.

**Out of the guarantee — local layer degrades to a speed bump when:**
- **The agent runs as root** (common in containers/sandboxes). Root chowns/chmods freely. CI `verify` + branch protection is then the only real gate.
- **A machine never ran `sudo golden-lock apply`** (fresh clone, teammate who skipped setup). Files return dev-owned `644`. CI still catches it.

**Defense-in-depth for the merge gate (recommended, not enforced by this tool):** put `golden.lock` under CODEOWNERS with required review + branch protection, so even a poisoned-but-committed manifest needs human sign-off.

## User Workflow

Three stages: a one-time locking ceremony, the steady-state dev loop, and the sanctioned unlock.

### Setup (one-time locking ceremony)
1. **Discover** — the `/golden-test` skill scans the repo and proposes candidate tests by core-functionality signal (auth, tenant isolation, payments, PII). Output: ranked list + rationale. *(A convenience helper for the test use case; it never auto-locks. Locking any other file is a direct `lock` away.)*
2. **Confirm** — user accepts/edits the set. Human judgment gate.
3. **Lock** — `sudo golden-lock lock <files…>` → hashes each, writes `golden.lock`, root-owns `444` the files + manifest.
4. **Wire the gate** — add `golden-lock verify` to CI and pin golden paths in `.gitattributes`. Local lock alone is bypassable; CI verify is the hard wall.
5. **Wire the loop** — drop the corrective message into CLAUDE.md and the Ralph-loop pre-acceptance `verify`, so the agent reads EACCES as "fix the impl," not "find a workaround."

### Steady state (continue development as usual)
6. Agent/engineer codes normally. Touching a locked file → **EACCES** locally; CI `verify` is the backstop.
7. **Onboarding / fresh clone** — new machine runs `sudo golden-lock apply` once (or via hooks) to re-establish locks; clones come back `644`.

### Unlock (sanctioned golden-lock change)
8. Invariant genuinely changed → human runs `sudo golden-lock remove <file>` → edits → `sudo golden-lock rehash <file>` (or re-`lock`), inside a CODEOWNERS-reviewed PR. This is the only blessed mutation path.

## CLI Commands

Privilege rule: **writes need root, reads do not.**

| Command | Effect | Privilege |
|---|---|---|
| `lock <filepath…>` | batch entry point: hash each file, append entries to `golden.lock`, `chown root` + `chmod 444` the files + manifest. Skips already-listed files with a notice. The primary setup verb. | **sudo** |
| `add <filepath>` | single-file form of `lock`. Errors if already listed. | **sudo** |
| `remove <filepath>` | drop entry, restore file to dev-owned `644`. The formal unlock (governed by team process / CODEOWNERS). | **sudo** |
| `apply` | set all listed files (+ manifest) to root-owned `444`. Idempotent. Skips-with-warning on a missing file. | **sudo** |
| `rehash <filepath>` | recompute + update stored hash after an approved modification. Errors if not listed. | **sudo** |
| `verify` | recompute SHA-256 of each listed file, compare to stored. The CI gate. | **none** |
| `status` | per-file hash ✅/❌ and lock state (root-`444` / unlocked). Human-readable; `--json` for tooling. | **none** |
| `list` | print the manifest's tracked files (paths only); `--json` for tooling. | **none** |
| `install-hooks` | write `post-checkout` / `post-merge` → `golden-lock apply` (see caveat). Append-safe. | **none** |
| `init` | create an empty `golden.lock` at repo root (discovered via `git rev-parse`). | **none** |
| `version` | print version + build info. | **none** |

### Command Reference (detailed)

Global flags (all commands): `--lockfile <path>` (default: `golden.lock` at repo root), `--repo-root <path>` (default: `git rev-parse --show-toplevel`), `--quiet`, `--json` (where supported).

- **`lock <filepath…>`** — *sudo, write.* For each path: resolve to repo-root-relative, compute SHA-256, append `<sha256>  <path>` to the manifest, `chown root` + `chmod 444` the file; then lock the manifest itself. Idempotent per-file (already-locked → skip + notice). Warns if a path lacks a governing `.gitattributes` rule. Fails fast (exit 4) if not run as root.
- **`add <filepath>`** — *sudo, write.* Single-file `lock`; **errors** (exit 5) if the file is already listed (vs `lock` which skips).
- **`remove <filepath>`** — *sudo, write.* Remove the entry, `chown` back to the invoking (sudo) user, `chmod 644`. Errors (exit 5) if not listed.
- **`apply`** — *sudo, write.* Re-assert root-`444` on every listed file + manifest. Idempotent. Missing file → warn + skip (does not fail). The git-hook / bootstrap command.
- **`rehash <filepath>`** — *sudo, write.* Recompute and overwrite the stored hash after a blessed edit. Errors (exit 5) if not listed. Temporarily requires the file be writable/readable to the operation; re-locks on completion.
- **`verify`** — *no privilege, read.* Recompute SHA-256 of each listed file, compare to stored. **The CI gate.** Exit codes below.
- **`status`** — *no privilege, read.* Per file: hash ✅/❌, lock state (root-`444` / unlocked / wrong-owner). `--json` emits structured records.
- **`list`** — *no privilege, read.* Manifest paths only. Scriptable inventory.
- **`install-hooks`** — *no privilege.* Append `golden-lock apply` to `post-checkout`/`post-merge`. Detects existing hooks / `core.hooksPath` / husky / lefthook and appends rather than clobbering. Prints the sudoers caveat.
- **`init`** — *no privilege.* Create an empty manifest at repo root if absent. Errors if one already exists.
- **`version`** — *no privilege.* Version + commit + platform.

### `verify` exit codes
- `0` — all hashes match.
- `1` — at least one hash mismatch (prints file + expected vs actual).
- `2` — at least one listed file is missing.
- `3` — `golden.lock` missing or malformed.

(Distinct codes so CI can tell tampering from misconfiguration.)

### Write-command exit codes (`lock`/`add`/`remove`/`apply`/`rehash`)
- `0` — success.
- `4` — not run as root (privilege required).
- `5` — argument error (already-listed for `add`, not-listed for `remove`/`rehash`).
- `6` — I/O / chown / chmod failure.

## `golden.lock` File Format

Plain text, checked into the repo, one entry per line. Hash-first to mirror `sha256sum`.

```
# golden.lock — manifest, managed by golden-lock CLI
# Do not edit manually. Use `golden-lock add` / `remove` (both require sudo).
# Format: <sha256>  <filepath>
a1b2c3d4e5f6...  src/auth/__tests__/tenant-isolation.test.ts
e5f6a7b8c9d0...  src/payments/__tests__/idempotency.test.ts
c9d0e1f2a3b4...  pkg/rbac/rbac_test.go
```

- **Paths are repo-root-relative, forward-slash**, normalized on `add`. Repo root discovered via `git rev-parse --show-toplevel`, so commands work from any subdirectory.
- Parser splits on the first run of spaces into `(hash, path)`; trailing path may contain spaces. The `*` binary marker from `sha256sum` is not emitted.

## Technical Decisions

- **Language:** Go. Single static binary, no runtime deps, drops into any repo regardless of stack.
- **Privilege model:** the locked files and `golden.lock` are root-owned `444`. Write commands require root and perform `os.Chown` **and** `os.Chmod` (chmod alone is insufficient — a dev-owned file can be re-chmod'd by its owner). Read commands need no privileges. **CI runs only `verify` → no sudo, zero CI disruption.**
- **Line-ending integrity:** golden paths must be pinned in `.gitattributes` (e.g. `path -text`, or repo-wide `* text=auto eol=lf`) so `autocrlf` cannot rewrite bytes on checkout and produce a spurious mismatch. `add` warns if a target lacks a governing rule.
- **No external dependencies.** SHA-256, `os.Chmod`, `os.Chown` are stdlib.
- **Cross-platform:** Linux, macOS, WSL2. Windows native out of scope Phase 1 (different ownership/permission model).

## Integration Points

### Git Hooks
`golden-lock install-hooks` writes `post-checkout` / `post-merge` → `golden-lock apply`.

**Caveat (stated honestly):** `apply` re-locks root-owned files and needs root, but hooks run as the dev user. So either (a) add a narrow passwordless sudoers entry for `golden-lock apply` only, or (b) accept re-locking as a manual `sudo golden-lock apply` after clone/checkout. `install-hooks` itself needs no privilege and detects existing hooks / `core.hooksPath` / husky / lefthook, appending rather than clobbering.

### CI Pipeline
`verify` is read-only and privilege-free; it runs as the normal CI user against the freshly checked-out `golden.lock`.

```yaml
# GitHub Actions
- name: Verify locked files
  run: golden-lock verify
```
```yaml
# GitLab CI
golden-lock-verify:
  stage: validate
  script: [golden-lock verify]
  allow_failure: false
```

### Agent Workflows (Claude Code, GSD, Ralph Loop)
Add to agent context (CLAUDE.md / system prompt / task spec):

```
Files listed in golden.lock are immutable, owned by root and read-only.
A permission-denied error on one means it is a locked invariant, not an obstacle.
Meet the invariant, don't rewrite it. Do not attempt to chmod, chown, or sudo around it.
```

For Ralph Loop: run `golden-lock verify` as a pre-acceptance gate before marking a task complete. On non-zero, reject the result and retry with the message above.

## File Structure

```
golden-lock/
├── main.go            # CLI entrypoint, command routing
├── lockfile.go        # golden.lock parse/read/write, path normalization, repo-root discovery
├── hash.go            # SHA-256 computation
├── permissions.go     # chown + chmod operations, privilege checks
├── hooks.go           # git hook installation (append-safe)
├── verify.go          # verification logic, exit codes
├── status.go          # status reporting (+ --json)
├── *_test.go          # per-unit tests
├── go.mod
└── README.md
```

## Out of Scope (Phase 1)
- Test tier system (Tiers 1–3) — golden tests only this phase.
- Append-to-locked-file (region-based hashing) — deferred to Phase 2.
- ~~Generic file-lock surface~~ — **delivered.** The tool locks any file, not just tests; the `/golden-test` skill remains as a test-discovery helper.
- Windows native (different ownership/permission model). WSL2 supported.
- Formal unlock ceremony automation — `remove`/`rehash` exist; governance (multi-approver PR, CODEOWNERS, ADR linkage) is team process.
- IDE integrations — CLI-first.

## Success Criteria
- `add` + `verify` round-trips: add a file, verify passes; modify it, verify fails (exit 1).
- After `add`, a non-root process cannot write the golden file **or** `golden.lock` (both root-owned `444`).
- `verify` exits non-zero on hash mismatch (1), missing file (2), or absent/malformed lockfile (3) — and requires no privileges.
- A modified locked file cannot be laundered without root, because `rehash`/manifest writes require root.
- Zero external Go dependencies. Compiles for linux/amd64, darwin/amd64, darwin/arm64.
- All commands idempotent and safe to run repeatedly.
