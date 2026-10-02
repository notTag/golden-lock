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
// Unusable paths are returned as failures alongside any successfully expanded
// files, so one bad input does not prevent independent files from being locked.
func expandLockTargets(root string, inputs []string) ([]string, []lockFailure) {
	stateDirAbs, err := filepath.Abs(filepath.Join(root, GoldenLockDir))
	if err != nil {
		return nil, []lockFailure{{root, err, ExitWriteIO}}
	}

	var files []string
	var failures []lockFailure
	seen := make(map[string]bool)
	// De-duplicate on the cleaned ABSOLUTE path so the same file reached via a
	// relative directory and an absolute argument (e.g. `lock core /repo/core/a.go`)
	// collapses to one target — otherwise the second pass reopens an already-frozen
	// file and can leave it frozen but unpublished (Codex review P1).
	add := func(path string) {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = filepath.Clean(path)
		}
		if seen[abs] {
			return
		}
		seen[abs] = true
		files = append(files, abs)
	}

	for _, input := range inputs {
		info, err := os.Lstat(input)
		if err != nil {
			failures = append(failures, lockFailure{input, err, ExitWriteIO})
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			failures = append(failures, lockFailure{input, ErrSymlink, ExitWriteArgs})
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
		//
		// Compare the dot-entry check against the CLEANED walk root so an explicitly
		// named dot-directory with a trailing separator (`lock .github/`) is not
		// mistaken for a nested dot-entry and skipped (Codex review P2).
		walkRoot := filepath.Clean(input)
		walkErr := filepath.Walk(input, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				failures = append(failures, lockFailure{path, err, ExitWriteIO})
				return nil
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
			isDotEntry := strings.HasPrefix(info.Name(), ".") && filepath.Clean(path) != walkRoot
			if isDotEntry {
				if info.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.IsDir() {
				return nil
			}
			// Report symlinks as failed targets. Other special entries (fifos,
			// devices, sockets) are excluded from directory sweeps.
			if info.Mode()&os.ModeSymlink != 0 {
				failures = append(failures, lockFailure{path, ErrSymlink, ExitWriteArgs})
				return nil
			}
			if info.Mode().IsRegular() {
				add(path)
			}
			return nil
		})
		if walkErr != nil {
			failures = append(failures, lockFailure{input, walkErr, ExitWriteIO})
		}
	}
	return files, failures
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

// rootForNewManifest resolves the root that the state-creating commands (`lock`,
// `setup`) record paths against. A repo — a `.git` ancestor, or an existing
// manifest from an earlier lock — anchors it as usual. When neither marker exists
// anywhere above, the directory is not a project at all, so the working directory
// becomes the root and the state is born there; locking a loose file needs no
// separate bootstrap step. looseDir reports that fallback so the caller can say
// where the state is about to appear.
//
// Only the creating commands fall back. verify, list and unlock still require a
// real root: without a manifest there is nothing for them to act on.
//
// ponytail: the marker search only walks UP, so locking work/f2 from a directory
// ABOVE an existing work/golden-lock/golden.lock creates a second, independent
// manifest instead of joining the first, and neither one then covers both files.
// Scan downward for an existing manifest if that collision shows up in practice.
func rootForNewManifest(cwd string) (root string, looseDir bool) {
	if repoRoot, err := FindRepoRoot(cwd); err == nil {
		return repoRoot, false
	}
	return cwd, true
}

// lockSweepWarnThreshold is the file count above which a directory sweep (e.g.
// `lock .`) asks for confirmation before freezing. It guards against an
// accidental repo-wide lock pulling in build output / dependencies; it is not a
// hard limit — the operator confirms interactively, or passes --yes, to proceed.
// ponytail: fixed threshold, promote to a flag only if someone needs to tune it.
const lockSweepWarnThreshold = 100

// stdinIsTerminal reports whether stdin is an interactive terminal. A large
// sweep in a non-interactive context (CI, a pipe) must refuse rather than block
// on a prompt no one can answer.
func stdinIsTerminal() bool {
	info, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// confirmLargeSweep warns that a directory sweep will freeze every regular file
// on disk under dirs (regardless of .gitignore) and returns true only if the
// reader answers yes. It reads one line; anything but y/yes — including an empty
// line or EOF — is a No.
func confirmLargeSweep(w io.Writer, in io.Reader, dirs []string, fileCount int) bool {
	fmt.Fprintf(w, "%s lock: about to lock %d files under %s.\n", progName(), fileCount, strings.Join(dirs, ", "))
	fmt.Fprint(w, "This freezes EVERY regular file on disk there — including build output and\n")
	fmt.Fprint(w, "dependencies, regardless of .gitignore — and each must be unlocked before it\n")
	fmt.Fprint(w, "can be edited again.\n")
	fmt.Fprint(w, "Proceed? [y/N]: ")
	var response string
	if _, err := fmt.Fscanln(in, &response); err != nil {
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(response))
	return answer == "y" || answer == "yes"
}

// runLock implements `lock [-y] [<path>...]`: discover repo root, hash each file,
// upsert manifest entries, persist the manifest, then lock the files + the
// manifest. With no file arguments, the lock set is gathered from
// golden-lock/proposal-locks/. Returns a write exit code.
func runLock(args []string) int {
	fs := flag.NewFlagSet("lock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	skipSweepPrompt := fs.Bool("yes", false, "skip the confirmation prompt for a large directory sweep")
	fs.BoolVar(skipSweepPrompt, "y", false, "shorthand for --yes")
	if err := fs.Parse(args); err != nil {
		return ExitWriteArgs
	}
	args = fs.Args()

	if !IsRoot() {
		fmt.Fprintf(os.Stderr, "%s lock: must run as root (use sudo)\n", progName())
		return ExitWriteNotRoot
	}

	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s lock: cannot determine working directory: %v\n", progName(), err)
		return ExitWriteIO
	}
	root, looseDir := rootForNewManifest(cwd)

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

	// Note which inputs are directories BEFORE expanding, so a large sweep can be
	// confirmed below. `lock .` in particular pulls in build output and
	// dependencies regardless of .gitignore (the expansion walks the filesystem,
	// not git), so an accidental repo-wide lock is a real footgun.
	var sweptDirs []string
	for _, input := range args {
		if info, statErr := os.Lstat(input); statErr == nil && info.IsDir() {
			sweptDirs = append(sweptDirs, input)
		}
	}

	// Expand any directory arguments into their contained regular files so the
	// freeze-then-hash flow below runs per file, one manifest entry each (feat-006).
	args, failures := expandLockTargets(root, args)
	defer func() { reportLockFailures(failures) }()
	if len(args) == 0 {
		if len(failures) > 0 {
			return lockFailureCode(failures)
		}
		fmt.Fprintf(os.Stderr, "%s lock: nothing to lock — the given path(s) held no regular files (empty directory, or only dotfiles/symlinks)\n", progName())
		return ExitWriteArgs
	}

	// Confirm before freezing a large tree swept from a directory argument. In a
	// non-interactive context (CI, a pipe) there is no one to answer, so refuse
	// and point at --yes rather than hang. --yes skips the prompt outright.
	if len(sweptDirs) > 0 && len(args) > lockSweepWarnThreshold && !*skipSweepPrompt {
		if !stdinIsTerminal() {
			fmt.Fprintf(os.Stderr, "%s lock: refusing to lock %d files swept from %s without confirmation; re-run with --yes to proceed non-interactively\n",
				progName(), len(args), strings.Join(sweptDirs, ", "))
			return ExitWriteArgs
		}
		if !confirmLargeSweep(os.Stderr, os.Stdin, sweptDirs, len(args)) {
			fmt.Fprintf(os.Stderr, "%s lock: aborted\n", progName())
			return ExitWriteArgs
		}
	}

	// Announce the root chosen for this attempted lock.
	// Diagnostics go to stderr, like every other lock pre-flight notice.
	if looseDir {
		fmt.Fprintf(os.Stderr, "note: %s is not in a project (no .git, no existing manifest); using this directory as the root for %s\n", cwd, LockfileRelPath)
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

	batchFailures := lockBatch(m, args, defaultLockOps())
	failures = append(failures, batchFailures...)
	return lockFailureCode(failures)
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

// runList implements `list`: prints the repo-root-relative path of every
// manifest entry. It reads the same trust anchor as verify but hashes nothing,
// so an entry appears here whether or not the file on disk still matches.
func runList(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s list: cannot determine working directory: %v\n", progName(), err)
		return ExitVerifyLockfile
	}
	root, err := FindRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s list: cannot find repo root: %v\n", progName(), err)
		return ExitVerifyLockfile
	}

	manifest, err := ReadManifest(root)
	if err != nil {
		absentOrMalformed := os.IsNotExist(err) || errors.Is(err, ErrManifestMalformed)
		if absentOrMalformed {
			fmt.Fprintf(os.Stderr, "%s list: %s absent or malformed\n", progName(), LockfileName)
		} else {
			fmt.Fprintf(os.Stderr, "%s list: %s present but unreadable: %v\n", progName(), LockfileName, err)
		}
		return ExitVerifyLockfile
	}

	for _, entry := range manifest.Entries {
		fmt.Println(entry.Path)
	}
	fmt.Printf("%d file(s) locked\n", len(manifest.Entries))
	return ExitVerifyOK
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
                       write a getting-started guide. Outside a project it
                       scaffolds in the working directory. Needs no privilege.
    lock   [-y] [<path>...]
                       Hash each file, record it in %s, then root-own + chmod 444
                       the file(s) and the manifest. Requires sudo. A directory
                       path locks every regular file under it, recursively — and
                       a large sweep (e.g. 'lock .', which ignores .gitignore)
                       prompts for confirmation first; pass -y/--yes to skip it.
                       With no paths, locks every path listed under %s/.
                       Outside a project (no .git, no manifest above), the
                       working directory becomes the root and the manifest is
                       created there — no bootstrap step needed.
                       Continues after per-file errors, records successes, and
                       lists failed paths on stderr with a nonzero exit status.
    unlock [<path>...] Remove file(s) from the manifest and restore writable
                       ownership/permissions. Requires sudo. A directory path
                       unlocks the locked files beneath it. With no paths,
                       unlocks every path listed under %s/.
    verify             Recompute the SHA-256 of every manifest entry and compare.
                       Needs no privilege (safe for CI).
    list               Print the path of every file recorded in %s.
                       Hashes nothing — use verify to check them. Needs no privilege.
    version            Print the version plus build info (revision, Go version).
                       Also available as --version / -v. Needs no privilege.

EXIT CODES:
    verify:  0 ok | 1 hash mismatch | 2 missing file | 3 absent/malformed manifest
    list:    0 ok | 3 absent/malformed manifest
    lock/unlock:  0 ok | 4 not root | 5 arg error | 6 io failure
    lock reports the WORST code of the batch: files it locked successfully stay
    locked and recorded even when it exits 5 or 6, so a non-zero lock does NOT
    mean nothing changed. Read the per-file output, or run list / verify.
`, p, p, GoldenLockDir, LockfileRelPath, filepath.Join(GoldenLockDir, ProposalLocksDir), filepath.Join(GoldenLockDir, ProposalLocksDir), LockfileRelPath)
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
	case "list":
		return runList(args[1:])
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
