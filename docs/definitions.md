# Definitions

Short glossary for terms used across golden-test's locking model.

**File descriptor (fd):** A small integer the kernel returns when you open a file; it is a handle to one specific *open inode*, so operations on it (read, `fchmod`, `fchflags`) act on exactly that file no matter what its path later resolves to. Using an fd instead of re-opening a path is what closes time-of-check-to-time-of-use (TOCTOU) gaps.

**inode:** The on-disk object that actually *is* a file — its content plus metadata (owner, mode, flags, timestamps), identified by a number. A filename is just a directory entry pointing at an inode, and permission to rename or delete that entry is governed by the parent directory, not the inode — which is why `chmod 444` alone does not stop replace-by-rename.

**schg (`SF_IMMUTABLE`):** The macOS/BSD "system immutable" file flag (`chflags schg`) that blocks all writes, renames, rename-over, `chmod`/`chown`, and deletion. Only root can set or clear it, and clearing is refused while the kernel runs at securelevel ≥ 1 (it then requires a single-user boot), making it root-proof on hardened hosts. golden-test uses this flag.

**uchg (`UF_IMMUTABLE`):** The macOS/BSD "user immutable" file flag (`chflags uchg`) with the same immutability effect as schg, except the file's owner (or root) may set and clear it at any time, independent of securelevel.
