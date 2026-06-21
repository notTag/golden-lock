# Architecture

The high-level component map and data flow — what the major pieces are and how they
fit together.

`golden-lock` is a single static Go binary, flat layout, every file `package main`.
The full frozen contract (every signature, type, const) lives in
[../ARCH.md](../ARCH.md); this is the orientation map.

## The two-layer model

Locking a file enforces immutability at two independent layers:

- **Local prevention** — the file and the manifest are root-owned, `chmod 444`,
  **and** marked immutable at the filesystem layer (`chflags schg` on macOS,
  `chattr +i` on Linux). The immutable flag is load-bearing: `444` alone blocks an
  in-place write but *not* replace-by-rename (an editor writes a sibling temp and
  `rename()`s over the target, which needs write on the *directory*, not the file).
  The immutable inode refuses write, chmod, chown, rename, rename-over, and unlink
  until a root clears the flag.
- **CI detection (the hard gate)** — `verify` recomputes SHA-256 and compares
  against the manifest. Read-only, no privilege, exits non-zero on any drift.
  Platform-independent: always applies, even where the filesystem can't store the
  immutable flag (overlayfs, many network mounts), where `lock` degrades to
  detection-only.

The trust anchor is the **manifest itself**: `golden.lock` is also root-owned `444`
and immutable, so an agent can't rewrite it to launder a changed hash.

## Components (source files)

| File | Role |
|---|---|
| `main.go` | Arg routing / `dispatch`, the three subcommands (`lock`/`unlock`/`verify`), write-path exit codes (0/4/5/6), `--help`. |
| `verify.go` | `Verify(root)` — recompute + compare; read exit codes (0/1/2/3) with precedence. |
| `hash.go` | Path-bound SHA-256 + the symlink-free resolver (`resolveNoSymlink`) — the core trust primitive. |
| `lockfile.go` | Manifest model (`Entry`/`Manifest`), repo-root discovery, path normalization, atomic (locked) manifest writes. |
| `permissions.go` | `LockFile`/`UnlockFile` (+ `…FD` variants) — fchown/fchmod through an `O_NOFOLLOW` fd; privilege check. |
| `immutable.go` (+ `_darwin`/`_linux`) | Set/clear the filesystem immutable flag, per-GOOS; degrade to detection-only on unsupported FS. |
| `syscalls.go` | Portable `*at` syscall wrappers (`unix.Openat`/`Renameat`/`Unlinkat`) via `x/sys` — the one external dep. |

## The trust primitive: symlink-free resolution

`resolveNoSymlink` never trusts a path string twice and never follows a symlink at
**any** component. It opens the repo root as a dir-fd, walks each intermediate with
`Openat(O_NOFOLLOW|O_DIRECTORY)`, opens the leaf `O_NOFOLLOW`, and fstat-checks it
as a regular file. The walk extends *above* the root too (canonical absolute path
from `/`), so a symlinked ancestor planted after canonicalization is rejected.
`verify`, `lock`, and `unlock` all resolve files this way — the same fd is hashed,
frozen, and recorded, so there's no re-open TOCTOU window.

## Data flow

**lock** (root): for each file → `resolveNoSymlink` (O_RDWR) → `LockFileFD`
(fchown root:0 + fchmod 444) **first** → seek-0 + hash the now-frozen fd →
`applyImmutable` → `Upsert` in memory. After *all* files, a **single**
`WriteManifestLocked` publishes the manifest atomically. Freeze-then-hash +
single-publish closes the same-inode mutation window and the mid-loop-kill window.

**verify** (no privilege): `ReadManifest` → for each entry recompute the
path-bound hash and compare → aggregate to an exit code (absent/malformed
lockfile `3` dominates; among entries, missing `2` outranks mismatch `1`; all-OK
`0`).

**unlock** (root): check membership of *every* arg first (unlisted → exit 5,
nothing changed) → restore each with `UnlockFileFD` + `Remove` → rewrite the
manifest, or delete it if emptied (so a later `verify` returns `3`, not a
misleading `0`).

## The path-bound hash

The digest covers the path **and** the content, not content alone:

```
SHA-256( uvarint(len(rel)) || rel || content )
```

So the `<relpath>` in a manifest line is a *checked* value: a locked line can't be
moved to point at another file, and two files with identical content can't be
swapped, without `verify` catching it. The length prefix (vs a separator byte)
keeps any byte legal in the content.

See [../FLOW.md](../FLOW.md) for the end-to-end lifecycle and [GOTCHAS.md](GOTCHAS.md)
for the non-obvious ordering constraints.
