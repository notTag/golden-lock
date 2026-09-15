# Golden Lock

Lock any file so an unprivileged process, including an AI coding agent, can't silently change it. Originally built to stop an agent from gaming "all tests pass" by rewriting the assertions.

AI coding agents optimize for green tests. When an agent can write to a test file, rewriting the assertion is often the cheapest path to passing — it games the reward function instead of fixing the implementation, and you find out a cycle later. That is the first thing `golden-lock` was built to stop, but the mechanism is file-agnostic: it designates any file you choose: a test, a security config, a schema, a fixture, as immutable and enforces it at two layers:

- **Local prevention:** the locked files and the manifest are root-owned, `chmod 444`, **and marked immutable at the filesystem layer** (`chflags schg` on macOS, `chattr +i` on Linux). The immutable flag is the load-bearing guard. `chmod 444` alone blocks an in-place write but **not** replace-by-rename: most editors, and Claude's own file-edit tool, write a sibling temp file and `rename()` it over the target, which needs write permission on the *directory*, not the file, so the root-owned `444` inode is simply unlinked and replaced. An immutable inode refuses writes, chmod, chown, rename, rename-over, and unlink until the flag is cleared (root only, on a host at securelevel ≥ 1, a single-user boot). An unprivileged agent hits a hard wall mid-loop, before any commit.
- **CI detection (hard gate):** `verify` recomputes SHA-256 and compares against the manifest. Read-only, no privileges, exits non-zero on any drift. This layer is platform-independent and always applies, including on filesystems that can't store the immutable flag (e.g. overlayfs in some containers), where `lock` reports that it degraded to detection-only.

The detection guarantee is the **manifest as trust anchor**: `golden.lock` itself is root-owned `444` and immutable. An agent that modifies a locked file changes its hash; to hide that it would have to rewrite the manifest, which it cannot. CI then catches the mismatch.

## Install

```sh
go build -o golden-lock .
# move onto PATH, e.g.
sudo mv golden-lock /usr/local/bin/
```

Single static binary, Go 1.26. Only dependency is `golang.org/x/sys` (ownership/permission/immutable-flag syscalls). Linux, macOS, and WSL2. Windows native is out of scope (different ownership model).

## Usage

```
golden-lock <command> [arguments]
```

| Command | Effect | Privilege |
|---|---|---|
| `init` | Create an empty `golden-lock/golden.lock` in the current directory, making it the root. For directories outside version control (no `.git` to anchor on). Refused inside a git repo; never overwrites an existing manifest. | none |
| `lock [<file>...]` | Hash each file, record it in `golden-lock/golden.lock`, then root-own + `chmod 444` + set the immutable flag on the file(s) **and the manifest**. With **no files**, locks every path listed under `golden-lock/proposal-locks/`. Prints a per-file warning if the filesystem can't store the flag (detection-only). | **sudo** |
| `unlock <file>...` | Remove file(s) from the manifest and restore writable ownership/permissions. The sanctioned change path. | **sudo** |
| `verify` | Recompute the SHA-256 of every manifest entry and compare. Safe for CI. | none |
| `list` | Print the path of every file recorded in the manifest. Hashes nothing — use `verify` for that. | none |

### Command status

The PRD ([PRD.md](PRD.md)) specifies a broader command surface. Current implementation ships the core three; the rest are planned.

| Command | Status | Notes |
|---|---|---|
| `lock [<file>...]` | ✅ Implemented | Hash, record, root-own `444` files + manifest. No-arg form locks every path listed under `golden-lock/proposal-locks/`. |
| `unlock <file>...` | ✅ Implemented | Removes entries + restores writable perms. Covers the PRD's `remove`. |
| `verify` | ✅ Implemented | CI gate. |
| `list` | ✅ Implemented | Manifest paths only; no hashing, no lock-state check. |
| `add <file>` | ☐ TODO | Single-file `lock` that errors if already listed (`lock` skips). |
| `remove <file>` | ☐ TODO | Formal single-file unlock (subsumed by `unlock` today). |
| `apply` | ☐ TODO | Re-assert root-`444` on all listed files; git-hook / bootstrap verb. |
| `rehash <file>` | ☐ TODO | Recompute stored hash after a blessed edit. |
| `status` | ☐ TODO | Per-file hash ✅/❌ and lock state; `--json`. |
| `list` | ☐ TODO | Print tracked paths; `--json`. |
| `init` | ✅ Implemented | Empty manifest in the current dir; anchors the root outside git. |
| `install-hooks` | ☐ TODO | Append-safe `post-checkout` / `post-merge` → `apply`. |
| `version` | ✅ Implemented | Print version + build info; also `--version` / `-v`. |

### Example

```sh
# One-time locking ceremony (writes require root) — name the files explicitly
sudo golden-lock lock src/auth/tenant_isolation_test.go config/production.yaml

# ...or declare the lock set in golden-lock/proposal-locks/ and lock it in one pass.
# Each file there is a newline-separated list of repo paths, grouped by concern:
#   golden-lock/proposal-locks/auth.txt     ->  src/auth/tenant_isolation_test.go
#   golden-lock/proposal-locks/configs.txt  ->  config/production.yaml
sudo golden-lock lock        # no args: locks every path listed under golden-lock/proposal-locks/

# Steady state — verify is read-only, runs anywhere including CI
golden-lock verify

# Sanctioned change: an invariant genuinely moved
sudo golden-lock unlock config/production.yaml
# ...edit the file under a reviewed PR...
sudo golden-lock lock config/production.yaml
```

Once locked, a non-root process cannot alter the file or the manifest by any path — neither an in-place write (`EACCES` from `444`) nor a replace-by-rename (`EPERM` from the immutable flag). Agent auto-modes don't bypass kernel access control or filesystem immutability regardless of autonomy settings.

## Exit codes

CI distinguishes tampering from misconfiguration by exit code.

**`verify`**

| Code | Meaning |
|---|---|
| `0` | all hashes match |
| `1` | hash mismatch (a tracked file changed) |
| `2` | a tracked file is missing |
| `3` | `golden.lock` absent or malformed |

**`lock` / `unlock`**

| Code | Meaning |
|---|---|
| `0` | success |
| `4` | not run as root |
| `5` | argument error |
| `6` | I/O / chown / chmod failure |

## The `golden.lock` manifest

The manifest lives at `golden-lock/golden.lock`. The `golden-lock/` directory
houses all of golden-lock's state — the manifest plus the `proposal-locks/` list
files that `lock` (no args) reads.

Plain text, checked into the repo, one entry per line. Hash-first to mirror `sha256sum`.

```
<sha256>  <repo-root-relative-path>
```

The `<sha256>` hashes the path **and** the content, not the content alone: `SHA-256( uvarint(len(path)) || path || content )`. This means the path is checked, not just recorded. A locked line can't be moved to point at a different file, and two locked files with identical content can't be swapped, without `verify` catching it.

Paths are repo-root-relative and forward-slash, normalized on `lock`. The repo root is discovered via `git rev-parse --show-toplevel`, falling back to walking upward for a `.git` directory or an existing `golden-lock/golden.lock`, so commands work from any subdirectory. Path resolution is symlink-free from the filesystem root, so a symlinked ancestor can't redirect a locked path.

> Pin golden paths in `.gitattributes` (e.g. `path -text`, or repo-wide `* text=auto eol=lf`) so `autocrlf` can't rewrite bytes on checkout and produce a spurious hash mismatch.

## Threat model

**Attacker:** an unprivileged local process (the agent) running as a normal user, distinct from the file owner (root).

Guaranteed under that assumption, on a filesystem that supports the immutable flag:

- The agent cannot modify a locked file — not in place (`444`) and not by replace-by-rename (immutable flag refuses rename-over and unlink).
- The agent cannot rewrite `golden.lock` to launder a hash (same protections).
- Any tampering therefore surfaces as a hash mismatch at CI `verify`.

The local prevention layer degrades to detection-only when the agent runs as root (common in containers), when the filesystem can't store the immutable flag (e.g. overlayfs — `lock` reports this per file), or when a machine never ran `sudo golden-lock lock` (fresh clone returns dev-owned files). In all cases CI `verify` plus branch protection remains the authoritative gate. For defense in depth, put `golden.lock` under CODEOWNERS with required review.

## What survives a clone

The local prevention layer does **not** travel with the repo. Be precise about which guarantee is portable and which is not.

- **`chown root:0` + `chmod 444` + the immutable flag are local filesystem state, not repo content.** Git records neither ownership nor permission bits — the executable bit is the only mode it tracks, and it stores no immutable flag at all. After any `git clone`, the golden files and `golden-lock/golden.lock` land as ordinary user-owned, writable files. Nothing the kernel was enforcing on the original host carries over. The lock has to be re-asserted per machine with `sudo golden-lock lock`.
- **The durable, portable protection is the CI hash gate (`golden-lock verify`).** It is just content comparison, so it works identically on every checkout regardless of who owns the files. But note the consequence of `golden-lock/golden.lock` being committed alongside the files it guards: a single commit that edits a golden file **and** rewrites that file's recorded hash in the manifest passes `verify` green. The gate proves only that file content matches the manifest content in the *same* commit — not that either is the blessed version.
- **So treat CI as a tripwire, not as cryptographic immutability.** Its real value is that tampering forces a visible diff in `golden-lock/golden.lock` into the pull request. That only protects you if branch protection is enabled (so the change can't be pushed straight to `main`) **and** a human actually reviews `.lock` diffs (so a manifest edit smuggled alongside a golden-file change gets caught). Without both, the tripwire is silent. Do not assume the immutability you set up locally rides along with the repository.

## CI integration

`verify` is read-only and privilege-free.

```yaml
# GitHub Actions
- name: Verify locked files
  run: golden-lock verify
```

## Agent integration

Add to your agent context (CLAUDE.md / system prompt / task spec):

```
Files listed in golden-lock/golden.lock are immutable: root-owned, chmod 444, and marked
immutable at the filesystem layer. They are locked on purpose — each encodes an
invariant (a test that is the spec, a config that must not drift, a fixture that
must stay byte-stable).

If you hit a permission error touching one of them — EACCES on a write, or EPERM on
a rename/move/chflags/chattr — do NOT treat it as an obstacle to route around. It is
a signal that you are about to change the invariant instead of meeting it. When it happens:

- Look at your OWN code first. A locked file that fails verification or blocks your
  edit almost always means your change is wrong or incomplete. Meet the invariant,
  don't rewrite it.
- Do not try to defeat the lock. No chmod, chown, chflags, chattr, sudo, deleting and
  recreating, or editing the file. The only sanctioned way to change a locked file is
  `sudo golden-lock unlock <file>` under human review — never something to do
  mid-task on your own.
- Do not stop. This error is feedback, not a fatal failure. Course-correct your
  work and keep going — do not abort the task, halt the loop, or report the run as
  blocked because a locked file refused a write.
```

For a Ralph-style loop, run `golden-lock verify` as a pre-acceptance gate before marking a task complete; on non-zero, reject and retry.

## Development

```sh
go test ./...   # unit + integration tests in goldenlock_test.go
go build -o golden-lock .
```

See [PRD.md](PRD.md) for the full product spec and [ARCH.md](ARCH.md) for design notes.
