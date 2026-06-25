# Definitions

Short glossary for terms used across golden-lock's locking model, grouped by relation.

## golden-lock concepts

**locked file (golden file):** Any file locked with golden-lock — root-owned, `chmod 444`, and marked immutable — so neither an agent nor a human can change it without the sanctioned unlock. It encodes an invariant treated as the specification. Tests were the first use case, but a locked file can equally be a config, schema, migration, or fixture.

**golden test:** The flagship kind of locked file — a test that encodes a core invariant and is treated as the specification: when it fails you fix the implementation, never the test. golden-lock makes such tests immutable so an agent (or human) can't quietly rewrite the assertion to force a green run.

**trust anchor (the manifest):** `golden.lock`, the root-owned, immutable file mapping each golden file to its expected SHA-256. It is the single source of truth `verify` checks against; locking it — so it can't be rewritten to launder a hash — is what makes the whole scheme trustworthy.

**detection vs prevention:** golden-lock's two-layer model. *Prevention* (the immutable flag) stops a tamper from ever landing but is best-effort and platform-dependent; *detection* (the SHA-256 manifest plus `verify`) catches any tamper after the fact, works everywhere, and is the authoritative CI gate.

## The attack it defends against

**replace-by-rename (rename-replace):** Modifying a file you can't write by creating a sibling temp file and `rename()`-ing it over the target. Because rename permission comes from the parent *directory*, not the file, this defeats `chmod 444` + root ownership — and is exactly how most editors (and Claude's Edit tool) save. The immutable flag is what blocks it.

**EACCES vs EPERM:** The two errors an unprivileged process hits on a locked golden file: `EACCES` ("permission denied") on an in-place write (blocked by `chmod 444`), and `EPERM` ("operation not permitted") on a rename-over or unlink (blocked by the immutable flag). Either one means the file is a locked invariant — fix your own code, not the locked file.

## Immutable file flags (the prevention layer)

**chflags:** The macOS/BSD command and syscall that set per-file flags such as `schg` and `uchg`, stored in the inode separately from the `rwx` permission bits. golden-lock calls the `Fchflags` form on an open fd to set `SF_IMMUTABLE`.

**schg (`SF_IMMUTABLE`):** The macOS/BSD "system immutable" file flag (`chflags schg`) that blocks all writes, renames, rename-over, `chmod`/`chown`, and deletion. Only root can set or clear it, and clearing is refused while the kernel runs at securelevel ≥ 1 (it then requires a single-user boot), making it root-proof on hardened hosts. golden-lock uses this flag.

**uchg (`UF_IMMUTABLE`):** The macOS/BSD "user immutable" file flag (`chflags uchg`) with the same immutability effect as schg, except the file's owner (or root) may set and clear it at any time, independent of securelevel.

**chattr / `FS_IMMUTABLE_FL`:** The Linux equivalent of `chflags`: `chattr +i` sets the `FS_IMMUTABLE_FL` inode flag via the `FS_IOC_SETFLAGS` ioctl, making the file unmodifiable until cleared. Requires the `CAP_LINUX_IMMUTABLE` capability (held by root).

**CAP_LINUX_IMMUTABLE:** The Linux capability a process must hold to set or clear the immutable (and append-only) inode flags. Root has it by default; without it `chattr +i` is refused, so locking on Linux requires privilege.

**securelevel:** A BSD/macOS kernel security level (`kern.securelevel`) that can only be raised at runtime, never lowered. At level ≥ 1 the `schg` flag cannot be cleared even by root (a single-user boot is required), which is what makes schg root-proof on hardened hosts; macOS defaults to 0, where root can clear it freely.

## Filesystem fundamentals

**inode:** The on-disk object that actually *is* a file — its content plus metadata (owner, mode, flags, timestamps), identified by a number. A filename is just a directory entry pointing at an inode, and permission to rename or delete that entry is governed by the parent directory, not the inode — which is why `chmod 444` alone does not stop replace-by-rename.

**File descriptor (fd):** A small integer the kernel returns when you open a file; it is a handle to one specific *open inode*, so operations on it (read, `fchmod`, `fchflags`) act on exactly that file no matter what its path later resolves to. Using an fd instead of re-opening a path is what closes time-of-check-to-time-of-use (TOCTOU) gaps.

**atomic rename:** Writing new content to a temp file then `rename()`-ing it over the destination, so readers ever see only the old or the new file, never a half-written one (`rename` is atomic within a filesystem). golden-lock publishes the manifest this way so a crash mid-write can't corrupt the trust anchor.

**sticky bit:** A directory permission (`chmod +t`) that lets only a file's owner (or root) rename or delete entries in that directory, even when the directory is world-writable. Considered as a lighter alternative to per-file immutability but rejected — it is a directory-wide change and offers no protection against a root-level actor.

**overlayfs:** A union/layered filesystem used by most container runtimes. It cannot store the immutable flag, so `lock` on an overlayfs golden file degrades to detection-only (with a per-file warning) while the SHA-256 manifest still guards it.

## Safe path resolution

**O_NOFOLLOW:** An `open()` flag that makes the call fail rather than follow a symlink at the final path component. golden-lock opens every component this way (plus `O_DIRECTORY` on intermediates) so a swapped symlink can't redirect a hash, freeze, or lock onto a decoy file.

**TOCTOU (time-of-check-to-time-of-use):** A class of bug where a resource is validated by path, then used by path a moment later, leaving a window for an attacker to swap what the path points at in between. golden-lock avoids it by checking and acting on the same open fd, never re-resolving the path.

## Build & portability

**GOOS / build tags:** Go's mechanism for compiling different code per target OS. `GOOS` names the target (`darwin`, `linux`); a `_darwin.go` / `_linux.go` filename suffix or a `//go:build` line selects which file compiles — how golden-lock isolates the macOS `chflags` vs Linux `ioctl` immutable-flag code.
