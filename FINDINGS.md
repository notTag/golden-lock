# golden-test — Independent Evaluation (2026-06-10)

Build: `go build`/`go vet`/`go test` all clean — 37 tests pass (Go 1.26, dep: `golang.org/x/sys` only).
Prior REVIEW.md (10 findings) confirmed remediated in code. Items below are NEW or residual.

## Value

A Unix CLI that makes "golden" test files immutable so an autonomous coding agent
can't silently game or delete them. Two layers:

1. **Local immutability** — `sudo golden-test lock` chowns golden files (and the
   manifest) to `root:0` + `chmod 444`. A non-root agent on that machine cannot
   edit/delete them. Hardening is genuinely thorough: symlink-free component-walk
   resolver (`O_NOFOLLOW` at every path component incl. ancestors above the repo
   root), freeze-then-hash on a single fd (no content TOCTOU), single atomic
   manifest publish via `Renameat` in a pinned dir-fd, vetted-PATH git, abort on
   malformed manifest.
2. **CI hash gate** — `golden-test verify` recomputes SHA-256 per manifest entry,
   needs no privilege, fails the build on drift. This is the layer that survives a
   fresh clone (see F3).

Solid, well-documented, niche. ~900 LOC, clean flat architecture, frozen-contract docs.

## Findings

| # | Sev | Type | Location | Issue |
|---|-----|------|----------|-------|
| A | MED | test coverage | goldentest_test.go | The privileged core (fchown root:0, freeze-then-hash, lock/unlock round-trip) is gated behind `if os.Geteuid()!=0 { t.Skip }`. CI (`ubuntu-latest`, non-root) and local runs skip these — the actual security mechanism is **never exercised in CI**. Only exit-code 4 (not-root) and read paths are covered. Add a privileged job (`sudo go test` or a root container) so the freeze/lock path is verified. |
| F3 | MED | value / threat-model | design | `root:0/444` is a **local FS property git does not preserve**. After any clone, or on any machine where `lock` wasn't run as root, golden files + manifest are ordinary writable files. Protection then reduces entirely to the CI hash gate — and since `golden-test.lock` is itself committed, an agent that edits a golden file **and** its manifest hash in one commit makes `verify` pass green. The real CI defense is therefore a *tripwire*: tampering forces a visible `golden-test.lock` diff into the PR. Requires branch protection **plus human review of any `.lock` change** to be meaningful. Document this explicitly; `verify` alone is not tamper-proof. |
| B | LOW | correctness | lockfile.go:70-106 | Root-resolution divergence: `lock`/`unlock` (root) use the ancestor-walk (`.git` or `golden-test.lock` per dir); `verify` (non-root) prefers `git rev-parse --show-toplevel`. With a stray `golden-test.lock` in a subdir or a nested `.git`, lock-time root and verify-time root can differ → `verify` reports exit 3 (manifest absent) against a populated lock, or resolves a different root. Normal repos (manifest at git toplevel) are unaffected. |
| C | LOW | correctness | permissions.go:41-55 | `SudoUID` falls back to `os.Getuid()` when `SUDO_UID` is unset. Run from a real root shell (not via sudo), `unlock` restores ownership to `0:0`/`644` — file stays root-owned, not handed to a usable user. Minor wart. |
| D | LOW | consistency | main.go:255 | Empty-manifest cleanup uses path-based `os.Remove(m.Path)` — the one mutation that bypasses the dir-fd/symlink-free machinery the rest of the code is meticulous about. Narrow TOCTOU window vs. a parent-dir swap after the per-file resolver loop. Low risk (root already walked), but inconsistent. |
| E | INFO | cosmetic | lockfile.go:76,118 | Child git env pins `PATH=/usr/bin:/bin:/usr/local/bin` but omits `/opt/homebrew/bin`, which IS a vetted candidate. `rev-parse` spawns no helpers, so harmless today. |
| G | LOW | robustness | lockfile.go:235-245 | `ReadManifest` never validates the hash field (64 lowercase hex chars). A corrupt-but-parseable entry (truncated/uppercase hash) is accepted and surfaces later as MISMATCH (exit 1) instead of malformed (exit 3) — misclassifies corruption as tampering. Cheap fix: length+hex check at parse time. |
| H | INFO | robustness | verify.go:45-49 | `Verify` maps every `ReadManifest` error to exit 3 "absent or malformed" — including EACCES/IO errors. An unreadable-but-intact manifest reports the same as a missing one. Cosmetic; CI treats both as failure either way. |
| R | KNOWN | documented residual | ARCH.md:168-175 | Root *selection* trusts path-based `os.Stat` for `.git`/`lockfile` markers, so a planted marker can influence which dir is chosen as root. Cannot induce a symlink-follow or launder a root-owned hash. Accepted; a `--lockfile` flag would sidestep it. |

## Bottom line

No high-severity defects. The symlink/TOCTOU hardening is correct and well-tested
for the read path. Two things matter most: (A) the privileged path has zero CI
coverage, and (F3) the durable guarantee is the CI hash *tripwire* (needs human
review of `.lock` diffs), not the local `444` lock, which doesn't survive clone.
