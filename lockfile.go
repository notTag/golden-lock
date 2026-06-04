package main

// lockfile.go — golden-test.lock manifest model: parse/read/write,
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

// LockfileName is the fixed manifest filename, expected at the repo root.
const LockfileName = "golden-test.lock"

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

// Manifest is the parsed in-memory model of a golden-test.lock file.
// Root is the absolute repo-root directory the relative Entry paths resolve against.
// Path is the absolute path to the manifest file on disk.
type Manifest struct {
	Root    string  // absolute repo-root directory
	Path    string  // absolute path to the golden-test.lock file
	Entries []Entry // tracked entries, in manifest order
}

// FindRepoRoot discovers the repository root starting from the given directory,
// walking upward. Discovery prefers `git rev-parse --show-toplevel`; if git is
// unavailable it falls back to the nearest ancestor containing a .git entry or
// an existing golden-test.lock. Returns the absolute repo-root path.
func FindRepoRoot(startDir string) (string, error) {
	abs, err := filepath.Abs(startDir)
	if err != nil {
		return "", err
	}

	// Prefer git's own notion of the top level — but only when we are NOT root.
	// Under root (the sudo lock/unlock paths), exec'ing a git resolved from an
	// attacker-influenced PATH would run an untrusted binary with full
	// privilege (#8). In that case skip the probe entirely and rely on the
	// .git / golden-test.lock ancestor walk below, which touches no external
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
		if _, err := os.Stat(filepath.Join(dir, LockfileName)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return "", fmt.Errorf("repo root not found from %q: no .git or %s ancestor", abs, LockfileName)
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

// LockfilePath returns the absolute path to the manifest (root + LockfileName).
func LockfilePath(root string) string {
	return filepath.Join(root, LockfileName)
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

	rel, err := filepath.Rel(rootAbs, abs)
	if err != nil {
		return "", err
	}
	rel = filepath.Clean(rel)

	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes repo root %q", input, rootAbs)
	}
	if rel == "." {
		return "", fmt.Errorf("path %q resolves to the repo root itself", input)
	}

	return filepath.ToSlash(rel), nil
}

// AbsPath resolves a repo-root-relative Entry.Path back to an absolute
// filesystem path under the manifest's Root.
func (m *Manifest) AbsPath(rel string) string {
	return filepath.Join(m.Root, filepath.FromSlash(rel))
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
	f, err := resolveNoSymlink(root, LockfileName, path, os.O_RDONLY)
	if err != nil {
		if errors.Is(err, ErrSymlink) {
			// A symlinked manifest must never be trusted. Surface as malformed so
			// verify maps it to exit 3 and the write path refuses to overwrite.
			return nil, fmt.Errorf("%s: manifest is a symlink or non-regular file: %w", path, ErrManifestMalformed)
		}
		return nil, err
	}
	defer f.Close()

	// Threat model: pre-apply / dev machines are legitimately non-root, so a
	// non-root-owned manifest is a WARNING, not a hard failure. (A symlinked or
	// non-regular manifest was already hard-rejected above.)
	if fi, serr := f.Stat(); serr == nil {
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

		m.Entries = append(m.Entries, Entry{Hash: hash, Path: rest})
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return m, nil
}

// manifestBody serializes the manifest header + entries to a string.
func manifestBody(m *Manifest) string {
	var b strings.Builder
	b.WriteString("# golden-test.lock — DO NOT EDIT BY HAND.\n")
	b.WriteString("# Format: <sha256>  <repo-root-relative-path>\n")
	b.WriteString("# Managed by golden-test; verify with `golden-test verify`.\n")
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

	// Resolve the manifest's PARENT directory (the repo root) to a dir-fd via the
	// symlink-free walk, so a swapped repo-root/parent symlink cannot relocate the
	// trust anchor between create and rename (Vector C). The temp is created with
	// Openat IN that dir-fd, frozen via its own fd, then Renameat'd within the
	// SAME dir-fd — no path re-resolution happens between create and rename, so
	// the anchor lands in the verified directory inode.
	dirFD, err := openManifestParentDir(m.Root)
	if err != nil {
		return err
	}
	defer dirFD.Close()

	tmpBase, tmp, err := createTempAt(dirFD, ".golden-test.lock.")
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

	// Renameat within the same dir-fd: no path string is re-resolved, so a
	// parent-directory symlink swap between create and rename has no effect.
	if err := sysRenameat(int(dirFD.Fd()), tmpBase, int(dirFD.Fd()), LockfileName); err != nil {
		return err
	}
	cleanup = false
	return nil
}

// openManifestParentDir opens the manifest's parent directory (the repo root)
// as a dir-fd via the symlink-free walk. The manifest lives directly at the
// repo root, so the parent IS the root; we open it with O_NOFOLLOW|O_DIRECTORY
// so a symlinked root is rejected.
func openManifestParentDir(root string) (*os.File, error) {
	return openRootDir(root)
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

// Find returns the index of the entry with the given normalized relpath, or -1.
func (m *Manifest) Find(rel string) int {
	for i := range m.Entries {
		if m.Entries[i].Path == rel {
			return i
		}
	}
	return -1
}

// Upsert inserts or updates the entry for rel with the given hash. It returns
// true if an existing entry was updated, false if a new entry was appended.
func (m *Manifest) Upsert(rel, hash string) bool {
	if i := m.Find(rel); i >= 0 {
		m.Entries[i].Hash = hash
		return true
	}
	m.Entries = append(m.Entries, Entry{Hash: hash, Path: rel})
	return false
}

// Remove deletes the entry for rel. It returns true if an entry was removed,
// false if rel was not listed.
func (m *Manifest) Remove(rel string) bool {
	i := m.Find(rel)
	if i < 0 {
		return false
	}
	m.Entries = append(m.Entries[:i], m.Entries[i+1:]...)
	return true
}
