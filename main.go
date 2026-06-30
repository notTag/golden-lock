package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// main.go — CLI entrypoint and argument routing.
//
// Invoked as either "golden-lock" or the alias "gl" (dispatch is by subcommand,
// not by argv[0]; the basename is accepted for help/usage text only).
//
// Subcommands (happy-path build):
//
//	golden-lock lock   <file>...   sudo — hash each file, record in manifest, lock files + manifest
//	golden-lock unlock <file>...   sudo — remove from manifest, restore writable ownership/perms
//	golden-lock verify             no priv — recompute + compare every entry
//
// Write exit codes (lock/unlock):
//
//	0 success | 4 not-root | 5 arg error | 6 io failure
//
// Read exit codes (verify): see verify.go (0/1/2/3).

// Write-command exit codes.
const (
	ExitWriteOK      = 0
	ExitWriteNotRoot = 4
	ExitWriteArgs    = 5
	ExitWriteIO      = 6
)

// progName returns the user-facing program name derived from argv[0]
// ("golden-lock" or "gl"), used only in usage/help output.
func progName() string {
	if len(os.Args) == 0 {
		return "golden-lock"
	}
	base := filepath.Base(os.Args[0])
	if base == "gl" {
		return "gl"
	}
	return "golden-lock"
}

// ProposalLocksDir is the GoldenLockDir subdirectory whose files each hold a
// newline-separated list of repo paths to lock — i.e. golden-lock/proposal-locks/.
// `lock` with no file arguments locks the union of every path listed across
// these files.
const ProposalLocksDir = "proposal-locks"

// gatherProposalLocks reads every list file under <root>/golden-lock/proposal-locks/
// and returns the absolute paths of the files they reference. Lines are trimmed;
// blank lines, dotfiles, and subdirectories are ignored. A listed path that does
// not exist (or escapes the repo root) is reported to stderr and skipped rather
// than failing the whole lock. Duplicates are collapsed so a file listed twice is
// only frozen once. An absent proposal-locks/ dir yields no paths.
func gatherProposalLocks(root string) ([]string, error) {
	dir := filepath.Join(root, GoldenLockDir, ProposalLocksDir)
	listFiles, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var paths []string
	seen := make(map[string]bool)
	for _, listFile := range listFiles {
		name := listFile.Name()
		if listFile.IsDir() || strings.HasPrefix(name, ".") {
			continue
		}
		listPath := filepath.Join(dir, name)
		data, err := os.ReadFile(listPath)
		if err != nil {
			return nil, fmt.Errorf("reading %s: %w", listPath, err)
		}
		for _, rawLine := range strings.Split(string(data), "\n") {
			line := strings.TrimSpace(rawLine)
			if line == "" {
				continue
			}
			var abs string
			if filepath.IsAbs(line) {
				abs = filepath.Clean(line)
			} else {
				abs = filepath.Join(root, filepath.FromSlash(line))
			}
			relPath, err := NormalizePath(root, abs)
			if err != nil {
				fmt.Fprintf(os.Stderr, "note: %s lists %q which is not a valid repo path; skipping: %v\n", name, line, err)
				continue
			}
			if seen[relPath] {
				continue
			}
			if _, err := os.Stat(abs); err != nil {
				fmt.Fprintf(os.Stderr, "note: %s lists %q but it was not found; skipping\n", name, line)
				continue
			}
			seen[relPath] = true
			paths = append(paths, abs)
		}
	}
	return paths, nil
}

// runLock implements `lock [<file>...]`: discover repo root, hash each file,
// upsert manifest entries, persist the manifest, then lock the files + the
// manifest. With no file arguments, the lock set is gathered from
// golden-lock/proposal-locks/. Returns a write exit code.
func runLock(args []string) int {
	if !IsRoot() {
		fmt.Fprintf(os.Stderr, "%s lock: must run as root (use sudo)\n", progName())
		return ExitWriteNotRoot
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s lock: cannot determine working directory: %v\n", progName(), err)
		return ExitWriteIO
	}
	root, err := FindRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s lock: cannot find repo root: %v\n", progName(), err)
		return ExitWriteArgs
	}

	// No explicit files → lock everything listed under golden-lock/proposal-locks/
	// (each file there is a newline-separated list of repo paths). Missing listed
	// files are reported and skipped, not fatal (feat-005).
	proposalLocksRel := filepath.Join(GoldenLockDir, ProposalLocksDir)
	if len(args) == 0 {
		args, err = gatherProposalLocks(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s lock: cannot read %s/: %v\n", progName(), proposalLocksRel, err)
			return ExitWriteIO
		}
		if len(args) == 0 {
			fmt.Fprintf(os.Stderr, "%s lock: no files given and %s/ lists none\n", progName(), proposalLocksRel)
			return ExitWriteArgs
		}
	}

	// Distinguish a genuinely absent manifest (start fresh) from a present but
	// malformed/corrupt one (#3). On malformed, ABORT — never overwrite the
	// trust anchor and silently drop previously-locked entries.
	m, err := ReadManifest(root)
	if err != nil {
		if os.IsNotExist(err) {
			m = &Manifest{Root: root, Path: LockfilePath(root)}
		} else if errors.Is(err, ErrManifestMalformed) {
			fmt.Fprintf(os.Stderr, "%s lock: %s is present but malformed; refusing to overwrite (fix or remove it manually): %v\n", progName(), LockfileName, err)
			return ExitWriteIO
		} else {
			fmt.Fprintf(os.Stderr, "%s lock: cannot read manifest: %v\n", progName(), err)
			return ExitWriteIO
		}
	}

	// FREEZE-THEN-HASH (Vectors D+E). For each file: open via the symlink-free
	// resolver (no swappable intermediate dir), FREEZE it first (fchown root:0 +
	// fchmod 0444 on the fd), THEN hash the now-immutable fd, THEN record the
	// entry. This eliminates the content-mutation window on the same inode (D):
	// there is no writable interval after the hash is taken because the file is
	// already 444 by then. We accumulate all entries and do a SINGLE
	// WriteManifestLocked at the very end, so a mid-loop kill never publishes a
	// manifest that asserts immutability over a not-yet-frozen file (E).
	type locked struct {
		relPath, hash string
		f             *os.File
		immutable     bool // false when the filesystem cannot store the flag
	}
	pending := make([]locked, 0, len(args))
	defer func() {
		for _, p := range pending {
			if p.f != nil {
				p.f.Close()
			}
		}
	}()

	for _, f := range args {
		relPath, err := NormalizePath(root, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s lock: bad path %q: %v\n", progName(), f, err)
			return ExitWriteArgs
		}
		abs := m.AbsPath(relPath)
		// O_RDWR so we can both freeze and hash; resolver refuses a symlink at
		// ANY component (leaf or intermediate dir) — Vector A.
		fh, err := openWritableResolved(root, relPath, abs)
		if err != nil {
			if errors.Is(err, ErrSymlink) {
				fmt.Fprintf(os.Stderr, "%s lock: refusing %q: path (or a parent) is a symlink\n", progName(), f)
				return ExitWriteArgs
			}
			fmt.Fprintf(os.Stderr, "%s lock: cannot open %q: %v\n", progName(), f, err)
			return ExitWriteIO
		}
		// Freeze BEFORE hashing — no writable window after the hash (Vector D).
		if err := LockFileFD(fh); err != nil {
			fh.Close()
			fmt.Fprintf(os.Stderr, "%s lock: cannot freeze %q: %v\n", progName(), f, err)
			return ExitWriteIO
		}
		// Hash the now-frozen fd. Seek to 0 in case the open positioned us at EOF.
		if _, err := fh.Seek(0, io.SeekStart); err != nil {
			fh.Close()
			fmt.Fprintf(os.Stderr, "%s lock: cannot rewind %q: %v\n", progName(), f, err)
			return ExitWriteIO
		}
		hash, err := hashReader(relPath, fh)
		if err != nil {
			fh.Close()
			fmt.Fprintf(os.Stderr, "%s lock: cannot read %q: %v\n", progName(), f, err)
			return ExitWriteIO
		}
		// Set the filesystem immutable flag so the inode can't be replaced by
		// rename (the gap chmod 0444 alone leaves open). An unsupported
		// filesystem degrades to detection-only rather than failing the lock.
		immutable, err := applyImmutable(fh)
		if err != nil {
			fh.Close()
			fmt.Fprintf(os.Stderr, "%s lock: cannot set immutable flag on %q: %v\n", progName(), f, err)
			return ExitWriteIO
		}
		pending = append(pending, locked{relPath: relPath, hash: hash, f: fh, immutable: immutable})
	}

	// All files are now frozen 0444. Build the manifest in memory and emit drift
	// / idempotency notices, then publish ONCE at the end.
	for _, p := range pending {
		if i := m.Find(p.relPath); i >= 0 {
			if m.Entries[i].Hash == p.hash {
				fmt.Printf("note: %s already locked, unchanged\n", p.relPath)
			} else {
				fmt.Printf("note: %s already locked; updating recorded hash to match current content\n", p.relPath)
			}
		}
		m.Upsert(p.relPath, p.hash)
	}

	// SINGLE manifest publish at the very end (Vector E): temp → root:0/444 →
	// Renameat within the verified repo-root dir-fd. The live manifest stays 444
	// throughout and is only written once every listed file is already frozen.
	if err := WriteManifestLocked(m); err != nil {
		fmt.Fprintf(os.Stderr, "%s lock: cannot write manifest: %v\n", progName(), err)
		return ExitWriteIO
	}

	for _, p := range pending {
		if p.immutable {
			fmt.Printf("locked %s\n", p.relPath)
		} else {
			fmt.Printf("locked %s  (warning: this filesystem does not support the immutable flag; tamper is detected by `verify` but not prevented)\n", p.relPath)
		}
	}
	return ExitWriteOK
}

// runUnlock implements `unlock <file>...`: unlock + restore the files, remove
// their entries, then persist (and re-lock) the manifest. Returns a write exit code.
func runUnlock(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "%s unlock: no files given\n", progName())
		return ExitWriteArgs
	}
	if !IsRoot() {
		fmt.Fprintf(os.Stderr, "%s unlock: must run as root (use sudo)\n", progName())
		return ExitWriteNotRoot
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s unlock: cannot determine working directory: %v\n", progName(), err)
		return ExitWriteIO
	}
	root, err := FindRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s unlock: cannot find repo root: %v\n", progName(), err)
		return ExitWriteArgs
	}

	m, err := ReadManifest(root)
	if err != nil {
		// A malformed-but-present manifest must NOT be wiped (#3); absent →
		// nothing to unlock. Either way, abort without mutating anything.
		if errors.Is(err, ErrManifestMalformed) {
			fmt.Fprintf(os.Stderr, "%s unlock: %s is present but malformed; refusing to modify it: %v\n", progName(), LockfileName, err)
			return ExitWriteIO
		}
		fmt.Fprintf(os.Stderr, "%s unlock: no readable manifest: %v\n", progName(), err)
		return ExitWriteIO
	}

	// Resolve all paths AND verify membership BEFORE mutating anything (#6).
	// An untracked arg is an arg error (5); we must not chown/chmod any file
	// (nor leave earlier args already unlocked) when a later arg is rejected.
	relPaths := make([]string, 0, len(args))
	for _, f := range args {
		relPath, err := NormalizePath(root, f)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s unlock: bad path %q: %v\n", progName(), f, err)
			return ExitWriteArgs
		}
		if m.Find(relPath) < 0 {
			fmt.Fprintf(os.Stderr, "%s unlock: %q is not listed in %s\n", progName(), relPath, LockfileName)
			return ExitWriteArgs
		}
		relPaths = append(relPaths, relPath)
	}

	for _, relPath := range relPaths {
		abs := m.AbsPath(relPath)
		// Open via the symlink-free resolver (repo-root dir-fd walk) so a swapped
		// parent/intermediate directory cannot redirect the chown/chmod (Vector A).
		fh, err := openWritableResolved(root, relPath, abs)
		if err != nil {
			if errors.Is(err, ErrSymlink) {
				fmt.Fprintf(os.Stderr, "%s unlock: refusing %q: path (or a parent) is a symlink\n", progName(), relPath)
				return ExitWriteArgs
			}
			fmt.Fprintf(os.Stderr, "%s unlock: cannot open %q: %v\n", progName(), relPath, err)
			return ExitWriteIO
		}
		if err := UnlockFileFD(fh); err != nil {
			fh.Close()
			fmt.Fprintf(os.Stderr, "%s unlock: cannot restore %q: %v\n", progName(), relPath, err)
			return ExitWriteIO
		}
		fh.Close()
		m.Remove(relPath)
	}

	// If unlocking emptied the manifest, remove the manifest file entirely so a
	// later `verify` returns exit 3 (absent) rather than a misleading exit 0 on
	// a vacuously-OK empty manifest (#9). The live manifest is 444; removing it
	// only needs write permission on the (operator-owned) repo-root directory.
	if len(m.Entries) == 0 {
		// The live manifest is root:0444 and (where supported) immutable; clear
		// the flag first or the unlink below is refused. Open via the
		// symlink-free resolver so a swapped manifest symlink can't redirect us.
		if mf, err := resolveNoSymlink(root, LockfileRelPath, m.Path, os.O_RDONLY); err == nil {
			_ = clearImmutable(mf)
			mf.Close()
		}
		if err := os.Remove(m.Path); err != nil && !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s unlock: cannot remove emptied manifest: %v\n", progName(), err)
			return ExitWriteIO
		}
		for _, relPath := range relPaths {
			fmt.Printf("unlocked %s\n", relPath)
		}
		fmt.Printf("note: %s now empty; removed it (verify will report absent)\n", LockfileName)
		return ExitWriteOK
	}

	// Rewrite the manifest via temp-file → root:0/444 → atomic rename so the
	// live trust anchor stays 444 throughout and is never left writable on a
	// partial failure (#4).
	if err := WriteManifestLocked(m); err != nil {
		fmt.Fprintf(os.Stderr, "%s unlock: cannot write manifest: %v\n", progName(), err)
		return ExitWriteIO
	}

	for _, relPath := range relPaths {
		fmt.Printf("unlocked %s\n", relPath)
	}
	return ExitWriteOK
}

// runVerify implements `verify`: runs Verify, prints per-entry results, and
// returns the read exit code (0/1/2/3).
func runVerify(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s verify: cannot determine working directory: %v\n", progName(), err)
		return ExitVerifyLockfile
	}
	root, err := FindRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s verify: cannot find repo root: %v\n", progName(), err)
		return ExitVerifyLockfile
	}

	results, code := Verify(root)
	if code == ExitVerifyLockfile {
		fmt.Fprintf(os.Stderr, "%s verify: %s absent or malformed\n", progName(), LockfileName)
		return code
	}

	for _, r := range results {
		switch r.Status {
		case StatusOK:
			fmt.Printf("ok       %s\n", r.Path)
		case StatusMismatch:
			fmt.Printf("MISMATCH %s\n  expected %s\n  actual   %s\n", r.Path, r.Expected, r.Actual)
		case StatusMissing:
			fmt.Printf("MISSING  %s\n", r.Path)
		}
	}

	switch code {
	case ExitVerifyOK:
		fmt.Printf("%d file(s) verified, all OK\n", len(results))
	case ExitVerifyMismatch:
		fmt.Fprintln(os.Stderr, "verify failed: hash mismatch")
	case ExitVerifyMissing:
		fmt.Fprintln(os.Stderr, "verify failed: missing file(s)")
	}
	return code
}

// usage prints help text to w and is invoked for --help, no args, or an
// unknown subcommand.
func usage() {
	p := progName()
	fmt.Fprintf(os.Stderr, `%s — make chosen files immutable so they can't be silently changed

USAGE:
    %s <command> [arguments]

COMMANDS:
    setup              Scaffold %s/ for the proposal-locks workflow and
                       write a getting-started guide. Needs no privilege.
    lock   [<file>...] Hash each file, record it in %s, then root-own + chmod 444
                       the file(s) and the manifest. Requires sudo. With no
                       files, locks every path listed under %s/.
    unlock <file>...   Remove file(s) from the manifest and restore writable
                       ownership/permissions. Requires sudo.
    verify             Recompute the SHA-256 of every manifest entry and compare.
                       Needs no privilege (safe for CI).
    version            Print the version plus build info (revision, Go version).
                       Also available as --version / -v. Needs no privilege.

EXIT CODES:
    verify:  0 ok | 1 hash mismatch | 2 missing file | 3 absent/malformed manifest
    lock/unlock:  0 ok | 4 not root | 5 arg error | 6 io failure
`, p, p, GoldenLockDir, LockfileRelPath, filepath.Join(GoldenLockDir, ProposalLocksDir))
}

// dispatch routes argv (excluding the program name) to the matching run* func
// and returns the process exit code. Exposed for testing.
func dispatch(args []string) int {
	if len(args) == 0 {
		usage()
		return ExitWriteArgs
	}
	switch args[0] {
	case "setup":
		return runSetup(args[1:])
	case "lock":
		return runLock(args[1:])
	case "unlock":
		return runUnlock(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "version", "--version", "-v":
		return runVersion(args[1:])
	case "--help", "-h", "help":
		usage()
		return ExitWriteOK
	default:
		fmt.Fprintf(os.Stderr, "%s: unknown command %q\n\n", progName(), args[0])
		usage()
		return ExitWriteArgs
	}
}

func main() {
	os.Exit(dispatch(os.Args[1:]))
}
