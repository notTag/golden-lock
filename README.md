# golden-test

Make golden test files immutable so an AI coding agent can't game "all tests pass" by editing the assertions.

AI coding agents optimize for green tests. When an agent can write to a test file, rewriting the assertion is often the cheapest path to passing — it games the reward function instead of fixing the implementation, and you find out a cycle later. `golden-test` designates certain tests as immutable and enforces it at two layers:

- **Local prevention:** golden tests and the manifest are root-owned, `chmod 444`, **and marked immutable at the filesystem layer** (`chflags schg` on macOS, `chattr +i` on Linux). The immutable flag is the load-bearing guard. `chmod 444` alone blocks an in-place write but **not** replace-by-rename: most editors — and Claude's own file-edit tool — write a sibling temp file and `rename()` it over the target, which needs write permission on the *directory*, not the file, so the root-owned `444` inode is simply unlinked and replaced. An immutable inode refuses writes, chmod, chown, rename, rename-over, and unlink until the flag is cleared (root only; on a host at securelevel ≥ 1, a single-user boot). An unprivileged agent hits a hard wall mid-loop, before any commit.
- **CI detection (hard gate):** `verify` recomputes SHA-256 and compares against the manifest. Read-only, no privileges, exits non-zero on any drift. This layer is platform-independent and always applies — including on filesystems that can't store the immutable flag (e.g. overlayfs in some containers), where `lock` reports that it degraded to detection-only.

The detection guarantee is the **manifest as trust anchor**: `golden-test.lock` itself is root-owned `444` and immutable. An agent that modifies a golden test changes its hash; to hide that it would have to rewrite the manifest, which it cannot. CI then catches the mismatch.

## Install

```sh
go build -o golden-test .
# move onto PATH, e.g.
sudo mv golden-test /usr/local/bin/
```

Single static binary, Go 1.26. Only dependency is `golang.org/x/sys` (ownership/permission/immutable-flag syscalls). Linux, macOS, and WSL2. Windows native is out of scope (different ownership model).

## Usage

```
golden-test <command> [arguments]
```

| Command | Effect | Privilege |
|---|---|---|
| `lock <file>...` | Hash each file, record it in `golden-test.lock`, then root-own + `chmod 444` + set the immutable flag on the file(s) **and the manifest**. Prints a per-file warning if the filesystem can't store the flag (detection-only). | **sudo** |
| `unlock <file>...` | Remove file(s) from the manifest and restore writable ownership/permissions. The sanctioned change path. | **sudo** |
| `verify` | Recompute the SHA-256 of every manifest entry and compare. Safe for CI. | none |

### Command status

The PRD ([PRD.md](PRD.md)) specifies a broader command surface. Current implementation ships the core three; the rest are planned.

| Command | Status | Notes |
|---|---|---|
| `lock <file>...` | ✅ Implemented | Hash, record, root-own `444` files + manifest. |
| `unlock <file>...` | ✅ Implemented | Removes entries + restores writable perms. Covers the PRD's `remove`. |
| `verify` | ✅ Implemented | CI gate. |
| `add <file>` | ☐ TODO | Single-file `lock` that errors if already listed (`lock` skips). |
| `remove <file>` | ☐ TODO | Formal single-file unlock (subsumed by `unlock` today). |
| `apply` | ☐ TODO | Re-assert root-`444` on all listed files; git-hook / bootstrap verb. |
| `rehash <file>` | ☐ TODO | Recompute stored hash after a blessed edit. |
| `status` | ☐ TODO | Per-file hash ✅/❌ and lock state; `--json`. |
| `list` | ☐ TODO | Print tracked paths; `--json`. |
| `init` | ☐ TODO | Create an empty manifest at repo root. |
| `install-hooks` | ☐ TODO | Append-safe `post-checkout` / `post-merge` → `apply`. |
| `version` | ☐ TODO | Print version + build info. |

### Example

```sh
# One-time locking ceremony (writes require root)
sudo golden-test lock src/auth/tenant_isolation_test.go pkg/payments/idempotency_test.go

# Steady state — verify is read-only, runs anywhere including CI
golden-test verify

# Sanctioned change: an invariant genuinely moved
sudo golden-test unlock pkg/payments/idempotency_test.go
# ...edit the test under a reviewed PR...
sudo golden-test lock pkg/payments/idempotency_test.go
```

Once locked, a non-root process cannot alter the test file or the manifest by any path — neither an in-place write (`EACCES` from `444`) nor a replace-by-rename (`EPERM` from the immutable flag). Agent auto-modes don't bypass kernel access control or filesystem immutability regardless of autonomy settings.

## Exit codes

CI distinguishes tampering from misconfiguration by exit code.

**`verify`**

| Code | Meaning |
|---|---|
| `0` | all hashes match |
| `1` | hash mismatch (a tracked file changed) |
| `2` | a tracked file is missing |
| `3` | `golden-test.lock` absent or malformed |

**`lock` / `unlock`**

| Code | Meaning |
|---|---|
| `0` | success |
| `4` | not run as root |
| `5` | argument error |
| `6` | I/O / chown / chmod failure |

## The `golden-test.lock` manifest

Plain text, checked into the repo, one entry per line. Hash-first to mirror `sha256sum`.

```
<sha256>  <repo-root-relative-path>
```

The `<sha256>` hashes the path **and** the content, not the content alone: `SHA-256( uvarint(len(path)) || path || content )`. This means the path is checked, not just recorded. A locked line can't be moved to point at a different file, and two golden files with identical content can't be swapped, without `verify` catching it.

Paths are repo-root-relative and forward-slash, normalized on `lock`. The repo root is discovered via `git rev-parse --show-toplevel`, falling back to walking upward for a `.git` directory or an existing `golden-test.lock`, so commands work from any subdirectory. Path resolution is symlink-free from the filesystem root, so a symlinked ancestor can't redirect a locked path.

> Pin golden paths in `.gitattributes` (e.g. `path -text`, or repo-wide `* text=auto eol=lf`) so `autocrlf` can't rewrite bytes on checkout and produce a spurious hash mismatch.

## Threat model

**Attacker:** an unprivileged local process (the agent) running as a normal user, distinct from the file owner (root).

Guaranteed under that assumption, on a filesystem that supports the immutable flag:

- The agent cannot modify a golden test — not in place (`444`) and not by replace-by-rename (immutable flag refuses rename-over and unlink).
- The agent cannot rewrite `golden-test.lock` to launder a hash (same protections).
- Any tampering therefore surfaces as a hash mismatch at CI `verify`.

The local prevention layer degrades to detection-only when the agent runs as root (common in containers), when the filesystem can't store the immutable flag (e.g. overlayfs — `lock` reports this per file), or when a machine never ran `sudo golden-test lock` (fresh clone returns dev-owned files). In all cases CI `verify` plus branch protection remains the authoritative gate. For defense in depth, put `golden-test.lock` under CODEOWNERS with required review.

## CI integration

`verify` is read-only and privilege-free.

```yaml
# GitHub Actions
- name: Verify golden tests
  run: golden-test verify
```

## Agent integration

Add to your agent context (CLAUDE.md / system prompt / task spec):

```
Files listed in golden-test.lock are immutable golden tests, owned by root and read-only.
A permission-denied error on one of them means the test is the spec.
Fix the implementation, not the test. Do not attempt to chmod, chown, or sudo around it.
```

For a Ralph-style loop, run `golden-test verify` as a pre-acceptance gate before marking a task complete; on non-zero, reject and retry.

## Development

```sh
go test ./...   # unit + integration tests in goldentest_test.go
go build -o golden-test .
```

See [PRD.md](PRD.md) for the full product spec and [ARCH.md](ARCH.md) for design notes.
