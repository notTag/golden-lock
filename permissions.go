package main

// permissions.go — privilege checks and the chown-root/chmod-444 lock op plus
// its writable-restore (unlock) inverse.
//
// Locking a file means: chown to root (uid 0) and chmod 0444. Unlocking means:
// chown back to the invoking (sudo) user and chmod 0644. The manifest itself is
// locked/unlocked the same way so an unprivileged agent cannot launder a hash.
//
// Security model (#1/#2/#3/#5): all chown/chmod go through a file descriptor
// opened with O_NOFOLLOW (f.Chown/f.Chmod → fchown/fchmod), never through a
// path that could be a symlink or could be swapped between resolution and the
// privileged op. LockFileFD/UnlockFileFD operate on an already-open fd so the
// caller can hash and lock the SAME inode; LockFile/UnlockFile are thin
// path-based wrappers that open with O_NOFOLLOW first.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
)

const (
	// LockedMode is the read-only permission applied to locked files + manifest.
	LockedMode = 0o444
	// UnlockedMode is the writable permission restored on unlock.
	UnlockedMode = 0o644
)

// IsRoot reports whether the current process has an effective uid of 0.
func IsRoot() bool {
	return os.Geteuid() == 0
}

// SudoUID returns the uid and gid to restore ownership to on unlock. When run
// under sudo it derives these from the SUDO_UID / SUDO_GID environment
// variables; otherwise it falls back to the current real uid/gid.
func SudoUID() (uid int, gid int) {
	uid = os.Getuid()
	gid = os.Getgid()
	if v := os.Getenv("SUDO_UID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			uid = n
		}
	}
	if v := os.Getenv("SUDO_GID"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			gid = n
		}
	}
	return uid, gid
}

// openWritableResolved opens the file at repo-root-relative rel for a privileged
// chown/chmod, refusing to follow a symlink at ANY path component (leaf or
// intermediate directory) via the symlink-free resolver. It first tries O_RDWR
// (so an unlocked 0644 file opens writable) and falls back to O_RDONLY for an
// already-0444 file (fchown/fchmod need ownership, not data write access). name
// is the human-facing path used in error messages and the *os.File's Name().
func openWritableResolved(root, rel, name string) (*os.File, error) {
	f, err := resolveNoSymlink(root, rel, name, os.O_RDWR)
	if err != nil {
		// A symlinked component is a hard reject regardless of mode.
		if errors.Is(err, ErrSymlink) {
			return nil, err
		}
		// A 0444 locked file can't be opened O_RDWR; retry O_RDONLY. The walk
		// re-verifies every intermediate component again with O_NOFOLLOW.
		f, err = resolveNoSymlink(root, rel, name, os.O_RDONLY)
		if err != nil {
			return nil, err
		}
	}
	return f, nil
}

func isELOOP(err error) bool {
	return errors.Is(err, syscall.ELOOP)
}

// LockFileFD makes an already-open file immutable via its fd: fchown root:0
// then fchmod 0444. Operating on the fd (not the path) guarantees the locked
// inode is exactly the one the caller hashed (#2/#3). Requires root.
func LockFileFD(f *os.File) error {
	if !IsRoot() {
		return fmt.Errorf("lock %s: must be root", f.Name())
	}
	if err := f.Chown(0, 0); err != nil {
		return fmt.Errorf("lock %s: chown root: %w", f.Name(), err)
	}
	if err := f.Chmod(LockedMode); err != nil {
		return fmt.Errorf("lock %s: chmod %#o: %w", f.Name(), LockedMode, err)
	}
	return nil
}

// UnlockFileFD restores an already-open file to writable via its fd: fchown to
// the sudo user then fchmod 0644. Requires root.
func UnlockFileFD(f *os.File) error {
	if !IsRoot() {
		return fmt.Errorf("unlock %s: must be root", f.Name())
	}
	uid, gid := SudoUID()
	if err := f.Chown(uid, gid); err != nil {
		return fmt.Errorf("unlock %s: chown %d:%d: %w", f.Name(), uid, gid, err)
	}
	if err := f.Chmod(UnlockedMode); err != nil {
		return fmt.Errorf("unlock %s: chmod %#o: %w", f.Name(), UnlockedMode, err)
	}
	return nil
}

// LockFile makes a single file immutable: open O_NOFOLLOW, then fchown root:0 +
// fchmod 0444 on the fd. It requires root and rejects symlinked leaves (#1).
// Returns a non-nil error on any open/chown/chmod failure (callers map this to
// the I/O exit code).
func LockFile(absPath string) error {
	if !IsRoot() {
		return fmt.Errorf("lock %s: must be root", absPath)
	}
	root, rel := filepath.Split(filepath.Clean(absPath))
	f, err := openWritableResolved(filepath.Clean(root), rel, absPath)
	if err != nil {
		return fmt.Errorf("lock %s: %w", absPath, err)
	}
	defer f.Close()
	return LockFileFD(f)
}

// UnlockFile restores a single file to writable: open O_NOFOLLOW, then fchown to
// the sudo user (see SudoUID) + fchmod 0644 on the fd. Requires root and rejects
// symlinked leaves (#1). Returns a non-nil error on any failure.
func UnlockFile(absPath string) error {
	if !IsRoot() {
		return fmt.Errorf("unlock %s: must be root", absPath)
	}
	root, rel := filepath.Split(filepath.Clean(absPath))
	f, err := openWritableResolved(filepath.Clean(root), rel, absPath)
	if err != nil {
		return fmt.Errorf("unlock %s: %w", absPath, err)
	}
	defer f.Close()
	return UnlockFileFD(f)
}
