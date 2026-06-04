# golden-test — Review Findings (10)

Source: build-golden-test workflow review phase. Fix scope: ALL.

## [1] HIGH — correctness — `main.go`

**Issue:** runLock (lines 71-75) treats a MALFORMED manifest identically to an ABSENT one: when ReadManifest returns any error it discards everything and builds a fresh empty Manifest. ReadManifest returns an error both when the file is missing AND when any single line is malformed (lockfile.go:170,175). So if golden-test.lock has even one bad/corrupted line, running `lock newfile.txt` rewrites the manifest containing ONLY the new entry, silently dropping every previously-locked file's recorded hash. Those files remain root-owned 444 on disk but are no longer tracked, so `verify` will no longer detect tampering on them. This directly undermines the trust anchor the tool exists to protect, and there is no warning to the operator. The inline comment 'No (or unreadable) manifest yet: start a fresh one' confirms the two cases were conflated intentionally.

**Fix:** Distinguish absent from malformed. Use os.IsNotExist on the underlying error (or have ReadManifest return a sentinel/typed error) and only start fresh when the file genuinely does not exist. On a malformed-but-present manifest, abort with ExitWriteIO (6) and a clear message rather than overwriting it, so existing entries are never silently lost.

## [2] HIGH — security — `permissions.go`

**Issue:** Symlink-following privilege escalation in LockFile/UnlockFile. Both use os.Chown and os.Chmod (lines 54-58, 71-75), which follow symlinks; NormalizePath in lockfile.go uses filepath.Clean/filepath.Rel only and never resolves or rejects symlinks. A golden file lives in a repo dir the unprivileged agent controls. The agent deletes the 444 golden file's path entry by replacing it with a symlink, e.g. `ln -sf /etc/shadow internal/golden_test.go`, then induces `sudo golden-test unlock internal/golden_test.go`. UnlockFile does os.Chown(target, SUDO_UID, SUDO_GID) + os.Chmod(target, 0644) on the symlink TARGET, handing the attacker ownership and write access to an arbitrary root-owned file (CWE-59/CWE-61). LockFile is similarly abusable to chown arbitrary files to root:0/444 (DoS on system files). This fully defeats the privilege model.

**Fix:** Operate on a file descriptor opened with O_NOFOLLOW (os.OpenFile(absPath, O_RDONLY|O_NOFOLLOW, 0)) and use f.Chown/f.Chmod (fchown/fchmod on the fd), or os.Lstat the path first and refuse if Mode()&os.ModeSymlink != 0. Reject any path component that is a symlink, or canonicalize with filepath.EvalSymlinks in NormalizePath and re-verify the result is still inside the repo root before chown/chmod.

## [3] HIGH — security — `main.go`

**Issue:** TOCTOU between hashing and locking in runLock. Files are hashed in the loop at lines 80-93 (HashFile -> os.Open by path) and only locked later at lines 108-113 (LockFile by the same path). Because the file is still attacker-writable in that window, the agent can (a) swap the file's content after HashFile records hash(A) so that hash(A) gets committed to the manifest while a different content B is what ends up locked, or (b) replace the path with a symlink between hash and chown, combining with the permissions.go symlink issue. The path is resolved to a string once and re-opened twice with no identity check, so the bytes that were hashed are not guaranteed to be the bytes that get frozen.

**Fix:** Open each file once with O_NOFOLLOW, hash from that fd, then fchmod/fchown the SAME fd so hash and lock apply to one inode atomically. At minimum os.Lstat before HashFile and os.Lstat again before LockFile and compare Sys() st_ino/st_dev (or use the fd) to detect a swap, aborting on mismatch.

## [4] MEDIUM — security — `main.go`

**Issue:** Manifest trust anchor can be silently left writable on partial failure. In runLock (line 101) and runUnlock (line 175) the code does `_ = UnlockFile(m.Path)` (chown to SUDO_UID + chmod 0644), discarding the error, then later re-locks. If the process is killed, or WriteManifest/LockFile returns an error after the unlock (e.g. lines 103-106, 180-183 return on failure), the golden-test.lock is left 0644 and owned by the unprivileged sudo user. The agent can then freely rewrite stored hashes to relaunder a modified golden file, exactly the attack the design exists to prevent. There is no rollback/re-lock on the error paths and no fd-based atomicity.

**Fix:** Use a deferred guard that re-locks the manifest (LockFile) on ALL exit paths if it was unlocked, and check the UnlockFile error instead of discarding it. Prefer rewriting via temp+rename while keeping the live manifest 444 until the final atomic rename of an already-root-owned temp, so there is never a window where the live manifest is writable.

## [5] MEDIUM — security — `hash.go`

**Issue:** verify follows symlinks and does not pin the inode, allowing redirection when a parent dir is attacker-controlled. HashFile (line 16, os.Open) follows symlinks, and Verify (verify.go line 54-55) resolves manifest relpaths via Manifest.AbsPath using plain filepath.Join. The leaf golden file is 444 root, but if the agent owns a parent directory in the path they can swap a directory component or the final name for a symlink pointing at a decoy file whose bytes match the stored hash, while the real (now-different) assertions live elsewhere. verify then reports OK against the decoy. The trust model assumes the path resolves to the locked inode, but nothing enforces that.

**Fix:** In HashFile/Verify open with O_NOFOLLOW and, for stronger guarantees, lstat each component or EvalSymlinks the resolved path and confirm it is the same inode that was locked. Document that parent directories of golden files must also be root-owned, and consider recording st_dev/st_ino in the manifest to detect substitution.

## [6] MEDIUM — prd-conformance — `main.go`

**Issue:** `unlock` of a file that is NOT listed in the manifest silently succeeds with exit 0 instead of the PRD's arg-error exit 5. runUnlock (lines 165-172) calls UnlockFile(abs) then `m.Remove(rel)` but ignores Remove's bool return. The PRD's `remove` verb (mapped onto `unlock` per ARCH) states: 'Errors (exit 5) if not listed.' Today, `sudo golden-test unlock some/untracked/file` chowns it to 644 and prints 'unlocked some/untracked/file' with exit 0 — divergent from the not-listed → exit 5 contract, and it mutates ownership/perms on an arbitrary in-repo file the tool never protected.

**Fix:** In runUnlock, resolve+check membership before mutating: for each rel, if `m.Find(rel) < 0` print a not-listed error and `return ExitWriteArgs` (5) BEFORE any UnlockFile call (consistent with the existing 'resolve all paths first so an arg error aborts before any perm change' comment at line 154). Alternatively capture `if !m.Remove(rel) { ... return ExitWriteArgs }`, but doing the Find check up-front avoids leaving some files already unlocked when a later arg is rejected.

## [7] LOW — correctness — `lockfile.go`

**Issue:** ReadManifest splits hash/path on the first ASCII space only (strings.IndexByte(trimmed, ' '), line 168), but the leading-whitespace strip uses TrimLeft(raw, " \t") (line 158) which also accepts tabs. A manifest entry that uses a TAB as the hash/path separator (e.g. 'hash\tpath' with no spaces) yields idx < 0 and is rejected as malformed, returning exit 3 from verify. WriteManifest always emits two spaces so self-produced manifests round-trip fine; this only bites hand-written or tab-formatted manifests. The spec defines the separator as 'a run of spaces', so this is spec-conformant but the asymmetry (tabs allowed as leading indent / comment whitespace but not as separator) is a latent surprise.

**Fix:** Either document explicitly that only spaces separate hash from path (tabs unsupported), or split on the first run of whitespace (space OR tab) using strings.IndexFunc(unicode.IsSpace) for consistency with the TrimLeft set.

## [8] LOW — security — `lockfile.go`

**Issue:** FindRepoRoot executes git from PATH while the process is root. exec.LookPath("git") + exec.Command(git, ...) at lines 52-55 runs an external binary as root, with PATH inherited from the (sudo) environment. If sudo is configured without secure_path or env is preserved, an attacker-planted `git` earlier in PATH runs with full root privileges during lock/unlock. Reads (verify) are unprivileged so this only matters for the sudo paths, but it is an untrusted-binary-as-root exposure.

**Fix:** When IsRoot(), avoid exec'ing git or use an absolute, vetted git path; or skip the git probe entirely under root and rely on the .git/golden-test.lock ancestor walk (lines 67-82). At minimum sanitize PATH before LookPath.

## [9] LOW — prd-conformance — `main.go`

**Issue:** When `unlock` removes the last entry, the manifest is rewritten to header-only and immediately re-locked root-444 (lines 175-183). The result is a root-owned 444 golden-test.lock with zero tracked entries. ARCH explicitly leaves 'final manifest state when emptied' to the implementer, so this is permitted, but leaving an empty manifest root-owned means an unprivileged user cannot delete or edit it, and a later non-sudo `verify` returns exit 0 on an empty manifest (vacuously OK) which can mask the fact that all protection was removed.

**Fix:** Consider: when Entries is empty after unlock, either remove the manifest file entirely (so a later `verify` returns exit 3 absent rather than a misleading exit 0), or leave it writable (644 root or chown back to the sudo user) so it is not an orphaned root-444 artifact. At minimum document the chosen end-state.

## [10] LOW — prd-conformance — `main.go`

**Issue:** `lock` re-run on an already-listed file whose content has since changed silently re-hashes to the new content and prints 'locked <path>' with no notice — effectively an undocumented `rehash`. PRD's `lock` says 'Skips already-listed files with a notice'; ARCH sanctions a same-hash re-upsert as the idempotency model, but does not anticipate the case where the on-disk content differs from the recorded hash (which would update the stored hash to match a possibly-tampered file). This is a privileged op (sudo) so the trust impact is bounded, but the silent hash overwrite is a behavioral divergence from both the PRD 'skip + notice' and from any drift-detection expectation.

**Fix:** On `lock`, when `m.Find(rel) >= 0` and the freshly computed hash differs from the stored hash, print an explicit notice (e.g. 'note: <path> already locked; updating recorded hash to match current content') rather than silently overwriting; or print 'already locked, unchanged' when the hash matches. This restores the PRD's 'with a notice' behavior without adding new verbs.
