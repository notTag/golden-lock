# golden-test — Architecture & Frozen Contract

Module path: `golden-test` · Go 1.26 · stdlib only · flat layout, all `package main`.
Targets: linux/amd64, darwin/amd64, darwin/arm64.

Base binary `golden-test`, alias dispatch name `gt`. Three subcommands only:
`lock <file>...` (sudo), `unlock <file>...` (sudo), `verify` (no privilege).

The signatures below are the FROZEN CONTRACT. Parallel implementers fill the
`panic("todo: ...")` bodies without changing any signature, type, or const.

---

## File responsibilities + signatures

### hash.go — SHA-256 of a file + symlink-FREE path resolver
```go
var ErrSymlink error // some path component (leaf OR intermediate dir) is a symlink

func resolveNoSymlink(root, rel, name string, flags int) (*os.File, error) // component-walk, O_NOFOLLOW at every step
func hashResolved(root, rel, name string) (string, error)                  // resolveNoSymlink + hashReader
func hashReader(r io.Reader) (string, error)                                // hash an already-open reader
func HashFile(path string) (string, error)                                 // resolver-backed convenience
func openNoFollow(absPath string) (*os.File, error)                        // resolver-backed convenience
```
Lowercase hex SHA-256 of one file. The trust primitive is `resolveNoSymlink`,
which opens the repo root with `O_NOFOLLOW|O_DIRECTORY`, walks each intermediate
component with `Openat(dirfd, comp, O_NOFOLLOW|O_DIRECTORY|O_RDONLY|O_CLOEXEC)`
down to the parent dir-fd, then opens the leaf with `Openat(parentfd, leaf,
O_NOFOLLOW|O_RDONLY|O_CLOEXEC)` (no `O_DIRECTORY`). A symlink (ELOOP) or a
non-directory where a directory is required, at ANY component, is rejected with
`ErrSymlink`; the leaf is fstat-checked as a regular file. `os.NewFile` wraps the
returned fd into an `*os.File` for hashing / fchown / fchmod. This defeats a
parent/intermediate directory symlink-swap, not merely a swapped leaf. The lock
path opens once via the resolver, freezes then hashes the SAME fd, then records —
hash, freeze, and the recorded entry all apply to one inode (no re-open TOCTOU
window, no swappable intermediate).

The component walk extends ABOVE the repo root too. `openRootDir` does not open
the root with a single path-based call (which would let the kernel follow every
ancestor directory blindly); it `filepath.Abs` + `EvalSymlinks` the root once —
resolving legitimate system symlinks such as macOS `/var`→`/private/var` and
`/tmp`, under which every `t.TempDir()` and real `/tmp` checkout lives — then
`openDirFromFSRoot` opens `/` and walks the canonical absolute path component by
component with `Openat(O_NOFOLLOW|O_DIRECTORY)`, pinning each ancestor inode by
fd. So a symlink introduced into any ancestor after canonicalization is rejected,
and the whole golden/manifest resolution chain — root prefix included — is
symlink-free at open time.

The `*at` syscalls are wrapped portably in `syscalls.go` via
`golang.org/x/sys/unix` (`unix.Openat`/`Renameat`/`Unlinkat`), which exports
maintained, ABI-safe constants on every supported target (linux/amd64,
darwin/amd64, darwin/arm64). This is the project's single external dependency;
it replaced an earlier per-OS split that hand-coded the Darwin XNU trap numbers,
which the stdlib `syscall` package does not export on macOS.

### lockfile.go — manifest model, repo-root discovery, path normalization
```go
const LockfileName = "golden-test.lock"

var ErrManifestMalformed error // present-but-corrupt manifest (use errors.Is)

type Entry struct {
	Hash string // lowercase hex SHA-256
	Path string // repo-root-relative, forward-slash, normalized
}

type Manifest struct {
	Root    string  // absolute repo-root dir
	Path    string  // absolute path to golden-test.lock
	Entries []Entry // manifest order
}

func FindRepoRoot(startDir string) (string, error)
func LockfilePath(root string) string
func NormalizePath(root, input string) (string, error)
func ReadManifest(root string) (*Manifest, error)  // absent → os.IsNotExist; corrupt → ErrManifestMalformed
func WriteManifest(m *Manifest) error              // atomic temp+rename, result NOT locked
func WriteManifestLocked(m *Manifest) error         // atomic temp → root:0/444 → rename (privileged write path)

func (m *Manifest) AbsPath(rel string) string
func (m *Manifest) Find(rel string) int
func (m *Manifest) Upsert(rel, hash string) bool // true=updated, false=appended
func (m *Manifest) Remove(rel string) bool       // true=removed, false=not listed
```

### permissions.go — lock/unlock ops + privilege check
```go
const LockedMode   = 0o444
const UnlockedMode = 0o644

func IsRoot() bool
func SudoUID() (uid int, gid int)
func LockFile(absPath string) error   // open O_NOFOLLOW, then fchown root:0 + fchmod 0444; requires root
func UnlockFile(absPath string) error // open O_NOFOLLOW, then fchown sudo-user + fchmod 0644; requires root
func LockFileFD(f *os.File) error     // fchown root:0 + fchmod 0444 on an open fd; requires root
func UnlockFileFD(f *os.File) error   // fchown sudo-user + fchmod 0644 on an open fd; requires root
```
All chown/chmod go through a fd opened with `O_NOFOLLOW` (`f.Chown`/`f.Chmod` =
fchown/fchmod), never a bare path — a symlinked or swapped leaf is rejected,
never followed. `LockFile`/`UnlockFile` reject symlinks via `ErrSymlink`.

### verify.go — verification + granular read exit codes
```go
const (
	ExitVerifyOK       = 0
	ExitVerifyMismatch = 1
	ExitVerifyMissing  = 2
	ExitVerifyLockfile = 3
)

type VerifyStatus int
const (
	StatusOK VerifyStatus = iota
	StatusMismatch
	StatusMissing
)

type VerifyResult struct {
	Path     string
	Status   VerifyStatus
	Expected string
	Actual   string // "" when missing
}

func Verify(root string) (results []VerifyResult, exitCode int)
```

### main.go — arg routing, dispatch, exit codes, --help
```go
const (
	ExitWriteOK      = 0
	ExitWriteNotRoot = 4
	ExitWriteArgs    = 5
	ExitWriteIO      = 6
)

func progName() string
func runLock(args []string) int
func runUnlock(args []string) int
func runVerify(args []string) int
func usage()
func dispatch(args []string) int // routes argv[1:], returns process exit code
func main()
```

---

## Contract notes

**Manifest line format.** One entry per line: `<sha256>  <relpath>`. Hash is
lowercase hex; separator is a run of whitespace — space OR tab (split on the
first whitespace run via `unicode.IsSpace` → `(hash, path)`; interior path
spaces preserved). `relpath` is repo-root-relative, forward-slash, normalized.
Lines whose first non-space byte is `#` are comments; blank lines ignored. No
`sha256sum` `*` binary marker. `WriteManifest` emits header comment lines then
entries in `Manifest.Entries` order (two spaces as the separator).

**Repo-root + paths.** `FindRepoRoot` walks upward from a start dir. The
`git rev-parse --show-toplevel` probe runs ONLY when unprivileged, and resolves
git from a fixed vetted list (`/usr/bin/git`, …) with a pinned `PATH`, never the
inherited PATH. Under root it is skipped entirely, relying on the nearest
`.git`/`golden-test.lock` ancestor walk so no untrusted binary runs as root.
`NormalizePath` resolves any input (abs or cwd-relative) to the
repo-root-relative cleaned form and errors if it escapes the root.
`Manifest.AbsPath` is the inverse (rel → abs under `Root`).

**Symlink / inode safety (symlink-FREE resolver).** Resolution never trusts a
path string twice and never follows a symlink at ANY component. `resolveNoSymlink`
opens the repo root as a dir-fd and walks every component with `Openat` +
`O_NOFOLLOW` (`O_DIRECTORY` on intermediates), rejecting `ErrSymlink` on any
symlinked or non-directory component, down to the leaf. `verify`, `lock`, and
`unlock` all open golden files this way against the explicit repo root, so a
swapped parent/intermediate directory cannot relocate the inode that is hashed or
frozen. The manifest is read the same way (a symlinked/non-regular
`golden-test.lock` is hard-rejected as malformed → exit 3; a present-but-non-root
manifest only WARNS, since pre-apply/dev machines are legitimately non-root). The
manifest WRITE opens the repo-root dir-fd, creates the temp with `Openat` inside
it, fchown root:0 + fchmod 444 the temp fd, then `Renameat(dirfd, tmp, dirfd,
"golden-test.lock")` — the anchor lands in the verified directory inode with no
path re-resolution between create and rename.

**Freeze-then-hash ordering (lock).** For each file `lock` opens via the resolver,
`LockFileFD` (fchown root:0 + fchmod 0444) FIRST, then seeks to 0 and hashes the
now-frozen fd, then records the entry. There is no writable window on the inode
after the hash is taken (it is already 444). All entries accumulate and a SINGLE
`WriteManifestLocked` runs at the very end, so a mid-loop kill never publishes a
manifest that asserts immutability over a not-yet-frozen file.

**verify → exit-code mapping** (precedence): absent/malformed lockfile (3)
dominates — detected before per-entry checks, returns `nil` results. Among
per-entry results, missing (2) outranks mismatch (1); all-OK → 0. `Verify`
returns both the per-entry slice and the aggregate `exitCode`; `runVerify`
prints results and returns that code.

**Lock / unlock semantics** (PRD add/remove mapped onto lock/unlock):
- `lock`: a present-but-malformed manifest (including a symlinked one) ABORTS
  with `ExitWriteIO (6)` and is never overwritten; only a genuinely absent
  (`os.IsNotExist`) manifest starts fresh. For each file → `resolveNoSymlink`
  once (O_RDWR, full component walk), `LockFileFD` (fchown root:0 + fchmod 0444)
  FIRST, then seek-0 + hash the now-frozen fd, then `Manifest.Upsert` in memory.
  After ALL files are frozen, a SINGLE `WriteManifestLocked` (repo-root dir-fd →
  temp via Openat → root:0/444 → Renameat in that dir-fd; live manifest stays 444
  throughout). This freeze-then-hash + single-publish ordering closes the
  same-inode content-mutation window and the mid-loop-kill window. Re-locking an
  already-listed file prints a notice: "unchanged" if the hash matches, or
  "updating recorded hash to match current content" if drifted. Requires root →
  else `ExitWriteNotRoot (4)`.
- `unlock`: membership of EVERY arg is checked (`Manifest.Find`) BEFORE any
  mutation — an unlisted path is `ExitWriteArgs (5)` with nothing changed. Then
  each file is opened via `resolveNoSymlink` (full component walk) and restored
  with `UnlockFileFD` + `Manifest.Remove`. If the manifest is thereby emptied,
  the manifest FILE is removed (so a later `verify` returns `3` absent, not a
  misleading `0`); otherwise `WriteManifestLocked` rewrites it root-444. A
  malformed/symlinked manifest aborts (`6`) without modification. Requires root.
- Arg errors (no files, bad path, unlisted unlock, symlinked leaf) →
  `ExitWriteArgs (5)`; chown/chmod/IO failure → `ExitWriteIO (6)`.

**Privilege.** Writes (`lock`/`unlock`) require `IsRoot()`; reads (`verify`)
require none — CI runs `verify` with no sudo. `SudoUID` reads `SUDO_UID`/
`SUDO_GID` (fallback to real uid/gid) for unlock ownership restore.

**Dispatch.** `dispatch(args)` matches `args[0]` to `lock`/`unlock`/`verify`;
`--help`/`-h`/empty/unknown → `usage()`. Alias `gt` vs `golden-test` affects only
help text (`progName()` from argv[0] basename), not routing.

**Build gate.** `go build ./...` and `go vet ./...` pass on stubs (verified).
