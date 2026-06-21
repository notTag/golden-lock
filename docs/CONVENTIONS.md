# Conventions

The code style, naming rules, and patterns to follow when working in this repo.

## Layout

- **Flat, single package.** Every `.go` file is `package main` at the repo root —
  no internal packages, no `cmd/` tree. Add new code as another root file grouped
  by responsibility (see [ARCHITECTURE.md](ARCHITECTURE.md) for the file map).
- **Stdlib-first.** The only external dependency is `golang.org/x/sys` (for the
  `*at` / ownership / immutable-flag syscalls the stdlib doesn't export portably).
  Don't add dependencies without a syscall-shaped reason.
- **Per-GOOS via build tags + `_GOOS` suffix.** Platform-specific code splits into
  `immutable_darwin.go` / `immutable_linux.go` behind a shared `immutable.go`
  interface. Mirror that pattern for anything else that diverges by OS.

## The frozen contract

[../ARCH.md](../ARCH.md) defines a FROZEN CONTRACT: every exported signature, type,
and const. Fill bodies — never change a signature, type, or const value without a
deliberate contract edit. The exit-code integers, `LockfileName` (`golden.lock`),
and the program name are pinned by tripwire tests in `rename_contract_test.go`; a
rename that touches them is *meant* to fail loudly so it gets a migration note
instead of silently breaking already-locked repos and CI scripts.

## Exit codes are the CLI contract

Two disjoint ranges, asserted by tests:

- **verify (read):** `0` match · `1` mismatch · `2` missing · `3` lockfile
  absent/malformed.
- **lock/unlock (write):** `0` ok · `4` not root · `5` arg error · `6` I/O.

Precedence matters: absent/malformed lockfile (`3`) dominates; among entries,
missing (`2`) outranks mismatch (`1`). Changing a value is a deliberate edit, not
incidental drift.

## Security patterns (non-negotiable)

- **Never trust a path string twice; never follow a symlink.** Resolve files
  through `resolveNoSymlink` (component-walk, `O_NOFOLLOW` at every step). When you
  add a path that resolves files, route it through the resolver and add a
  symlink-rejection test alongside it.
- **Mutate through an fd, not a bare path.** chown/chmod go through
  `f.Chown`/`f.Chmod` (fchown/fchmod) on a fd opened `O_NOFOLLOW` — never a path
  call the kernel would follow.
- **Hash the same fd you froze.** Open once, freeze, hash, record — no re-open
  between, no TOCTOU window.

## Go style (matches existing code + repo CLAUDE.md)

- **Descriptive names over abbreviations.** `relPath`, not `rel`; spell out what a
  value holds. Abbreviations only when domain-standard (`fd`, `ctx`, `err`,
  loop `i`/`j`, `r`/`w` for a Reader/Writer in a short helper). Name length scales
  with scope.
- **Decompose dense lines.** One operation per line with a named intermediate —
  don't nest a call result inside a slice index inside another call. Idiomatic Go
  one-liners (`if v, ok := m[k]; ok {`) are fine; merely-shorter ones aren't.
- **Code self-documents.** Names + structure carry the meaning; strip every comment
  and it should still read. Comments earn their place explaining **why** (rationale,
  a non-obvious constraint, security reasoning) — never restating **what** the code
  already says. A comment compensating for a bad name means rename the variable.
- **`errors.Is` for sentinels.** `ErrSymlink`, `ErrManifestMalformed` are matched
  with `errors.Is`, not string compares.

## Tests

`go test` only — no build tags, no external fixtures. Every test builds its own
fake repo under `t.TempDir()` (hermetic, immune to ambient git state). Write-path
tests `t.Skip` unless `os.Geteuid() == 0`. See [TESTING.md](TESTING.md) for the
full testing convention and the golden contract guards.
