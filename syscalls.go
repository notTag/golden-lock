package main

// syscalls.go — *at syscall wrappers backed by golang.org/x/sys/unix, which
// exports Openat / Renameat / Unlinkat with Apple-blessed, maintained constants
// on every supported target (linux/amd64, darwin/amd64, darwin/arm64). This
// replaces the previous per-OS split, including the hand-coded XNU BSD trap
// numbers on Darwin (stdlib syscall does not export the *at calls there).
//
// The rest of the code only ever calls these sysOpenat / sysRenameat /
// sysUnlinkat wrappers, never the kernel directly.

import "golang.org/x/sys/unix"

func sysOpenat(dirfd int, path string, flags int, mode uint32) (int, error) {
	return unix.Openat(dirfd, path, flags, mode)
}

func sysRenameat(olddirfd int, oldpath string, newdirfd int, newpath string) error {
	return unix.Renameat(olddirfd, oldpath, newdirfd, newpath)
}

func sysUnlinkat(dirfd int, path string) error {
	// Third arg is the flag; 0 = remove a file (AT_REMOVEDIR would remove a dir).
	return unix.Unlinkat(dirfd, path, 0)
}
