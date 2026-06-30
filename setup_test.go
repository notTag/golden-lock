package main

import (
	"os"
	"path/filepath"
	"testing"
)

// setup must scaffold golden-lock/proposal-locks/ and a getting-started guide,
// but must NOT create golden.lock — an empty manifest would make verify pass
// vacuously. It must also be idempotent and never clobber a hand-edited guide.
func TestSetup(t *testing.T) {
	root := fakeRepo(t)
	chdirTo(t, root)

	if code := runSetup(nil); code != ExitWriteOK {
		t.Fatalf("setup exit = %d, want %d", code, ExitWriteOK)
	}

	proposalDir := filepath.Join(root, GoldenLockDir, ProposalLocksDir)
	if info, err := os.Stat(proposalDir); err != nil || !info.IsDir() {
		t.Fatalf("proposal-locks dir not created: %v", err)
	}

	guidePath := filepath.Join(root, GoldenLockDir, "getting-started.md")
	if _, err := os.Stat(guidePath); err != nil {
		t.Fatalf("getting-started.md not created: %v", err)
	}

	// The manifest is a first-lock artifact, never a setup one.
	if _, err := os.Stat(filepath.Join(root, GoldenLockDir, LockfileName)); !os.IsNotExist(err) {
		t.Fatalf("setup must not create %s (got err=%v)", LockfileName, err)
	}

	// Idempotent: re-running succeeds and preserves a hand-edited guide.
	const edited = "my notes\n"
	if err := os.WriteFile(guidePath, []byte(edited), 0o644); err != nil {
		t.Fatalf("overwrite guide: %v", err)
	}
	if code := runSetup(nil); code != ExitWriteOK {
		t.Fatalf("second setup exit = %d, want %d", code, ExitWriteOK)
	}
	got, err := os.ReadFile(guidePath)
	if err != nil {
		t.Fatalf("read guide: %v", err)
	}
	if string(got) != edited {
		t.Fatalf("setup clobbered an existing guide")
	}
}
