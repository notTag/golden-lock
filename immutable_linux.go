//go:build linux

package main

// immutable_linux.go — Linux immutable-flag primitive via the FS_IOC_*FLAGS
// ioctls (the same mechanism as `chattr +i`). Setting FS_IMMUTABLE_FL requires
// the CAP_LINUX_IMMUTABLE capability (held by root); even root must clear the
// flag before the inode can be modified.
//
// FS_IOC_SETFLAGS takes the COMPLETE flag word, so both operations read the
// current flags first and toggle only the immutable bit — preserving unrelated
// inode flags (extents, etc.) that a blind write would clobber.

import "golang.org/x/sys/unix"

// fsImmutableFlag is FS_IMMUTABLE_FL from the Linux uapi (<linux/fs.h>). x/sys
// exports the FS_IOC_*FLAGS ioctls but not this flag bit, so it is defined here
// from the canonical kernel value.
const fsImmutableFlag = 0x00000010

func setImmutableFD(fd int) error {
	currentFlags, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return err
	}
	withImmutable := currentFlags | fsImmutableFlag
	return unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, withImmutable)
}

func clearImmutableFD(fd int) error {
	currentFlags, err := unix.IoctlGetInt(fd, unix.FS_IOC_GETFLAGS)
	if err != nil {
		return err
	}
	withoutImmutable := currentFlags &^ fsImmutableFlag
	return unix.IoctlSetPointerInt(fd, unix.FS_IOC_SETFLAGS, withoutImmutable)
}
