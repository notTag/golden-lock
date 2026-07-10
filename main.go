package main

import (
	"errors"
	"flag"
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

// expandLockTargets flattens each input path into the concrete set of regular
// files to lock. A directory input is walked recursively and every regular file
// under it is collected; a plain file input is kept as-is. Dotfiles and
// dot-directories (.git, .DS_Store, …) are skipped, matching the proposal-locks
// listing rules (gatherProposalLocks). Symlinks — whether an input itself or an
// entry found during the walk — are skipped rather than locked; the per-file
// freeze flow additionally refuses any symlinked path component, so this is
// defence in depth (feat-006). Golden Lock's own state directory (golden-lock/,
// holding the manifest and proposal-locks) is pruned so `lock .` never freezes
// and hashes the manifest as user content — which would leave the rewritten
// live manifest inconsistent with its own recorded hash and fail verify. Results
// are absolute, cleaned, and de-duplicated so a file reached both explicitly and
// via a directory is only locked once.
func expandLockTargets(root string, inputs []string) ([]string, error) {
	stateDirAbs, err := filepath.Abs(filepath.Join(root, GoldenLockDir))
	if err != nil {
		return nil, err
	}

	var files []string
	seen := make(map[string]bool)
	add := func(path string) {
		clean := filepath.Clean(path)
		if seen[clean] {
			return
		}
		seen[clean] = true
		files = append(files, clean)
	}

	for _, input := range inputs {
		info, err := os.Lstat(input)
		if err != nil {
			return nil, fmt.Errorf("cannot stat %q: %w", input, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			fmt.Fprintf(os.Stderr, "note: skipping %q: path is a symlink\n", input)
			continue
		}
		if !info.IsDir() {
			add(input)
			continue
		}
		// filepath.Walk lstats every entry, so directory / symlink / regular-file
		// classification comes from a real stat rather than readdir type bits —
		// which some filesystems report as unknown, making WalkDir mis-recurse or
		// mistake a subdirectory for a regular file (Codex review P2).
		walkErr := filepath.Walk(input, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			// Prune Golden Lock's own state dir (golden-lock/) so a root-level
			// `lock .` never treats the manifest / proposal-locks as user content.
			if info.IsDir() {
				if abs, absErr := filepath.Abs(path); absErr == nil && abs == stateDirAbs {
					return filepath.SkipDir
				}
			}
			// Skip dot-entries anywhere below the input root (the root itself was
			// explicitly named, so it is never skipped). A dot-directory is pruned
			// whole; a dotfile is just ignored.
			isDotEntry := strings.HasPrefix(info.Name(), ".") && path != input
			if isDotEntry {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			// Only regular files are lockable; symlinks and specials (fifos,
			// devices, sockets) are silently passed over.
			if info.Mode()&os.ModeSymlink != 0 {
				fmt.Fprintf(os.Stderr, "note: skipping %q: symlink\n", path)
				return nil
			}
			if info.Mode().IsRegular() {
				add(path)
			}
			return nil
		})
		if walkErr != nil {
			return nil, fmt.Errorf("walking %q: %w", input, walkErr)
		}
	}
	return files, nil
}

// expandUnlockTargets resolves each unlock argument to the manifest-relative
// paths it refers to. A directory argument expands to exactly the manifest
// entries beneath it — untracked files on disk are ignored, preserving unlock's
// rule that it never touches a file it did not lock — and a directory with no
// locked files under it is reported and skipped. The repo root itself (e.g.
// `unlock .`) expands to every entry, the symmetric inverse of a repo-wide
// `lock .`. A file argument keeps the strict rule that an explicitly-named path
// must itself be locked, else it is an argument error. Results are de-duplicated
// so a path named both directly and via a parent directory is unlocked once
// (feat-006).
func expandUnlockTargets(m *Manifest, root string, inputs []string) ([]string, error) {
	var relPaths []string
	seen := make(map[string]bool)
	add := func(relPath string) {
		if seen[relPath] {
			return
		}
		seen[relPath] = true
		relPaths = append(relPaths, relPath)
	}

	for _, input := range inputs {
		// Detect a directory via lstat so a symlinked "directory" is not followed.
		info, statErr := os.Lstat(input)
		isDir := statErr == nil && info.IsDir()

		// The repo root has no NormalizePath form (it is rejected as "resolves to
		// the repo root itself"), so handle it before normalizing: a directory
		// argument pointing at the root unlocks every entry.
		if isDir && isRepoRoot(root, input) {
			if len(m.Entries) == 0 {
				fmt.Fprintln(os.Stderr, "note: no locked files to unlock; skipping")
				continue
			}
			for i := range m.Entries {
				add(m.Entries[i].Path)
			}
			continue
		}

		relPath, err := NormalizePath(root, input)
		if err != nil {
			return nil, fmt.Errorf("bad path %q: %w", input, err)
		}
		if isDir {
			under := m.entriesUnder(relPath)
			if len(under) == 0 {
				fmt.Fprintf(os.Stderr, "note: %s has no locked files under it; skipping\n", relPath)
				continue
			}
			for _, entryPath := range under {
				add(entryPath)
			}
			continue
		}
		// A plain file argument must itself be locked (unchanged strict rule).
		if m.Find(relPath) < 0 {
			return nil, fmt.Errorf("%q is not listed in %s", relPath, LockfileName)
		}
		add(relPath)
	}
	return relPaths, nil
}

// isRepoRoot reports whether input resolves to the same absolute path as root.
// Used to treat a directory argument that points at the repo root (e.g. `.`) as
// "every manifest entry", since NormalizePath deliberately rejects the root.
func isRepoRoot(root, input string) bool {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	inputAbs, err := filepath.Abs(input)
	if err != nil {
		return false
	}
	return filepath.Clean(rootAbs) == filepath.Clean(inputAbs)
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

	// Expand any directory arguments into their contained regular files so the
	// freeze-then-hash flow below runs per file, one manifest entry each (feat-006).
	args, err = expandLockTargets(root, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s lock: %v\n", progName(), err)
		return ExitWriteIO
	}
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "%s lock: nothing to lock — the given path(s) held no regular files (empty directory, or only dotfiles/symlinks)\n", progName())
		return ExitWriteArgs
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

// runUnlock implements `unlock [<file>...]`: unlock + restore the files, remove
// their entries, then persist (and re-lock) the manifest. With no file arguments
// it unlocks every path listed under golden-lock/proposal-locks/ — the symmetric
// inverse of `lock` with no args. Returns a write exit code.
func runUnlock(args []string) int {
	fs := flag.NewFlagSet("unlock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	uidFlag := fs.Int("uid", 0, "uid to restore unlocked file ownership to (required from a bare root shell)")
	gidFlag := fs.Int("gid", 0, "gid to restore ownership to (defaults to the uid's primary group)")
	if err := fs.Parse(args); err != nil {
		return ExitWriteArgs
	}
	uidProvided, gidProvided := false, false
	fs.Visit(func(fl *flag.Flag) {
		switch fl.Name {
		case "uid":
			uidProvided = true
		case "gid":
			gidProvided = true
		}
	})
	fileArgs := fs.Args()

	if !IsRoot() {
		fmt.Fprintf(os.Stderr, "%s unlock: must run as root (use sudo)\n", progName())
		return ExitWriteNotRoot
	}

	// A bare root shell (sudo -i / root login) has no SUDO_UID, so without an
	// explicit --uid the chown below would hand the file to 0:0 and leave it
	// uneditable by an ordinary user. Refuse, and tell the operator how to find
	// the uid (#19).
	sudoUIDSet := os.Getenv("SUDO_UID") != ""
	if rootShellNeedsExplicitUID(os.Geteuid(), sudoUIDSet, uidProvided) {
		fmt.Fprintf(os.Stderr, "%s unlock: running as root with no SUDO_UID; pass --uid=<n> so the unlocked file is owned by a real user (find it with: id -u <username>)\n", progName())
		return ExitWriteArgs
	}
	restoreUID, restoreGID := restoreIdentity(uidProvided, *uidFlag, gidProvided, *gidFlag)

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

	// No explicit files → unlock everything listed under golden-lock/proposal-locks/,
	// the symmetric inverse of `lock` with no args (#34). The same gatherer is
	// reused, so missing listed files are reported and skipped, not fatal.
	proposalLocksRel := filepath.Join(GoldenLockDir, ProposalLocksDir)
	if len(fileArgs) == 0 {
		fileArgs, err = gatherProposalLocks(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s unlock: cannot read %s/: %v\n", progName(), proposalLocksRel, err)
			return ExitWriteIO
		}
		if len(fileArgs) == 0 {
			fmt.Fprintf(os.Stderr, "%s unlock: no files given and %s/ lists none\n", progName(), proposalLocksRel)
			return ExitWriteArgs
		}
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
	// (nor leave earlier args already unlocked) when a later arg is rejected. A
	// directory arg expands to the manifest entries beneath it (feat-006).
	relPaths, err := expandUnlockTargets(m, root, fileArgs)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s unlock: %v\n", progName(), err)
		return ExitWriteArgs
	}
	if len(relPaths) == 0 {
		fmt.Fprintf(os.Stderr, "%s unlock: nothing to unlock — the given path(s) held no locked files\n", progName())
		return ExitWriteArgs
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
		if err := UnlockFileFDAs(fh, restoreUID, restoreGID); err != nil {
			fh.Close()
			fmt.Fprintf(os.Stderr, "%s unlock: cannot restore %q: %v\n", progName(), relPath, err)
			return ExitWriteIO
		}
		fh.Close()
		m.Remove(relPath)
	}

	// If unlocking emptied the manifest, remove the manifest file entirely so a
	// later `verify` returns exit 3 (absent) rather than a misleading exit 0 on
	// a vacuously-OK empty manifest (#9). The removal goes through the same
	// dir-fd discipline as every other privileged mutation — no path-based op (#20).
	if len(m.Entries) == 0 {
		// Resolve the manifest's parent dir to a verified dir-fd via the
		// symlink-free walk (the same anchor WriteManifestLocked's Renameat uses),
		// so a swapped parent symlink cannot redirect the unlink. The live manifest
		// is root:0444 and (where supported) immutable; clear its flag first via
		// that dir-fd or the Unlinkat below is refused.
		dirFD, err := openManifestParentDir(root)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s unlock: cannot open manifest dir: %v\n", progName(), err)
			return ExitWriteIO
		}
		defer dirFD.Close()
		clearLiveManifestImmutable(dirFD)
		if err := unlinkAt(dirFD, LockfileName); err != nil && !os.IsNotExist(err) {
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

	results, code, manifestErr := Verify(root)
	if code == ExitVerifyLockfile {
		// Same exit code (3), but distinguish a missing/corrupt trust anchor from
		// one that is present-but-unreadable. An os.IsNotExist or malformed error
		// means the anchor is gone or garbage; any other error (e.g. EACCES, I/O)
		// means the anchor exists and the operator just can't read it — a
		// different remediation (fix permissions, not re-lock) (#29).
		absentOrMalformed := os.IsNotExist(manifestErr) || errors.Is(manifestErr, ErrManifestMalformed)
		if manifestErr != nil && !absentOrMalformed {
			fmt.Fprintf(os.Stderr, "%s verify: %s present but unreadable: %v\n", progName(), LockfileName, manifestErr)
		} else {
			fmt.Fprintf(os.Stderr, "%s verify: %s absent or malformed\n", progName(), LockfileName)
		}
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
    lock   [<path>...] Hash each file, record it in %s, then root-own + chmod 444
                       the file(s) and the manifest. Requires sudo. A directory
                       path locks every regular file under it, recursively. With
                       no paths, locks every path listed under %s/.
    unlock [<path>...] Remove file(s) from the manifest and restore writable
                       ownership/permissions. Requires sudo. A directory path
                       unlocks the locked files beneath it. With no paths,
                       unlocks every path listed under %s/.
    verify             Recompute the SHA-256 of every manifest entry and compare.
                       Needs no privilege (safe for CI).
    version            Print the version plus build info (revision, Go version).
                       Also available as --version / -v. Needs no privilege.

EXIT CODES:
    verify:  0 ok | 1 hash mismatch | 2 missing file | 3 absent/malformed manifest
    lock/unlock:  0 ok | 4 not root | 5 arg error | 6 io failure
`, p, p, GoldenLockDir, LockfileRelPath, filepath.Join(GoldenLockDir, ProposalLocksDir), filepath.Join(GoldenLockDir, ProposalLocksDir))
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
	case "--version", "-v", "version":
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
