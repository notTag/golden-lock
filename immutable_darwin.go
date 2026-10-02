//go:build darwin

package main

// immutable_darwin.go — macOS immutable-flag primitive via chflags(2).
//
// Uses SF_IMMUTABLE (the `schg` "system immutable" flag), not UF_IMMUTABLE
// (`uchg`): because lock chowns the file to root, only root can clear either
// flag anyway, so schg costs nothing extra on a normal host (securelevel 0,
// where root clears it freely) yet self-hardens to genuinely root-proof on any
// host raised to securelevel >= 1, where clearing it requires a single-user
// boot. Setting an SF_ flag requires root.
//
// Both operations read the current flag set first and toggle only the immutable
// bit, so any unrelated flags on the inode are preserved.

import "golang.org/x/sys/unix"

func immutableFD(fd int) (bool, error) {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return false, err
	}
	return st.Flags&unix.SF_IMMUTABLE != 0, nil
}

func setImmutableFD(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	currentFlags := int(st.Flags)
	withImmutable := currentFlags | unix.SF_IMMUTABLE
	return unix.Fchflags(fd, withImmutable)
}

func clearImmutableFD(fd int) error {
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil {
		return err
	}
	currentFlags := int(st.Flags)
	withoutImmutable := currentFlags &^ unix.SF_IMMUTABLE
	return unix.Fchflags(fd, withoutImmutable)
}
