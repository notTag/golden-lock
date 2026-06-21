# Gotchas

Non-obvious pitfalls, footguns, and fragile spots — the "looks wrong but isn't"
notes that save wasted cycles.

## `chmod 444` does NOT stop a write — the immutable flag does

The instinct is that root-owned `444` makes a file unwritable. It blocks an
*in-place* write, but not replace-by-rename: most editors (and Claude's own Edit
tool) write a sibling temp and `rename()` it over the target, which needs write on
the **parent directory**, not the file. The load-bearing guard is the filesystem
immutable flag (`schg` / `chattr +i`), which refuses write, chmod, chown, rename,
rename-over, and unlink. If you "fix" the perms model by leaning on `444` alone,
you've reopened the hole.

## Lock order is freeze-THEN-hash, and publish is single-shot

`lock` does `LockFileFD` (fchown root:0 + fchmod 444) **before** hashing, then
seeks to 0 and hashes the now-frozen fd. Reversing this (hash, then freeze) opens
a window where the inode is still writable after its hash is taken. And all
entries accumulate to a **single** `WriteManifestLocked` at the very end — a
mid-loop kill must never publish a manifest asserting immutability over a
not-yet-frozen file. Don't refactor the loop to write per-file.

## The immutable flag fights the atomic rename publish

Atomic manifest publish relies on `rename()` over the live `golden.lock` — but an
immutable file can't be renamed over. So `WriteManifestLocked` **clears** the live
manifest's flag, does the `Renameat`, then **re-applies** the flag to the fresh
file. Same dance for `UnlockFileFD` and the emptied-manifest `os.Remove`. Each
clear→mutate→set window is root-only and momentary. If you see "clear immutable"
right before a privileged mutation, that's why — not a bug.

## `…FD` ops clear the flag first, then chown/chmod

chown/chmod are refused on an immutable inode, so `LockFileFD`/`UnlockFileFD` clear
any prior flag first (this also makes re-lock idempotent). They are the
ownership/mode freeze **only** — they do NOT set the immutable flag. Setting it is
`applyImmutable`, kept separate so it can be reported per file and so the manifest
temp stays renamable until publish.

## The hash is path-bound — the manifest path is checked, not a label

`SHA-256( uvarint(len(rel)) || rel || content )`. Editing `<relpath>` in a manifest
line to point at another file fails verify; two identical-content files can't be
swapped. Don't assume you can hand-edit a path in `golden.lock`.

## `autocrlf` can produce a spurious mismatch

The hash is over raw bytes. If git rewrites line endings on checkout, `verify`
fails even though nothing was "changed." Pin golden paths in `.gitattributes`
(e.g. `path -text`, or repo-wide `* text=auto eol=lf`).

## Write path needs REAL root; tests skip without it

`lock`/`unlock` `chown` to root — they need true root, not just any user. The
write-path tests `t.Skip` when `os.Geteuid() != 0`, so an unprivileged `go test`
reports them **skipped, not passed**. Use `sudo go test ./...` to actually
exercise them. (The non-root exit code `4` is still asserted without root.)

## Detection-only degradations are silent unless you read the output

Local prevention falls back to **detection-only** when: the agent runs as root
(common in containers), the filesystem can't store the flag (overlayfs, many
network mounts — `lock` prints a per-file warning), or the machine never ran
`sudo golden-lock lock` (fresh clone = dev-owned files). CI `verify` + branch
protection stays authoritative in all three. Don't read a green local lock as a
guarantee without checking for the degradation warning.

## Emptied manifest is DELETED, not left empty

If `unlock` removes the last entry, the `golden.lock` **file** is removed — so a
later `verify` returns `3` (absent), not a misleading `0` (all-OK over zero
entries). Don't expect an empty manifest file to linger.

## Malformed manifest ABORTS a write, never overwrites

`lock`/`unlock` against a present-but-corrupt (or symlinked) `golden.lock` exit
`6` and touch nothing. Only a genuinely absent (`os.IsNotExist`) manifest starts
fresh. This protects already-locked repos from a silent reset.

## Root *selection* trusts path-based stat (documented residual)

Repo-root selection uses path-based `os.Stat` for the `.git`/`golden.lock`
markers, so someone who can plant such a marker can influence *which* dir is
chosen as root. This can't induce a symlink-follow (the chosen root is still
opened through the O_NOFOLLOW canonical walk) nor launder a root-owned `444` hash.
Worst case it selects a different legitimate dir the attacker already controls. A
future `--lockfile` flag would sidestep discovery; not in this build.

## Under root, the `git rev-parse` probe is skipped

Repo-root discovery only shells out to `git rev-parse --show-toplevel` when
**unprivileged** (and from a vetted git path with a pinned PATH). Under root it's
skipped entirely — nearest `.git`/`golden.lock` ancestor walk only — so no
untrusted binary runs as root. Don't "simplify" by always probing git.
