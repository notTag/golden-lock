package main

// setup.go — `setup`: scaffold the proposal-locks workflow in a repo.
//
// `lock <file>...` already creates golden-lock/ on demand (the manifest is born
// there on first lock), so the only thing missing for a brand-new repo is the
// proposal-locks/ directory you populate BEFORE running the no-arg `lock`. setup
// creates that directory plus a getting-started guide. It needs no privilege: it
// only creates operator-owned directories under the repo. The manifest itself is
// still written root-owned by the first `lock`.
//
// setup deliberately does NOT create an empty golden.lock. An empty manifest
// makes `verify` return exit 0 ("0 files verified, all OK") while protecting
// nothing — the same vacuously-OK state `unlock` removes the manifest to avoid.
// The lockfile is a first-lock artifact, not a setup one.

import (
	"fmt"
	"os"
	"path/filepath"
)

// gettingStartedGuide is dropped at golden-lock/getting-started.md by setup
// (only when absent, so a hand-edited copy is never clobbered).
const gettingStartedGuide = `# golden-lock — getting started

golden-lock freezes chosen files so they can't be silently changed: it hashes
each file, records the hash in ` + "`golden-lock/golden.lock`" + `, and makes the
file root-owned + read-only + immutable.

## Lock a few files directly

    sudo golden-lock lock path/to/a_test.go config/production.yaml

## Lock many files in one pass (proposal-locks)

Drop one or more list files under ` + "`golden-lock/proposal-locks/`" + `. Each
line is a repo-root-relative path; blank lines and ` + "`#`" + ` comments are ignored:

    # golden-lock/proposal-locks/core.txt
    src/auth/tenant_isolation_test.go
    config/production.yaml

Then lock the union of every list with no arguments:

    sudo golden-lock lock

## Verify (no privilege — safe for CI)

    golden-lock verify

Exit 0 = all match, 1 = hash mismatch, 2 = missing file, 3 = no/!manifest.

## Unlock

    sudo golden-lock unlock path/to/a_test.go
`

// runSetup implements `setup`: create golden-lock/proposal-locks/ and drop a
// getting-started guide so the proposal-locks workflow has somewhere to start.
// Unprivileged. Idempotent — safe to re-run; an existing guide is left as-is.
func runSetup(args []string) int {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s setup: cannot determine working directory: %v\n", progName(), err)
		return ExitWriteIO
	}
	root, err := FindRepoRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s setup: cannot find repo root: %v\n", progName(), err)
		return ExitWriteArgs
	}

	proposalRel := filepath.Join(GoldenLockDir, ProposalLocksDir)
	proposalDir := filepath.Join(root, proposalRel)
	if err := os.MkdirAll(proposalDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "%s setup: cannot create %s/: %v\n", progName(), proposalRel, err)
		return ExitWriteIO
	}

	guideRel := filepath.Join(GoldenLockDir, "getting-started.md")
	guidePath := filepath.Join(root, guideRel)
	wroteGuide := false
	if _, statErr := os.Stat(guidePath); os.IsNotExist(statErr) {
		if err := os.WriteFile(guidePath, []byte(gettingStartedGuide), 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "%s setup: cannot write %s: %v\n", progName(), guideRel, err)
			return ExitWriteIO
		}
		wroteGuide = true
	}

	p := progName()
	fmt.Printf("%s setup: ready\n", p)
	fmt.Printf("  %s/\n", proposalRel)
	if wroteGuide {
		fmt.Printf("  %s\n", guideRel)
	} else {
		fmt.Printf("  %s (kept existing)\n", guideRel)
	}
	fmt.Printf("\nNext:\n")
	fmt.Printf("  1. list the files to freeze in %s/<name>.txt (one repo path per line)\n", proposalRel)
	fmt.Printf("  2. lock them all:        sudo %s lock\n", p)
	fmt.Printf("  3. verify any time:      %s verify\n", p)
	fmt.Printf("Or lock files directly:    sudo %s lock <file>...\n", p)
	return ExitWriteOK
}
