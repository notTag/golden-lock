package main

// immutable.go — platform-neutral layer over the per-GOOS immutable-flag
// primitives (setImmutableFD / clearImmutableFD, defined in immutable_<os>.go).
//
// Why this exists: chmod 0444 + root ownership stop an in-place write of the
// existing inode, but NOT replace-by-rename. An editor (and Claude's Edit tool)
// writes a sibling temp file and renames it over the target; rename needs write
// permission on the parent DIRECTORY, not on the file, so the root-owned 0444
// inode is simply unlinked and replaced. The filesystem immutable flag closes
// that hole: an immutable inode cannot be written, chmod'd, chown'd, renamed,
// renamed-over, or unlinked until the flag is cleared (which needs root, and on
// a host at securelevel >= 1 a single-user boot).
//
// Immutability is best-effort PREVENTION. The SHA-256 manifest remains the
// universal, platform-independent DETECTION layer: on a filesystem that cannot
// store the flag (overlayfs in many containers, assorted network mounts) lock
// degrades to detection-only and says so, rather than failing.

import (
	"errors"
	"os"
	"syscall"
)

// immutableUnsupported reports whether err means the filesystem cannot store the
// immutable flag, in which case the caller degrades to detection-only instead of
// treating it as a hard failure.
func immutableUnsupported(err error) bool {
	return errors.Is(err, syscall.ENOTSUP) ||
		errors.Is(err, syscall.EOPNOTSUPP) ||
		errors.Is(err, syscall.ENOTTY)
}

// applyImmutable sets the immutable flag on f. It returns (true, nil) when the
// flag was applied, (false, nil) when the filesystem does not support it (caller
// falls back to detection-only and should report that), or (false, err) on a
// genuine failure. Setting the system-immutable flag requires root.
func applyImmutable(f *os.File) (applied bool, err error) {
	if err := setImmutableFD(int(f.Fd())); err != nil {
		if immutableUnsupported(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// clearImmutable removes the immutable flag from f so the file can again be
// chown'd, chmod'd, renamed, or unlinked. A filesystem that never supported the
// flag — or a file that was not immutable — is treated as already-clear, so this
// is safe to call unconditionally before a privileged mutation. Requires root.
func clearImmutable(f *os.File) error {
	if err := clearImmutableFD(int(f.Fd())); err != nil {
		if immutableUnsupported(err) {
			return nil
		}
		return err
	}
	return nil
}
