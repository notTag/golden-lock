package main

// lockfile.go — golden.lock manifest model: parse/read/write,
// repo-root discovery, and path normalization.
//
// Manifest line format (one entry per line):
//
//	<sha256>  <relpath>
//
// where <sha256> is lowercase hex, the separator is a run of spaces, and
// <relpath> is repo-root-relative, forward-slash, normalized. Lines whose
// first non-space byte is '#' are comments. Blank lines are ignored.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unicode"
)

// GoldenLockDir is the repo-root directory that holds golden-lock's state: the
// manifest (LockfileName) and the proposal-locks list files. It is created on
// the first lock if absent.
const GoldenLockDir = "golden-lock"

// LockfileName is the fixed manifest filename. It lives inside GoldenLockDir,
// so its leaf name is used where a name relative to that dir-fd is needed
// (Renameat/Openat), and LockfileRelPath where a repo-root-relative path is
// needed (the symlink-free component walk).
const LockfileName = "golden.lock"

// LockfileRelPath is the manifest's repo-root-relative, forward-slash path:
// "golden-lock/golden.lock". Pass this (not LockfileName) to the component-
// walking resolver so the golden-lock segment is verified too.
const LockfileRelPath = GoldenLockDir + "/" + LockfileName

// ErrManifestMalformed is the sentinel returned by ReadManifest when the
// manifest file exists but cannot be parsed (e.g. a malformed entry line).
// It is distinct from an os.IsNotExist error (genuinely absent file), so
// callers can refuse to overwrite a present-but-corrupt trust anchor.
var ErrManifestMalformed = errors.New("manifest malformed")

// Entry is a single manifest record: a stored hash bound to a repo-root-relative path.
type Entry struct {
	Hash string // lowercase hex SHA-256 as recorded in the manifest
	Path string // repo-root-relative, forward-slash, normalized
}

// Manifest is the parsed in-memory model of a golden.lock file.
// Root is the absolute repo-root directory the relative Entry paths resolve against.
// Path is the absolute path to the manifest file on disk.
type Manifest struct {
	Root    string  // absolute repo-root directory
	Path    string  // absolute path to the golden.lock file
	Entries []Entry // tracked entries, in manifest order
}

// FindRepoRoot discovers the repository root starting from the given directory,
// walking upward. Discovery prefers `git rev-parse --show-toplevel`; if git is
// unavailable it falls back to the nearest ancestor containing a .git entry or
// an existing golden.lock. Returns the absolute repo-root path.
func FindRepoRoot(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	// Prefer git's own notion of the top level — but only when we are NOT root.
	// Under root (the sudo lock/unlock paths), exec'ing a git resolved from an
	// attacker-influenced PATH would run an untrusted binary with full
	// privilege (#8). In that case skip the probe entirely and rely on the
	// .git / golden.lock ancestor walk below, which touches no external
	// binary. When unprivileged (verify), resolve git from a sanitized,
	// well-known set of system locations rather than the inherited PATH.
	if !IsRoot() {
		if git := vettedGitPath(); git != "" {
			cmd := exec.Command(git, "rev-parse", "--show-toplevel")
			cmd.Dir = abs
			// Pin a minimal PATH so any child process git might spawn also
			// resolves from trusted locations only.
			cmd.Env = append(os.Environ(), "PATH=/usr/bin:/bin:/usr/local/bin")
			if out, err := cmd.Output(); err == nil {
				top := strings.TrimSpace(string(out))
				if top != "" {
					if topAbs, err := filepath.Abs(top); err == nil {
						return topAbs, nil
					}
					return top, nil
				}
			}
		}
	}

	// Fallback: walk upward looking for a .git entry or an existing lockfile.
	dir := abs
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		if _, err := os.Stat(filepath.Join(dir, GoldenLockDir, LockfileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("repo root not found from %q: no .git or %s ancestor", abs, LockfileRelPath)
}

// vettedGitPath returns an absolute path to a git binary from a fixed list of
// trusted system locations, or "" if none is found. It deliberately does NOT
// consult the inherited PATH, so an attacker-planted ./git or one earlier in
// PATH cannot be selected (#8).
func vettedGitPath() string {
	for _, cand := range []string{
		"/usr/bin/git",
		"/bin/git",
		"/usr/local/bin/git",
		"/opt/homebrew/bin/git",
	} {
		if fi, err := os.Stat(cand); err == nil && !fi.IsDir() {
			return cand
		}
	}
	return ""
}

// LockfilePath returns the absolute path to the manifest
// (root/GoldenLockDir/LockfileName).
func LockfilePath(root string) string {
	return filepath.Join(root, GoldenLockDir, LockfileName)
}

// NormalizePath converts an arbitrary input path (absolute or relative to the
// process working directory) into the repo-root-relative, forward-slash,
// cleaned form used as an Entry.Path. It errors if the resolved path escapes
// the repo root.
func NormalizePath(root, input string) (string, error) {
	if input == "" {
		return "", fmt.Errorf("empty path")
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}

	var abs string
	if filepath.IsAbs(input) {
		abs = filepath.Clean(input)
	} else {
		abs, err = filepath.Abs(input)
		if err != nil {
			return "", err
		}
	}

	relPath, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	relPath = filepath.Clean(relPath)

	if relPath == ".." || strings.HasPrefix(relPath, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes repo root %q", input, rootAbs)
	}
	if relPath == "." {
		return "", fmt.Errorf("path %q resolves to the repo root itself", input)
	}

	return filepath.ToSlash(relPath), nil
}

// AbsPath resolves a repo-root-relative Entry.Path back to an absolute
// filesystem path under the manifest's Root.
func (m *Manifest) AbsPath(relPath string) string {
	return filepath.Join(m.Root, filepath.FromSlash(relPath))
}

// ReadManifest loads and parses the manifest at root.
//
// If the file does not exist it returns the underlying os error (which satisfies
// os.IsNotExist), so callers can distinguish a genuinely absent manifest from a
// present-but-corrupt one. If the file exists but a line cannot be parsed it
// returns an error wrapping ErrManifestMalformed (use errors.Is). Both map to
// the absent/malformed verify exit code (3), but the write path treats them
// differently: absent → start fresh, malformed → abort without overwriting (#3).
func ReadManifest(root string) (*Manifest, error) {
	path := LockfilePath(root)

	// Open the manifest via the symlink-free resolver so a manifest that was
	// swapped for a symlink (to an attacker file with forged hashes) is rejected
	// rather than trusted (Vector B). resolveNoSymlink also fstat-checks that the
	// result is a regular file; a symlinked/non-regular manifest is a HARD reject.
	f, err := resolveNoSymlink(root, LockfileRelPath, path, os.O_RDONLY)
	if err != nil {
		if errors.Is(err, ErrSymlink) {
			// A symlinked manifest (or golden-lock dir) must never be trusted.
			// Surface as malformed so verify maps it to exit 3 and the write path
			// refuses to overwrite.
			return nil, fmt.Errorf("%s: manifest is a symlink or non-regular file: %w", path, ErrManifestMalformed)
		}
		return nil, err
	}
	defer f.Close()

	// Threat model: pre-apply / dev machines are legitimately non-root, so a
	// non-root-owned manifest is a WARNING, not a hard failure. (A symlinked or
	// non-regular manifest was already hard-rejected above.)
	if fi, statErr := f.Stat(); statErr == nil {
		if st, ok := fi.Sys().(*syscall.Stat_t); ok && st.Uid != 0 {
			fmt.Fprintf(os.Stderr, "warning: %s is not root-owned (uid=%d); its integrity is not anchored\n", path, st.Uid)
		}
	}

	m := &Manifest{
		Root: root,
		Path: path,
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		trimmed := strings.TrimLeft(raw, " \t")
		if trimmed == "" {
			continue
		}
		if trimmed[0] == '#' {
			continue
		}

		// Split on the first run of whitespace: "<hash>  <relpath>". The
		// separator may be spaces OR tabs (consistent with the TrimLeft set
		// above, #7). Hash contains no whitespace; the path keeps any interior
		// spaces, only leading separator whitespace is stripped.
		idx := strings.IndexFunc(trimmed, unicode.IsSpace)
		if idx < 0 {
			return nil, fmt.Errorf("%s:%d: malformed line (no separator): %q: %w", path, lineNo, raw, ErrManifestMalformed)
		}
		hash := trimmed[:idx]
		rest := strings.TrimLeftFunc(trimmed[idx:], unicode.IsSpace)
		if hash == "" || rest == "" {
			return nil, fmt.Errorf("%s:%d: malformed line: %q: %w", path, lineNo, raw, ErrManifestMalformed)
		}

		// Reject a parseable-but-malformed hash token (truncated, over-long, or
		// upper-cased hex) HERE rather than letting it flow through to a content
		// comparison. A corrupt hash that never matches any recomputed digest
		// would otherwise surface as a content MISMATCH (verify exit 1,
		// "tampering") when its real cause is a corrupt manifest (exit 3) (#18).
		if !isValidManifestHash(hash) {
			return nil, fmt.Errorf("%s:%d: malformed hash %q: %w", path, lineNo, hash, ErrManifestMalformed)
		}

		m.Entries = append(m.Entries, Entry{Hash: hash, Path: rest})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return m, nil
}

// isValidManifestHash reports whether token is a well-formed manifest hash:
// exactly 64 lowercase hex characters, the textual form of a SHA-256 digest as
// emitted by manifestBody. The strictness is intentional — a SHA-256 verify
// only ever compares against this canonical form, so an upper-cased or
// wrong-length token can never legitimately match and is treated as manifest
// corruption (see ReadManifest, #18).
func isValidManifestHash(token string) bool {
	const sha256HexLen = 64
	if len(token) != sha256HexLen {
		return false
	}
	for i := 0; i < len(token); i++ {
		c := token[i]
		isDigit := c >= '0' && c <= '9'
		isLowerHex := c >= 'a' && c <= 'f'
		if !isDigit && !isLowerHex {
			return false
		}
	}
	return true
}

// manifestBody serializes the manifest header + entries to a string.
func manifestBody(m *Manifest) string {
	var b strings.Builder
	b.WriteString("# golden.lock — DO NOT EDIT BY HAND.\n")
	b.WriteString("# Format: <sha256>  <repo-root-relative-path>\n")
	b.WriteString("# Managed by golden-lock; verify with `golden-lock verify`.\n")
	for _, e := range m.Entries {
		b.WriteString(e.Hash)
		b.WriteString("  ")
		b.WriteString(e.Path)
		b.WriteByte('\n')
	}
	return b.String()
}

// WriteManifest serializes the manifest (header comments + entries) and writes
// it atomically to m.Path via a temp file + rename. The live manifest is never
// modified in place; the new content lands by an atomic os.Rename.
//
// The caller is responsible for any required privilege. Under root this is the
// non-locking writer used by tests / unprivileged contexts; the privileged
// write path uses WriteManifestLocked (temp-file → root:0/444 → atomic rename)
// so the live manifest stays 444 the entire time (#4).
func WriteManifest(m *Manifest) error {
	return writeManifestFile(m, false)
}

// WriteManifestLocked writes the manifest atomically AND leaves the result
// root-owned 0444. It chowns/chmods the TEMP file before the rename, so the
// previous live manifest remains 444 until the atomic swap — there is never a
// window where a writable manifest is exposed (#4). Requires root.
func WriteManifestLocked(m *Manifest) error {
	return writeManifestFile(m, true)
}

func writeManifestFile(m *Manifest, lockResult bool) error {
	if m.Path == "" {
		return fmt.Errorf("manifest has no path")
	}
	if m.Root == "" {
		return fmt.Errorf("manifest has no root")
	}

	body := manifestBody(m)

	// Ensure the manifest directory exists before resolving its dir-fd. A symlink
	// planted at the golden-lock component is still caught by the O_NOFOLLOW walk
	// below.
	if err := os.MkdirAll(filepath.Join(m.Root, GoldenLockDir), 0o755); err != nil {
		return err
	}

	// Resolve the manifest's PARENT directory (<root>/golden-lock) to a dir-fd via
	// the symlink-free walk, so a swapped parent symlink cannot relocate the trust
	// anchor between create and rename (Vector C). The temp is created with Openat
	// IN that dir-fd, frozen via its own fd, then Renameat'd within the SAME dir-fd
	// — no path re-resolution happens between create and rename, so the anchor
	// lands in the verified directory inode.
	dirFD, err := openManifestParentDir(m.Root)
	if err != nil {
		return err
	}
	defer dirFD.Close()

	tmpBase, tmp, err := createTempAt(dirFD, ".golden.lock.")
	if err != nil {
		return err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = unlinkAt(dirFD, tmpBase)
		}
	}()

	if _, err := tmp.WriteString(body); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if lockResult {
		// Freeze the temp file to root:0/444 BEFORE the rename, via its own fd,
		// so the manifest that becomes live is already the trust anchor and the
		// outgoing one was never unlocked.
		if err := LockFileFD(tmp); err != nil {
			tmp.Close()
			return err
		}
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// An immutable live manifest cannot be replaced by rename, so clear its flag
	// first (privileged path only). A first-ever lock (no live manifest) or an
	// unsupported filesystem is a no-op.
	if lockResult {
		clearLiveManifestImmutable(dirFD)
	}

	// Renameat within the same dir-fd: no path string is re-resolved, so a
	// parent-directory symlink swap between create and rename has no effect.
	if err := sysRenameat(int(dirFD.Fd()), tmpBase, int(dirFD.Fd()), LockfileName); err != nil {
		return err
	}
	cleanup = false

	// Re-apply immutability to the freshly published manifest. There is a brief
	// window between rename and this call where the manifest is 0444-but-mutable;
	// it is no weaker than the manifest's permanent state before this feature, and
	// the manifest is root-owned throughout.
	if lockResult {
		if err := setLiveManifestImmutable(dirFD); err != nil {
			return err
		}
	}
	return nil
}

// clearLiveManifestImmutable best-effort clears the immutable flag on the
// current live manifest inside dirFD so the atomic rename may replace it. Absent
// manifest (ENOENT) or an unsupported filesystem is a silent no-op; a swapped
// non-regular/symlink manifest is left for the rename to surface.
func clearLiveManifestImmutable(dirFD *os.File) {
	fd, err := sysOpenat(int(dirFD.Fd()), LockfileName,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return
	}
	f := os.NewFile(uintptr(fd), LockfileName)
	_ = clearImmutable(f)
	f.Close()
}

// setLiveManifestImmutable sets the immutable flag on the freshly published live
// manifest inside dirFD. An unsupported filesystem degrades silently (the hash
// manifest still provides detection); a genuine failure is returned.
func setLiveManifestImmutable(dirFD *os.File) error {
	fd, err := sysOpenat(int(dirFD.Fd()), LockfileName,
		syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), LockfileName)
	defer f.Close()
	if _, err := applyImmutable(f); err != nil {
		return err
	}
	return nil
}

// openManifestParentDir opens the manifest's parent directory
// (<root>/golden-lock) as a dir-fd via the symlink-free walk. EvalSymlinks
// canonicalizes the repo-root prefix; the O_NOFOLLOW walk in openDirFromFSRoot
// then re-verifies every component — including golden-lock — so a symlink
// planted at the golden-lock segment is rejected. The dir must already exist
// (writeManifestFile creates it before calling this).
func openManifestParentDir(root string) (*os.File, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	canonicalRoot, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return openDirFromFSRoot(filepath.Join(canonicalRoot, GoldenLockDir))
}

// createTempAt creates a uniquely-named temp file inside the directory referred
// to by dirFD using Openat (O_CREAT|O_EXCL|O_NOFOLLOW), returning the base name
// and an *os.File. The file is created mode 0600 (it is re-chmod'd to 444 by the
// caller when locking).
func createTempAt(dirFD *os.File, prefix string) (string, *os.File, error) {
	for attempt := 0; attempt < 1000; attempt++ {
		base := prefix + randSuffix() + ".tmp"
		fd, err := sysOpenat(int(dirFD.Fd()), base,
			syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0o600)
		if err != nil {
			if err == syscall.EEXIST {
				continue
			}
			return "", nil, err
		}
		return base, os.NewFile(uintptr(fd), base), nil
	}
	return "", nil, fmt.Errorf("could not create unique temp file in manifest dir")
}

// unlinkAt removes base relative to dirFD.
func unlinkAt(dirFD *os.File, base string) error {
	return sysUnlinkat(int(dirFD.Fd()), base)
}

// randSuffix returns a short random hex string for unique temp-file names.
func randSuffix() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is exceptional; fall back to PID + nanos.
		return fmt.Sprintf("%d-%d", os.Getpid(), time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

// Find returns the index of the entry with the given normalized relPath, or -1.
func (m *Manifest) Find(relPath string) int {
	for i := range m.Entries {
		if m.Entries[i].Path == relPath {
			return i
		}
	}
	return -1
}

// Upsert inserts or updates the entry for relPath with the given hash. It
// returns true if an existing entry was updated, false if a new entry was
// appended.
func (m *Manifest) Upsert(relPath, hash string) bool {
	if i := m.Find(relPath); i >= 0 {
		m.Entries[i].Hash = hash
		return true
	}
	m.Entries = append(m.Entries, Entry{Hash: hash, Path: relPath})
	return false
}

// Remove deletes the entry for relPath. It returns true if an entry was removed,
// false if relPath was not listed.
func (m *Manifest) Remove(relPath string) bool {
	i := m.Find(relPath)
	if i < 0 {
		return false
	}
	m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
	return true
}
