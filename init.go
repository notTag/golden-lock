package main

// init.go — `init`: create an empty manifest in the current directory.
//
// FindRepoRoot anchors on the nearest `.git`, falling back to the nearest
// golden-lock/golden.lock. A directory outside version control has no `.git`, so
// until a manifest exists there is no root and `lock` cannot run. init writes
// that manifest so the current directory becomes the root.
//
// An empty manifest verifies as "0 file(s) verified, all OK", which is accurate:
// nothing is locked yet.

import (
	"fmt"
	"os"
)

// runInit implements `init`: write an empty golden-lock/golden.lock in the
// working directory. Refuses inside a git working tree, where `.git` already
// anchors the root and would shadow the new manifest (#17). Idempotent — an
// existing manifest is never overwritten. Unprivileged; under sudo the manifest
// is published root-owned 0444 like any locked write.
func runInit(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s init: cannot determine working directory: %v\n", progName(), err)
		return ExitWriteIO
	}

	if gitRoot := nearestAncestorWith(cwd, ".git"); gitRoot != "" {
		fmt.Fprintf(os.Stderr, "%s init: %s is inside the git repo at %s, which already anchors the root; init is only needed outside version control\n", progName(), cwd, gitRoot)
		return ExitWriteArgs
	}

	manifestPath := LockfilePath(cwd)
	_, statErr := os.Lstat(manifestPath)
	if statErr == nil {
		fmt.Printf("note: %s already exists; left unchanged\n", LockfileRelPath)
		return ExitWriteOK
	}
	if !os.IsNotExist(statErr) {
		fmt.Fprintf(os.Stderr, "%s init: cannot check %s: %v\n", progName(), LockfileRelPath, statErr)
		return ExitWriteIO
	}

	manifest := &Manifest{Root: cwd, Path: manifestPath}
	writeManifest := WriteManifest
	if IsRoot() {
		writeManifest = WriteManifestLocked
	}
	if err := writeManifest(manifest); err != nil {
		fmt.Fprintf(os.Stderr, "%s init: cannot write %s: %v\n", progName(), LockfileRelPath, err)
		return ExitWriteIO
	}

	p := progName()
	fmt.Printf("initialized %s\n", LockfileRelPath)
	fmt.Printf("\nNext:\n")
	fmt.Printf("  lock files:        sudo %s lock <file>...\n", p)
	fmt.Printf("  verify any time:   %s verify\n", p)
	return ExitWriteOK
}
