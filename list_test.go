package main

import (
	"strings"
	"testing"
)

// list prints every manifest entry and nothing else — a locked file that no
// longer matches its hash still appears, since list never hashes anything.
func TestDispatch_ListPrintsManifestEntries(t *testing.T) {
	root := fakeRepo(t)
	writeFile(t, root, "golden/expected.json", `{"answer":42}`)
	writeFile(t, root, "golden/other.json", `{"answer":7}`)
	writeFile(t, root, "notlocked.txt", "ignore me")
	lockManifestFor(t, root, "golden/expected.json", "golden/other.json")
	writeFile(t, root, "golden/other.json", "tampered after locking")

	t.Chdir(root)
	output, code := captureStdout(t, func() int { return dispatch([]string{"list"}) })

	if code != ExitVerifyOK {
		t.Fatalf("list code = %d, want %d", code, ExitVerifyOK)
	}
	for _, want := range []string{"golden/expected.json", "golden/other.json", "2 file(s) locked"} {
		if !strings.Contains(output, want) {
			t.Errorf("list output missing %q; got:\n%s", want, output)
		}
	}
	if strings.Contains(output, "notlocked.txt") {
		t.Errorf("list printed an unlocked file; got:\n%s", output)
	}
}

func TestDispatch_ListNoManifest(t *testing.T) {
	root := fakeRepo(t)
	t.Chdir(root)

	if code := dispatch([]string{"list"}); code != ExitVerifyLockfile {
		t.Errorf("list with no manifest: code = %d, want %d", code, ExitVerifyLockfile)
	}
}
