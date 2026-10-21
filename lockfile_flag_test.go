package main

// lockfile_flag_test.go — pins the global --lockfile flag (#27): it is accepted
// before or after the subcommand, it names the root outright, and a planted
// marker in the working directory can no longer steer which manifest is used.

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestExtractLockfileFlag(t *testing.T) {
	cases := map[string]struct {
		args          []string
		wantRemaining []string
		wantPath      string
	}{
		"before subcommand": {[]string{"--lockfile", "x/golden-lock/golden.lock", "verify"}, []string{"verify"}, "x/golden-lock/golden.lock"},
		"after subcommand":  {[]string{"lock", "-y", "--lockfile", "p", "a.go"}, []string{"lock", "-y", "a.go"}, "p"},
		"equals form":       {[]string{"verify", "--lockfile=p"}, []string{"verify"}, "p"},
		"absent":            {[]string{"verify"}, []string{"verify"}, ""},
		"after end marker":  {[]string{"lock", "--", "--lockfile"}, []string{"lock", "--", "--lockfile"}, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			remaining, path, err := extractLockfileFlag(tc.args)
			if err != nil {
				t.Fatalf("extractLockfileFlag(%q): %v", tc.args, err)
			}
			if !reflect.DeepEqual(remaining, tc.wantRemaining) || path != tc.wantPath {
				t.Errorf("extractLockfileFlag(%q) = (%q, %q), want (%q, %q)", tc.args, remaining, path, tc.wantRemaining, tc.wantPath)
			}
		})
	}
}

func TestExtractLockfileFlag_RejectsMalformed(t *testing.T) {
	cases := map[string][]string{
		"missing value": {"verify", "--lockfile"},
		"empty equals":  {"verify", "--lockfile="},
		"given twice":   {"--lockfile", "a", "verify", "--lockfile", "b"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := extractLockfileFlag(args); err == nil {
				t.Errorf("extractLockfileFlag(%q): want error, got nil", args)
			}
		})
	}
}

func TestRootFromLockfile_RequiresCanonicalLayout(t *testing.T) {
	root := t.TempDir()

	got, err := rootFromLockfile(LockfilePath(root))
	if err != nil {
		t.Fatalf("canonical path: %v", err)
	}
	if !sameDir(t, got, root) {
		t.Errorf("rootFromLockfile root = %q, want %q", got, root)
	}

	badPaths := []string{
		filepath.Join(root, LockfileName),
		filepath.Join(root, GoldenLockDir, "other.lock"),
		filepath.Join(root, "absent", GoldenLockDir, LockfileName),
	}
	for _, badPath := range badPaths {
		if _, err := rootFromLockfile(badPath); err == nil {
			t.Errorf("rootFromLockfile(%q): want error, got nil", badPath)
		}
	}
}

// writeVerifiableManifest records one hashed file in a manifest at root, so a
// verify against that root exits 0 while a verify against any other root does not.
func writeVerifiableManifest(t *testing.T, root string) {
	t.Helper()
	relPath := "golden/expected.json"
	writeFile(t, root, relPath, `{"answer":42}`)
	hash, err := HashFile(root, relPath)
	if err != nil {
		t.Fatalf("hash: %v", err)
	}
	manifest := &Manifest{Root: root, Path: LockfilePath(root), Entries: []Entry{{Hash: hash, Path: relPath}}}
	if err := WriteManifest(manifest); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// A decoy .git in the working directory would win discovery and point verify at
// a root with no manifest (exit 3). --lockfile skips discovery, so verify checks
// the real manifest instead — in either flag position.
func TestDispatch_LockfileFlagBypassesPlantedMarker(t *testing.T) {
	realRoot := t.TempDir()
	writeVerifiableManifest(t, realRoot)
	decoy := fakeRepo(t)
	t.Chdir(decoy)

	discovered := withSilencedStderr(t, func() int { return dispatch([]string{"verify"}) })
	if discovered != ExitVerifyLockfile {
		t.Fatalf("precondition: verify from decoy = %d, want %d (decoy root has no manifest)", discovered, ExitVerifyLockfile)
	}

	lockfilePath := LockfilePath(realRoot)
	positions := map[string][]string{
		"before subcommand": {"--lockfile", lockfilePath, "verify"},
		"after subcommand":  {"verify", "--lockfile=" + lockfilePath},
	}
	for name, args := range positions {
		t.Run(name, func(t *testing.T) {
			code := withSilencedStderr(t, func() int { return dispatch(args) })
			if code != ExitVerifyOK {
				t.Errorf("dispatch(%q) = %d, want %d", args, code, ExitVerifyOK)
			}
		})
	}
	if explicitLockfile != "" {
		t.Errorf("explicitLockfile leaked past dispatch: %q", explicitLockfile)
	}
}

func TestDispatch_LockfileFlagKeepsBarePathShorthand(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: exit 4 no longer marks the lock path as reached")
	}
	root := fakeRepo(t)
	writeFile(t, root, "golden/expected.json", `{"answer":42}`)
	t.Chdir(root)

	args := []string{"--lockfile", LockfilePath(root), "golden/expected.json"}
	code := withSilencedStderr(t, func() int { return dispatch(args) })
	if code != ExitWriteNotRoot {
		t.Errorf("dispatch(%q) = %d, want %d (lock reached, refused for lack of root)", args, code, ExitWriteNotRoot)
	}
}
