package main

// verify.go — verification logic mapping results to the granular read exit codes.
//
// Read exit codes (verify):
//
//	0 — all hashes match
//	1 — at least one hash mismatch
//	2 — at least one listed file is missing
//	3 — golden.lock absent or malformed
//
// Precedence when multiple conditions occur: malformed/absent lockfile (3)
// dominates (it is detected before per-entry checks); among per-entry results,
// a missing file (2) outranks a mismatch (1).

// Verify exit codes.
const (
	ExitVerifyOK       = 0
	ExitVerifyMismatch = 1
	ExitVerifyMissing  = 2
	ExitVerifyLockfile = 3
)

// VerifyStatus classifies a single entry's verification outcome.
type VerifyStatus int

const (
	StatusOK       VerifyStatus = iota // recomputed hash matches stored hash
	StatusMismatch                     // file present but hash differs
	StatusMissing                      // file does not exist / unreadable
)

// VerifyResult is the per-entry verification outcome.
type VerifyResult struct {
	Path     string       // repo-root-relative path from the manifest
	Status   VerifyStatus // OK / Mismatch / Missing
	Expected string       // stored hash from the manifest
	Actual   string       // recomputed hash ("" when missing)
}

// Verify reads the manifest at root, recomputes the SHA-256 of every entry,
// and returns the per-entry results together with the aggregate exit code per
// the precedence rules above. When the lockfile is absent or malformed it
// returns nil results and ExitVerifyLockfile.
func Verify(root string) (results []VerifyResult, exitCode int) {
	m, err := ReadManifest(root)
	if err != nil {
		// Absent or malformed lockfile dominates: nil results, exit 3.
		return nil, ExitVerifyLockfile
	}

	exitCode = ExitVerifyOK
	for _, e := range m.Entries {
		abs := m.AbsPath(e.Path)
		// Open via the symlink-free resolver: a symlink at the leaf OR any
		// intermediate directory is rejected (ErrSymlink) and reported as Missing
		// rather than silently verifying OK against a decoy (Vector A / #5).
		actual, hashErr := hashResolved(m.Root, e.Path, abs)
		res := VerifyResult{Path: e.Path, Expected: e.Hash}
		switch {
		case hashErr != nil:
			res.Status = StatusMissing
			res.Actual = ""
			if exitCode < ExitVerifyMissing {
				exitCode = ExitVerifyMissing
			}
		case actual != e.Hash:
			res.Status = StatusMismatch
			res.Actual = actual
			if exitCode < ExitVerifyMismatch {
				exitCode = ExitVerifyMismatch
			}
		default:
			res.Status = StatusOK
			res.Actual = actual
		}
		results = append(results, res)
	}
	return results, exitCode
}
