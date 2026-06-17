package main

// hash.go — SHA-256 computation for a single file, plus the symlink-free path
// resolver that the whole trust model is built on.
//
// The security findings all reduce to one rule: never resolve a golden-file (or
// manifest) path twice, and never follow a symlink — at ANY path component, not
// just the leaf. O_NOFOLLOW on a bare os.OpenFile only guards the FINAL
// component, so a swapped intermediate directory could still relocate the inode
// we operate on.
//
// resolveNoSymlink closes that gap: it opens the repo root, then walks each
// intermediate component with syscall.Openat(dirfd, comp,
// O_NOFOLLOW|O_DIRECTORY|...) down to the parent dir-fd, and finally opens the
// leaf with syscall.Openat(parentfd, leaf, O_NOFOLLOW|...) (no O_DIRECTORY).
// ELOOP / a symlink at any component is rejected. Every operation that must
// apply to "the locked file" — hashing on lock, hashing on verify, the
// chown/chmod that freezes it, and the manifest temp-create+rename — is
// performed against the resulting fd / parent dir-fd. One inode, one fd, no
// re-open window, no swappable intermediate.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrSymlink is returned when the resolver refuses a path because some
// component (leaf or any intermediate directory) is a symlink (O_NOFOLLOW →
// ELOOP, or a non-directory where a directory was required).
var ErrSymlink = errors.New("refusing to operate on a symlink")

// openRootDir opens the repo root directory as a dir-fd, refusing to follow a
// symlink at ANY component of the absolute root path — not just root's final
// component. A single path-based open with O_NOFOLLOW would guard only the last
// component and let the kernel follow every ancestor above the repo root, so an
// attacker who could swap a symlink into an ancestor directory could relocate
// the entire trust anchor. Instead we canonicalize the root once (resolving
// legitimate system symlinks such as macOS /var→/private/var and /tmp, which
// otherwise sit above every t.TempDir() and real /tmp checkout) and then walk
// the canonical path from the filesystem root, opening each component with
// syscall.Openat(O_NOFOLLOW|O_DIRECTORY). This pins every ancestor inode by fd
// and rejects any symlink introduced after canonicalization.
//
// Returns a dir-fd as an *os.File the caller must Close.
func openRootDir(root string) (*os.File, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	// EvalSymlinks resolves legitimate symlinks in the prefix to their real
	// location; the subsequent O_NOFOLLOW walk then verifies (at open time) that
	// the canonical chain contains no symlink and pins each ancestor by fd.
	canon, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, err
	}
	return openDirFromFSRoot(canon)
}

// openDirFromFSRoot opens absDir (which must be absolute and symlink-canonical)
// by opening the filesystem root and walking each component with
// syscall.Openat(..., O_NOFOLLOW|O_DIRECTORY|...). A symlink or non-directory at
// any component is rejected with ErrSymlink. Returns the final dir-fd as an
// *os.File the caller must Close.
func openDirFromFSRoot(absDir string) (*os.File, error) {
	rootFD, err := syscall.Open(string(filepath.Separator),
		syscall.O_DIRECTORY|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	dir := os.NewFile(uintptr(rootFD), string(filepath.Separator))
	walked := string(filepath.Separator)
	for _, comp := range strings.Split(filepath.ToSlash(absDir), "/") {
		if comp == "" || comp == "." {
			continue
		}
		if comp == ".." {
			dir.Close()
			return nil, fmt.Errorf("root path %q contains %q", absDir, "..")
		}
		walked = filepath.Join(walked, comp)
		nfd, oerr := sysOpenat(int(dir.Fd()), comp,
			syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		dir.Close()
		if oerr != nil {
			if isELOOP(oerr) || oerr == syscall.ENOTDIR {
				return nil, fmt.Errorf("%s: %w", walked, ErrSymlink)
			}
			return nil, oerr
		}
		dir = os.NewFile(uintptr(nfd), walked)
	}
	return dir, nil
}

// splitRel splits a repo-root-relative, forward-slash, cleaned path into its
// components, rejecting empty / "." / ".." segments (defense in depth; callers
// already pass NormalizePath output).
func splitRel(rel string) ([]string, error) {
	rel = filepath.ToSlash(rel)
	parts := strings.Split(rel, "/")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." {
			continue
		}
		if p == ".." {
			return nil, fmt.Errorf("path component %q escapes repo root", rel)
		}
		out = append(out, p)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("path %q has no components", rel)
	}
	return out, nil
}

// walkToParent opens the repo root, then walks every intermediate directory
// component of rel with syscall.Openat(..., O_NOFOLLOW|O_DIRECTORY|...),
// returning the parent dir-fd (an *os.File the caller must Close) and the leaf
// component name. A symlink (or non-directory) at any intermediate component is
// rejected with ErrSymlink.
func walkToParent(root, rel string) (parent *os.File, leaf string, err error) {
	comps, err := splitRel(rel)
	if err != nil {
		return nil, "", err
	}
	dir, err := openRootDir(root)
	if err != nil {
		return nil, "", err
	}
	// Walk all but the final component as directories.
	for i := 0; i < len(comps)-1; i++ {
		comp := comps[i]
		nfd, oerr := sysOpenat(int(dir.Fd()), comp,
			syscall.O_NOFOLLOW|syscall.O_DIRECTORY|syscall.O_RDONLY|syscall.O_CLOEXEC, 0)
		dir.Close()
		if oerr != nil {
			if isELOOP(oerr) || oerr == syscall.ENOTDIR {
				return nil, "", fmt.Errorf("%s/%s: %w", root, strings.Join(comps[:i+1], "/"), ErrSymlink)
			}
			return nil, "", oerr
		}
		dir = os.NewFile(uintptr(nfd), filepath.Join(root, filepath.Join(comps[:i+1]...)))
	}
	return dir, comps[len(comps)-1], nil
}

// openLeafAt opens the leaf component relative to an already-walked parent
// dir-fd, with the given extra flags ORed onto O_NOFOLLOW|O_CLOEXEC. It rejects
// a symlinked leaf (ELOOP) and verifies the result is a regular file. name is
// used only for diagnostics / the returned *os.File's Name().
func openLeafAt(parent *os.File, leaf, name string, flags int) (*os.File, error) {
	fd, err := sysOpenat(int(parent.Fd()), leaf, flags|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		if isELOOP(err) {
			return nil, fmt.Errorf("%s: %w", name, ErrSymlink)
		}
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	fi, serr := f.Stat()
	if serr != nil {
		f.Close()
		return nil, serr
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return nil, fmt.Errorf("%s: not a regular file", name)
	}
	return f, nil
}

// resolveNoSymlink opens the file at repo-root-relative rel, refusing to follow
// a symlink at ANY component (leaf or intermediate directory). flags are extra
// open flags (e.g. os.O_RDONLY or os.O_RDWR) ORed onto O_NOFOLLOW|O_CLOEXEC.
// The returned *os.File is the single handle the caller uses for hashing and/or
// fchown/fchmod, guaranteeing every op applies to the same inode (no swappable
// intermediate, no re-open window). name is the human-facing path for errors.
func resolveNoSymlink(root, rel, name string, flags int) (*os.File, error) {
	parent, leaf, err := walkToParent(root, rel)
	if err != nil {
		return nil, err
	}
	defer parent.Close()
	return openLeafAt(parent, leaf, name, flags)
}

// hashReader computes the golden digest for the file content in r. It hashes
// the file's repo-root-relative path relPath together with the content, in this
// order: the length of relPath, then relPath, then the content:
//
//	SHA-256( uvarint(len(relPath)) || relPath || content )
//
// Hashing the path (not just the content) means the path is checked, not just
// recorded. A manifest line cannot be moved to point at a different file, and
// two golden files with identical content cannot be swapped, without the hash
// changing and verify failing.
//
// We write the length of relPath first rather than a separator byte between
// relPath and content. A separator would forbid that byte from appearing in
// the content. The length prefix has no such limit, so the content can be any
// bytes at all.
func hashReader(relPath string, r io.Reader) (string, error) {
	h := sha256.New()
	var lengthPrefixBuf [binary.MaxVarintLen64]byte
	lenRelPath := uint64(len(relPath))
	bytesWritten := binary.PutUvarint(lengthPrefixBuf[:], lenRelPath)
	lengthPrefix := lengthPrefixBuf[:bytesWritten]
	h.Write(lengthPrefix)
	io.WriteString(h, relPath)
	if _, err := io.Copy(h, r); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// hashResolved computes the lowercase hex SHA-256 of the file at repo-root-
// relative rel, refusing to follow a symlink at ANY component via the
// symlink-free resolver (Vector A). name is the human-facing path for errors.
func hashResolved(root, rel, name string) (string, error) {
	f, err := resolveNoSymlink(root, rel, name, os.O_RDONLY)
	if err != nil {
		return "", err
	}
	defer f.Close()
	return hashReader(rel, f)
}

// HashFile computes the golden digest (see hashReader) of the file at repo-root-
// relative rel under root, refusing to follow a symlink at any component.
// Because the hash covers rel, the result matches what lock records and verify
// recomputes for the same path. Returns ErrSymlink for a symlinked component, or
// another error if the file cannot be opened/read.
func HashFile(root, rel string) (string, error) {
	return hashResolved(root, rel, filepath.Join(root, filepath.FromSlash(rel)))
}
