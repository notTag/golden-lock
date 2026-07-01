package main

// goldenlock_test.go — happy-path + exit-code tests for golden-lock.
//
// Privilege model under test:
//   - Read path (hashing, manifest parse, Verify) needs no privilege and is
//     exercised directly against a fake repo root built in t.TempDir().
//   - Write path (LockFile/UnlockFile chown-to-root) needs real root. Those
//     tests t.Skip when os.Geteuid() != 0. The non-root *exit code* (4) is
//     still asserted via the dispatch layer, which needs no root to reach.
//
// All tests use t.TempDir() as a self-contained fake repo root. We drop a .git
// marker so FindRepoRoot (where used) resolves to that root rather than walking
// up into the real checkout, but the core verify/manifest tests call Verify()
// and the manifest API directly with an explicit root, so they are immune to
// ambient git state entirely.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRepo creates a temp directory acting as a repo root, with a .git marker
// so any FindRepoRoot fallback anchors here. Returns the absolute root path.
func fakeRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatalf("create .git marker: %v", err)
	}
	return root
}

// writeFile writes content to a path relative to root, creating parent dirs.
func writeFile(t *testing.T, root, rel, content string) string {
	t.Helper()
	abs := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", rel, err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", rel, err)
	}
	return abs
}

// sha256hex returns the lowercase hex SHA-256 of content alone. This is NOT the
// golden digest, which also hashes the path (see goldenHash). Use it only when a
// test needs some valid 64-hex hash string, e.g. manifest parse and upsert tests
// that never recompute against a real file.
func sha256hex(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// goldenHash returns the golden digest for content stored at repo-root-relative
// relPath: SHA-256( uvarint(len(relPath)) || relPath || content ). It is computed
// here by hand, mirroring the production hashReader step for step, so it can
// cross-check that implementation.
func goldenHash(relPath, content string) string {
	h := sha256.New()
	var lengthPrefixBuf [binary.MaxVarintLen64]byte
	lenRelPath := uint64(len(relPath))
	bytesWritten := binary.PutUvarint(lengthPrefixBuf[:], lenRelPath)
	lengthPrefix := lengthPrefixBuf[:bytesWritten]
	h.Write(lengthPrefix)
	h.Write([]byte(relPath))
	h.Write([]byte(content))
	return hex.EncodeToString(h.Sum(nil))
}

// writeManifest writes a golden.lock with the given raw body at its canonical
// location (<root>/golden-lock/golden.lock), creating the golden-lock dir if
// absent.
func writeManifest(t *testing.T, root, body string) {
	t.Helper()
	path := LockfilePath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir golden-lock: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}
}

// ---------------------------------------------------------------------------
// HashFile
// ---------------------------------------------------------------------------

func TestHashFile_MatchesKnownDigest(t *testing.T) {
	root := fakeRepo(t)
	const content = "golden assertions must not change\n"
	const rel = "testdata/golden.txt"
	writeFile(t, root, rel, content)

	got, err := HashFile(root, rel)
	if err != nil {
		t.Fatalf("HashFile: %v", err)
	}
	if want := goldenHash(rel, content); got != want {
		t.Errorf("HashFile = %s, want %s", got, want)
	}
}

func TestHashFile_MissingReturnsError(t *testing.T) {
	root := fakeRepo(t)
	_, err := HashFile(root, "does-not-exist")
	if err == nil {
		t.Fatal("HashFile of missing file: want error, got nil")
	}
}

// ---------------------------------------------------------------------------
// Manifest model: parse / upsert / remove / round-trip
// ---------------------------------------------------------------------------

func TestManifest_ParseSkipsCommentsAndBlanks(t *testing.T) {
	root := fakeRepo(t)
	h := sha256hex("x")
	writeManifest(t, root, "# header comment\n\n   \n"+h+"  a/b.txt\n  # indented comment\n")

	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(m.Entries), m.Entries)
	}
	if m.Entries[0].Hash != h || m.Entries[0].Path != "a/b.txt" {
		t.Errorf("entry = %+v, want {%s a/b.txt}", m.Entries[0], h)
	}
}

func TestManifest_UpsertAndRemove(t *testing.T) {
	m := &Manifest{Root: "/r", Path: "/r/" + LockfileName}

	if updated := m.Upsert("a.txt", "hash1"); updated {
		t.Error("first Upsert reported update, want insert")
	}
	if updated := m.Upsert("a.txt", "hash2"); !updated {
		t.Error("second Upsert reported insert, want update")
	}
	if i := m.Find("a.txt"); i < 0 || m.Entries[i].Hash != "hash2" {
		t.Errorf("after upsert, hash = %v, want hash2", m.Entries)
	}
	if len(m.Entries) != 1 {
		t.Errorf("entries = %d, want 1", len(m.Entries))
	}

	if removed := m.Remove("a.txt"); !removed {
		t.Error("Remove of present entry reported absent")
	}
	if removed := m.Remove("a.txt"); removed {
		t.Error("Remove of absent entry reported removed")
	}
	if len(m.Entries) != 0 {
		t.Errorf("entries after remove = %d, want 0", len(m.Entries))
	}
}

func TestManifest_WriteReadRoundTrip(t *testing.T) {
	root := fakeRepo(t)
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	m.Upsert("dir/one.txt", sha256hex("one"))
	m.Upsert("two.txt", sha256hex("two"))

	if err := WriteManifest(m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	back, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(back.Entries) != 2 {
		t.Fatalf("round-trip entries = %d, want 2", len(back.Entries))
	}
	if back.Find("dir/one.txt") < 0 || back.Find("two.txt") < 0 {
		t.Errorf("round-trip lost entries: %+v", back.Entries)
	}
}

func TestReadManifest_AbsentReturnsError(t *testing.T) {
	root := fakeRepo(t)
	if _, err := ReadManifest(root); err == nil {
		t.Fatal("ReadManifest of absent lockfile: want error, got nil")
	}
}

func TestReadManifest_MalformedReturnsError(t *testing.T) {
	root := fakeRepo(t)
	// A non-comment, non-blank line with no space separator is malformed.
	writeManifest(t, root, "thisIsAHashWithNoPathAndNoSpace\n")
	if _, err := ReadManifest(root); err == nil {
		t.Fatal("ReadManifest of malformed lockfile: want error, got nil")
	}
}

// TestReadManifest_MalformedHashReportedAsCorruption pins #18: a line that
// parses into (hash, path) but whose hash token is not exactly 64 lowercase hex
// chars is manifest CORRUPTION (ErrManifestMalformed → verify exit 3), not a
// content mismatch (exit 1). Without parse-time validation a truncated or
// upper-cased hash would slip through and later masquerade as tampering.
func TestReadManifest_MalformedHashReportedAsCorruption(t *testing.T) {
	valid := sha256hex("ok")
	cases := map[string]string{
		"truncated":   valid[:63],
		"over-long":   valid + "a",
		"upper-cased": strings.ToUpper(valid),
		"non-hex":     strings.Repeat("g", 64),
	}
	for name, badHash := range cases {
		t.Run(name, func(t *testing.T) {
			root := fakeRepo(t)
			writeManifest(t, root, badHash+"  some/file.txt\n")
			_, err := ReadManifest(root)
			if err == nil || !errors.Is(err, ErrManifestMalformed) {
				t.Fatalf("hash %q: want ErrManifestMalformed, got %v", badHash, err)
			}
		})
	}
}

// A canonical 64-lowercase-hex hash must still parse cleanly after #18.
func TestReadManifest_ValidHashAccepted(t *testing.T) {
	root := fakeRepo(t)
	h := sha256hex("good")
	writeManifest(t, root, h+"  some/file.txt\n")
	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest of valid hash: %v", err)
	}
	if len(m.Entries) != 1 || m.Entries[0].Hash != h {
		t.Fatalf("entries = %+v, want one entry with hash %s", m.Entries, h)
	}
}

// TestReadManifest_AbsentVsMalformedSentinels pins finding #3's contract: an
// absent manifest yields an os.IsNotExist error, while a present-but-corrupt
// one yields ErrManifestMalformed. The write path keys on this distinction to
// avoid wiping a corrupt trust anchor.
func TestReadManifest_AbsentVsMalformedSentinels(t *testing.T) {
	root := fakeRepo(t)

	_, absErr := ReadManifest(root)
	if absErr == nil || !os.IsNotExist(absErr) {
		t.Fatalf("absent manifest: want os.IsNotExist error, got %v", absErr)
	}
	if errors.Is(absErr, ErrManifestMalformed) {
		t.Errorf("absent manifest must NOT report malformed: %v", absErr)
	}

	writeManifest(t, root, "garbage-no-separator\n")
	_, malErr := ReadManifest(root)
	if malErr == nil || !errors.Is(malErr, ErrManifestMalformed) {
		t.Fatalf("malformed manifest: want ErrManifestMalformed, got %v", malErr)
	}
	if os.IsNotExist(malErr) {
		t.Errorf("malformed (present) manifest must NOT report IsNotExist: %v", malErr)
	}
}

// TestReadManifest_TabSeparator pins finding #7: a TAB between hash and path is
// a valid separator, symmetric with the leading TrimLeft whitespace set.
func TestReadManifest_TabSeparator(t *testing.T) {
	root := fakeRepo(t)
	h := sha256hex("tabbed")
	writeManifest(t, root, h+"\tdir/with spaces.txt\n")

	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest with tab separator: %v", err)
	}
	if len(m.Entries) != 1 {
		t.Fatalf("entries = %d, want 1: %+v", len(m.Entries), m.Entries)
	}
	if m.Entries[0].Hash != h || m.Entries[0].Path != "dir/with spaces.txt" {
		t.Errorf("entry = %+v, want {%s dir/with spaces.txt}", m.Entries[0], h)
	}
}

// ---------------------------------------------------------------------------
// NormalizePath
// ---------------------------------------------------------------------------

func TestNormalizePath(t *testing.T) {
	root := t.TempDir()
	abs := filepath.Join(root, "a", "b.txt")

	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{"absolute under root", abs, "a/b.txt", false},
		{"escapes root", filepath.Join(root, "..", "evil"), "", true},
		{"root itself", root, "", true},
		{"empty", "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := NormalizePath(root, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("NormalizePath = %q, want %q", got, tc.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Verify — exit-code matrix (the core privilege-free contract)
// ---------------------------------------------------------------------------

// lockManifestFor hashes the given relpaths' current content and writes a valid
// manifest at root. Mirrors what `lock` records, minus the chown/chmod.
func lockManifestFor(t *testing.T, root string, rels ...string) {
	t.Helper()
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	for _, rel := range rels {
		h, err := HashFile(root, rel)
		if err != nil {
			t.Fatalf("hash %s: %v", rel, err)
		}
		m.Upsert(rel, h)
	}
	if err := WriteManifest(m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
}

func TestVerify_OK(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "golden/expected.json", `{"answer":42}`)
	lockManifestFor(t, root, "golden/expected.json")

	results, code, _ := Verify(root)
	if code != ExitVerifyOK {
		t.Fatalf("Verify code = %d, want %d (OK)", code, ExitVerifyOK)
	}
	if len(results) != 1 || results[0].Status != StatusOK {
		t.Errorf("results = %+v, want one StatusOK", results)
	}
}

func TestVerify_Mismatch(t *testing.T) {
	root := fakeRepo(t)
	rel := "golden/expected.json"
	writeFile(t, root, rel, `{"answer":42}`)
	lockManifestFor(t, root, rel)

	// Mutate the locked file's content after recording its hash.
	writeFile(t, root, rel, `{"answer":99}`)

	results, code, _ := Verify(root)
	if code != ExitVerifyMismatch {
		t.Fatalf("Verify code = %d, want %d (mismatch)", code, ExitVerifyMismatch)
	}
	if len(results) != 1 || results[0].Status != StatusMismatch {
		t.Fatalf("results = %+v, want one StatusMismatch", results)
	}
	if results[0].Actual == results[0].Expected {
		t.Errorf("actual hash should differ from expected after mutation")
	}
}

func TestVerify_Missing(t *testing.T) {
	root := fakeRepo(t)
	rel := "golden/expected.json"
	writeFile(t, root, rel, "data")
	lockManifestFor(t, root, rel)

	// Remove the file the manifest references.
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
		t.Fatalf("remove: %v", err)
	}

	results, code, _ := Verify(root)
	if code != ExitVerifyMissing {
		t.Fatalf("Verify code = %d, want %d (missing)", code, ExitVerifyMissing)
	}
	if len(results) != 1 || results[0].Status != StatusMissing {
		t.Errorf("results = %+v, want one StatusMissing", results)
	}
}

func TestVerify_AbsentLockfile(t *testing.T) {
	root := fakeRepo(t) // no manifest written
	results, code, _ := Verify(root)
	if code != ExitVerifyLockfile {
		t.Fatalf("Verify code = %d, want %d (absent lockfile)", code, ExitVerifyLockfile)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
}

func TestVerify_MalformedLockfile(t *testing.T) {
	root := fakeRepo(t)
	writeManifest(t, root, "garbage-with-no-separator\n")
	_, code, _ := Verify(root)
	if code != ExitVerifyLockfile {
		t.Fatalf("Verify code = %d, want %d (malformed lockfile)", code, ExitVerifyLockfile)
	}
}

// TestVerify_UnreadableLockfile covers #29: a manifest that is present and
// intact but unreadable (mode 000 / EACCES) must still map to exit 3, yet
// surface a permission error distinct from os.IsNotExist and ErrManifestMalformed
// so the caller can tell "I can't read the trust anchor" from "it's gone". Run as
// non-root only — root bypasses the permission bits, so we skip rather than give a
// false pass.
func TestVerify_UnreadableLockfile(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits are bypassed, EACCES is unobservable")
	}
	root := fakeRepo(t)
	writeFile(t, root, "golden/expected.json", `{"answer":42}`)
	lockManifestFor(t, root, "golden/expected.json")

	manifestPath := LockfilePath(root)
	if err := os.Chmod(manifestPath, 0o000); err != nil {
		t.Fatalf("chmod manifest 000: %v", err)
	}
	// Restore a readable mode so t.TempDir cleanup can remove the file.
	t.Cleanup(func() { _ = os.Chmod(manifestPath, 0o644) })

	results, code, manifestErr := Verify(root)
	if code != ExitVerifyLockfile {
		t.Fatalf("Verify code = %d, want %d (unreadable lockfile still exit 3)", code, ExitVerifyLockfile)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil", results)
	}
	if manifestErr == nil {
		t.Fatal("manifestErr = nil, want a permission error")
	}
	if os.IsNotExist(manifestErr) {
		t.Errorf("manifestErr satisfies os.IsNotExist; an unreadable manifest is not absent: %v", manifestErr)
	}
	if errors.Is(manifestErr, ErrManifestMalformed) {
		t.Errorf("manifestErr is ErrManifestMalformed; an unreadable manifest is not malformed: %v", manifestErr)
	}
}

// TestVerify_MissingOutranksMismatch checks the documented precedence: a
// missing file (2) dominates a mere mismatch (1) in the aggregate code.
func TestVerify_MissingOutranksMismatch(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "a.txt", "a-original")
	writeFile(t, root, "b.txt", "b-original")
	lockManifestFor(t, root, "a.txt", "b.txt")

	writeFile(t, root, "a.txt", "a-mutated") // -> mismatch
	if err := os.Remove(filepath.Join(root, "b.txt")); err != nil {
		t.Fatalf("remove b: %v", err)
	}

	_, code, _ := Verify(root)
	if code != ExitVerifyMissing {
		t.Fatalf("aggregate code = %d, want %d (missing outranks mismatch)", code, ExitVerifyMissing)
	}
}

// ---------------------------------------------------------------------------
// Lock -> Verify happy path, and unlock round-trip, via the pure logic.
// (Permission/ownership ops are exercised separately; gated on real root.)
// ---------------------------------------------------------------------------

// TestLockThenVerify_OK simulates `lock` (record hashes) then `verify` (compare)
// with no privilege required — the central guarantee of the tool.
func TestLockThenVerify_OK(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "tests/golden_output.txt", "line1\nline2\n")
	lockManifestFor(t, root, "tests/golden_output.txt")

	_, code, _ := Verify(root)
	if code != ExitVerifyOK {
		t.Fatalf("lock-then-verify code = %d, want %d", code, ExitVerifyOK)
	}
}

// TestUnlockRoundTrip_RemovesEntry models `unlock`'s manifest side-effect: the
// entry disappears, so verify no longer tracks the file (and it can be edited).
func TestUnlockRoundTrip_RemovesEntry(t *testing.T) {
	root := fakeRepo(t)
	rel := "tests/golden_output.txt"
	writeFile(t, root, rel, "original")
	lockManifestFor(t, root, rel)

	// Sanity: locked + verifies OK.
	if _, code, _ := Verify(root); code != ExitVerifyOK {
		t.Fatalf("pre-unlock verify = %d, want 0", code)
	}

	// Unlock = drop the entry and rewrite the manifest.
	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if removed := m.Remove(rel); !removed {
		t.Fatal("Remove reported entry absent")
	}
	if err := WriteManifest(m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}

	// After unlock: manifest has zero entries; editing the file no longer
	// trips verify (still OK because nothing is tracked).
	back, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest post-unlock: %v", err)
	}
	if len(back.Entries) != 0 {
		t.Fatalf("post-unlock entries = %d, want 0", len(back.Entries))
	}
	writeFile(t, root, rel, "freely edited after unlock")
	if _, code, _ := Verify(root); code != ExitVerifyOK {
		t.Errorf("post-unlock verify = %d, want 0 (untracked file is free to edit)", code)
	}
}

// ---------------------------------------------------------------------------
// Write-path exit codes via dispatch (no root needed to assert the non-root path)
// ---------------------------------------------------------------------------

func TestDispatch_LockNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: cannot exercise the not-root (exit 4) path")
	}
	code := dispatch([]string{"lock", "somefile.txt"})
	if code != ExitWriteNotRoot {
		t.Errorf("lock as non-root: code = %d, want %d", code, ExitWriteNotRoot)
	}
}

func TestDispatch_UnlockNotRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: cannot exercise the not-root (exit 4) path")
	}
	code := dispatch([]string{"unlock", "somefile.txt"})
	if code != ExitWriteNotRoot {
		t.Errorf("unlock as non-root: code = %d, want %d", code, ExitWriteNotRoot)
	}
}

// feat-005: `lock` with no args no longer means "arg error" — it sources files
// from golden-lock/proposal-locks/. The privilege gate still runs first, so a
// non-root invocation returns not-root. The "nothing listed" arg-error path is
// only reachable as root; the gathering logic itself is covered in
// proposal_locks_test.go.
func TestDispatch_LockNoArgs(t *testing.T) {
	if IsRoot() {
		t.Skip("running as root: the no-args path proceeds to proposal-locks gathering")
	}
	code := dispatch([]string{"lock"})
	if code != ExitWriteNotRoot {
		t.Errorf("lock with no args (non-root): code = %d, want %d", code, ExitWriteNotRoot)
	}
}

func TestDispatch_UnlockNoArgs(t *testing.T) {
	code := dispatch([]string{"unlock"})
	if code != ExitWriteArgs {
		t.Errorf("unlock with no args: code = %d, want %d", code, ExitWriteArgs)
	}
}

func TestDispatch_NoCommand(t *testing.T) {
	if code := dispatch(nil); code != ExitWriteArgs {
		t.Errorf("no command: code = %d, want %d", code, ExitWriteArgs)
	}
}

func TestDispatch_UnknownCommand(t *testing.T) {
	if code := dispatch([]string{"bogus"}); code != ExitWriteArgs {
		t.Errorf("unknown command: code = %d, want %d", code, ExitWriteArgs)
	}
}

func TestDispatch_Help(t *testing.T) {
	if code := dispatch([]string{"--help"}); code != ExitWriteOK {
		t.Errorf("--help: code = %d, want %d", code, ExitWriteOK)
	}
}

// TestDispatch_Version pins that both the `version` subcommand and the
// --version / -v flags exit 0 and emit a non-empty version line on stdout.
func TestDispatch_Version(t *testing.T) {
	for _, arg := range []string{"--version", "-v", "version"} {
		stdout, code := captureStdout(t, func() int {
			return dispatch([]string{arg})
		})
		if code != ExitWriteOK {
			t.Errorf("%s: code = %d, want %d", arg, code, ExitWriteOK)
		}
		if !strings.Contains(stdout, version) {
			t.Errorf("%s: stdout = %q, want it to contain version %q", arg, stdout, version)
		}
	}
}

// captureStdout redirects os.Stdout for the duration of fn and returns whatever
// fn wrote along with fn's return value.
func captureStdout(t *testing.T, fn func() int) (string, int) {
	t.Helper()

	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	originalStdout := os.Stdout
	os.Stdout = writer
	code := fn()
	os.Stdout = originalStdout

	if closeErr := writer.Close(); closeErr != nil {
		t.Fatalf("close pipe writer: %v", closeErr)
	}

	captured, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	return string(captured), code
}

// ---------------------------------------------------------------------------
// Symlink rejection (finding #1/#5) — no privilege required for the read side.
// ---------------------------------------------------------------------------

// TestHashFile_RejectsSymlink pins that HashFile refuses to follow a symlinked
// leaf (O_NOFOLLOW), so a swapped symlink-to-decoy cannot redirect hashing.
func TestHashFile_RejectsSymlink(t *testing.T) {
	root := fakeRepo(t)
	target := writeFile(t, root, "real.txt", "real content")
	link := filepath.Join(root, "golden.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	_, err := HashFile(root, "golden.txt")
	if err == nil {
		t.Fatal("HashFile of a symlink: want error, got nil")
	}
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("HashFile symlink error = %v, want ErrSymlink", err)
	}
}

// TestVerify_SymlinkLeafNotOK pins finding #5: if a locked entry's path is
// swapped for a symlink (even pointing at content with the recorded hash),
// verify must NOT report OK — O_NOFOLLOW makes it surface as Missing.
func TestVerify_SymlinkLeafNotOK(t *testing.T) {
	root := fakeRepo(t)
	rel := "golden/expected.txt"
	writeFile(t, root, rel, "decoy-and-real-share-bytes")
	lockManifestFor(t, root, rel)

	// Replace the real file with a symlink to a decoy holding identical bytes.
	abs := filepath.Join(root, filepath.FromSlash(rel))
	decoy := writeFile(t, root, "decoy.txt", "decoy-and-real-share-bytes")
	if err := os.Remove(abs); err != nil {
		t.Fatalf("remove real: %v", err)
	}
	if err := os.Symlink(decoy, abs); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	results, code, _ := Verify(root)
	if code == ExitVerifyOK {
		t.Fatalf("verify of symlinked-to-decoy entry returned OK; want non-OK")
	}
	if len(results) != 1 || results[0].Status != StatusMissing {
		t.Errorf("results = %+v, want one StatusMissing (symlink rejected)", results)
	}
}

// TestResolver_RejectsParentDirSymlink pins Vector A: the symlink-free resolver
// must refuse a path whose INTERMEDIATE directory component is a symlink, not
// just the leaf. No privilege required (pure read path via HashFile).
func TestResolver_RejectsParentDirSymlink(t *testing.T) {
	root := fakeRepo(t)

	// Real target dir holding a real file: root/realdir/secret.txt
	writeFile(t, root, "realdir/secret.txt", "attacker-controlled content")

	// Intermediate symlink: root/linkdir -> root/realdir
	linkdir := filepath.Join(root, "linkdir")
	if err := os.Symlink(filepath.Join(root, "realdir"), linkdir); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	// Resolving root/linkdir/secret.txt must be rejected at the symlinked
	// intermediate component — the leaf itself is a normal regular file.
	_, err := hashResolved(root, "linkdir/secret.txt", filepath.Join(linkdir, "secret.txt"))
	if err == nil {
		t.Fatal("hashResolved through a symlinked parent dir: want error, got nil")
	}
	if !errors.Is(err, ErrSymlink) {
		t.Errorf("parent-dir symlink error = %v, want ErrSymlink", err)
	}
}

// TestVerify_ManifestSymlinkRejected pins Vector B: if golden.lock itself
// is a symlink to an attacker file (with forged hashes), verify must NOT trust
// it. ReadManifest returns ErrManifestMalformed and Verify returns exit 3. No
// privilege required.
func TestVerify_ManifestSymlinkRejected(t *testing.T) {
	root := fakeRepo(t)

	// A real tracked file plus a forged manifest the attacker controls elsewhere.
	rel := "golden/expected.json"
	writeFile(t, root, rel, `{"answer":42}`)

	// Forged manifest in a sibling dir, claiming a (wrong) hash for the file.
	forged := filepath.Join(t.TempDir(), "forged.lock")
	if err := os.WriteFile(forged, []byte(sha256hex("totally-different")+"  "+rel+"\n"), 0o644); err != nil {
		t.Fatalf("write forged manifest: %v", err)
	}

	// Point golden.lock at the forged file via a symlink.
	if err := os.Symlink(forged, LockfilePath(root)); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	// ReadManifest must hard-reject the symlinked manifest as malformed.
	if _, err := ReadManifest(root); err == nil || !errors.Is(err, ErrManifestMalformed) {
		t.Fatalf("ReadManifest of symlinked manifest: want ErrManifestMalformed, got %v", err)
	}

	// And Verify must surface exit 3 (absent/malformed), never 0 against forged hashes.
	results, code, _ := Verify(root)
	if code != ExitVerifyLockfile {
		t.Fatalf("Verify with symlinked manifest: code = %d, want %d (lockfile)", code, ExitVerifyLockfile)
	}
	if results != nil {
		t.Errorf("results = %+v, want nil for rejected manifest", results)
	}
}

// TestRunLock_FreezesBeforeRecording pins Vectors D+E: by the time a file's hash
// lands in the manifest, the file is already 0444 (frozen). Requires real root
// (chown-to-root); t.Skip otherwise.
func TestRunLock_FreezesBeforeRecording(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("not root: lock's chown-to-root requires real root privilege")
	}
	root := fakeRepo(t)
	rel := "tests/golden.txt"
	writeFile(t, root, rel, "freeze me before you hash me")

	chdirTo(t, root)
	if code := runLock([]string{rel}); code != ExitWriteOK {
		t.Fatalf("runLock: code = %d, want %d", code, ExitWriteOK)
	}
	// lock makes the file + manifest immutable; clear that (via unlock) before
	// t.TempDir's RemoveAll runs, or cleanup cannot delete the immutable inodes.
	t.Cleanup(func() { _ = runUnlock([]string{rel}) })

	// The file must be 0444 (frozen).
	fi, err := os.Stat(filepath.Join(root, filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("stat locked file: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != LockedMode {
		t.Errorf("locked file perm = %#o, want %#o (frozen before recording)", perm, LockedMode)
	}

	// The manifest must record exactly the now-frozen content's hash, and the
	// manifest itself must be root-owned 0444 (single end-of-loop publish).
	m, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest after lock: %v", err)
	}
	i := m.Find(rel)
	if i < 0 {
		t.Fatalf("manifest missing entry for %s", rel)
	}
	if want := goldenHash(rel, "freeze me before you hash me"); m.Entries[i].Hash != want {
		t.Errorf("recorded hash = %s, want %s", m.Entries[i].Hash, want)
	}
	mfi, err := os.Stat(LockfilePath(root))
	if err != nil {
		t.Fatalf("stat manifest: %v", err)
	}
	if perm := mfi.Mode().Perm(); perm != LockedMode {
		t.Errorf("manifest perm = %#o, want %#o", perm, LockedMode)
	}
}

// TestRunLock_ImmutableBlocksRename is the regression test for the core bug:
// chmod 0444 + root ownership did NOT stop replace-by-rename (write a sibling
// temp, rename it over the target — needs only directory write permission), so
// an editor/agent could silently swap a locked golden file. After lock sets the
// filesystem immutable flag, that rename must fail. Requires real root; if the
// filesystem can't store the flag, lock degrades to detection-only and the
// rename succeeds, so we skip rather than fail.
func TestRunLock_ImmutableBlocksRename(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("not root: lock's chown-to-root + immutable flag require real root")
	}
	root := fakeRepo(t)
	rel := "tests/golden.txt"
	const original = "original golden assertion"
	abs := writeFile(t, root, rel, original)

	chdirTo(t, root)
	if code := runLock([]string{rel}); code != ExitWriteOK {
		t.Fatalf("runLock: code = %d, want %d", code, ExitWriteOK)
	}
	t.Cleanup(func() { _ = runUnlock([]string{rel}) })

	// The replace-by-rename attack: a fresh sibling file renamed over the locked
	// target. This is exactly how Edit/most editors write.
	attack := filepath.Join(root, "tests", "attack.tmp")
	if err := os.WriteFile(attack, []byte("tampered assertion"), 0o644); err != nil {
		t.Fatalf("write attack temp: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(attack) })

	if err := os.Rename(attack, abs); err == nil {
		t.Skip("filesystem does not enforce the immutable flag; rename-replace not prevented (detection-only)")
	}

	// Rename was refused — the locked content must be intact and unchanged.
	got, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read locked file after blocked rename: %v", err)
	}
	if string(got) != original {
		t.Errorf("locked golden content changed despite immutable flag: %q, want %q", got, original)
	}
}

// TestDispatch_UnlockNotListed pins finding #6: unlocking a path not present in
// the manifest is an arg error (exit 5), not a silent success. Requires root to
// pass the IsRoot gate that precedes the membership check.
func TestDispatch_UnlockNotListed(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("not root: cannot reach the membership check past the IsRoot gate")
	}
	root := fakeRepo(t)
	writeFile(t, root, "tracked.txt", "x")
	lockManifestFor(t, root, "tracked.txt")

	chdirTo(t, root)
	code := dispatch([]string{"unlock", "untracked.txt"})
	if code != ExitWriteArgs {
		t.Errorf("unlock of not-listed file: code = %d, want %d", code, ExitWriteArgs)
	}
	// And the manifest must be untouched (no entry removed, file not chmod'd).
	if m, err := ReadManifest(root); err != nil || m.Find("tracked.txt") < 0 {
		t.Errorf("manifest mutated by rejected unlock: err=%v entries=%+v", err, m)
	}
}

// TestDispatch_LockMalformedManifestAborts pins finding #3 at the dispatch
// layer: locking a new file when the manifest is present-but-malformed aborts
// with exit 6 and does NOT overwrite the corrupt manifest. Requires root.
func TestDispatch_LockMalformedManifestAborts(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("not root: lock requires root to pass the IsRoot gate")
	}
	root := fakeRepo(t)
	writeFile(t, root, "new.txt", "data")
	const corrupt = "this-line-has-no-separator\n"
	writeManifest(t, root, corrupt)

	chdirTo(t, root)
	code := dispatch([]string{"lock", "new.txt"})
	if code != ExitWriteIO {
		t.Errorf("lock against malformed manifest: code = %d, want %d", code, ExitWriteIO)
	}
	data, err := os.ReadFile(LockfilePath(root))
	if err != nil {
		t.Fatalf("read manifest after abort: %v", err)
	}
	if string(data) != corrupt {
		t.Errorf("malformed manifest was overwritten; want it preserved verbatim")
	}
}

// chdirTo changes into dir for the duration of the test, restoring afterward.
func chdirTo(t *testing.T, dir string) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	t.Cleanup(func() { _ = os.Chdir(prev) })
}

// ---------------------------------------------------------------------------
// Permission ops — require real root. Skipped otherwise.
// ---------------------------------------------------------------------------

func TestLockFile_RequiresRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: this test asserts the non-root rejection")
	}
	root := t.TempDir()
	abs := filepath.Join(root, "f.txt")
	if err := os.WriteFile(abs, []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LockFile(abs); err == nil {
		t.Error("LockFile as non-root: want error, got nil")
	}
}

func TestLockUnlockFile_AsRoot(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("not root: chown-to-root lock/unlock requires real root privilege")
	}
	root := t.TempDir()
	abs := filepath.Join(root, "f.txt")
	if err := os.WriteFile(abs, []byte("golden"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := LockFile(abs); err != nil {
		t.Fatalf("LockFile: %v", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		t.Fatalf("stat after lock: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != LockedMode {
		t.Errorf("locked perm = %#o, want %#o", perm, LockedMode)
	}

	if err := UnlockFile(abs); err != nil {
		t.Fatalf("UnlockFile: %v", err)
	}
	fi, err = os.Stat(abs)
	if err != nil {
		t.Fatalf("stat after unlock: %v", err)
	}
	if perm := fi.Mode().Perm(); perm != UnlockedMode {
		t.Errorf("unlocked perm = %#o, want %#o", perm, UnlockedMode)
	}
}

// Guard: confirm the lockfile name constant is what the manifest writer emits,
// so a rename can't silently desync tests from production.
func TestManifestHeader_IsNotParsedAsEntry(t *testing.T) {
	root := fakeRepo(t)
	m := &Manifest{Root: root, Path: LockfilePath(root)}
	m.Upsert("only.txt", sha256hex("only"))
	if err := WriteManifest(m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	data, err := os.ReadFile(LockfilePath(root))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if !strings.Contains(string(data), "# golden.lock") {
		t.Error("manifest missing header comment")
	}
	back, err := ReadManifest(root)
	if err != nil {
		t.Fatalf("ReadManifest: %v", err)
	}
	if len(back.Entries) != 1 {
		t.Errorf("header leaked into entries: %+v", back.Entries)
	}
}

// ---------------------------------------------------------------------------
// Repo-root-prefix hardening — the component walk now starts at the filesystem
// root, so a symlink anywhere ABOVE the repo root is caught too (not just below
// it). No privilege required (pure read path).
// ---------------------------------------------------------------------------

// TestOpenDirFromFSRoot_RejectsSymlinkComponent pins the prefix hardening:
// openDirFromFSRoot walks the absolute path from the filesystem root with
// O_NOFOLLOW at every component, so a symlinked directory anywhere in the chain
// — not just below the repo root — is refused with ErrSymlink.
func TestOpenDirFromFSRoot_RejectsSymlinkComponent(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval base: %v", err)
	}
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatalf("mkdir real: %v", err)
	}
	slink := filepath.Join(base, "slink")
	if err := os.Symlink(real, slink); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	// The real, symlink-free chain opens fine.
	d, err := openDirFromFSRoot(real)
	if err != nil {
		t.Fatalf("openDirFromFSRoot(real): unexpected error %v", err)
	}
	d.Close()

	// The same directory reached via a symlinked component is refused.
	if _, err := openDirFromFSRoot(slink); !errors.Is(err, ErrSymlink) {
		t.Errorf("openDirFromFSRoot(symlinked component) error = %v, want ErrSymlink", err)
	}
}

// TestOpenRootDir_AcceptsSymlinkedAncestor pins that the prefix hardening does
// NOT false-positive on a legitimate symlinked ancestor (e.g. macOS /var→/private
// /var or /tmp, under which every t.TempDir() and real /tmp checkout lives):
// openRootDir canonicalizes with EvalSymlinks before the strict from-/ walk, so
// a repo reached through a symlinked ancestor still resolves correctly.
func TestOpenRootDir_AcceptsSymlinkedAncestor(t *testing.T) {
	base := t.TempDir()
	realRoot := filepath.Join(base, "realroot")
	if err := os.MkdirAll(filepath.Join(realRoot, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir realroot/.git: %v", err)
	}
	const body = "golden via a symlinked ancestor"
	if err := os.WriteFile(filepath.Join(realRoot, "g_test.go"), []byte(body), 0o644); err != nil {
		t.Fatalf("write golden: %v", err)
	}
	linkRoot := filepath.Join(base, "linkroot")
	if err := os.Symlink(realRoot, linkRoot); err != nil {
		t.Skipf("symlink unsupported here: %v", err)
	}

	// Resolve the golden file using the repo root expressed THROUGH the symlink.
	got, err := hashResolved(linkRoot, "g_test.go", filepath.Join(linkRoot, "g_test.go"))
	if err != nil {
		t.Fatalf("hashResolved via symlinked ancestor: unexpected error %v", err)
	}
	if want := goldenHash("g_test.go", body); got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
}
