# Issue #27 — Explicit `--lockfile` global flag: PRD

**Status:** implemented on branch `feat/lockfile-flag` (worktree `../golden-lock-lockfile-flag`); not yet committed/PR'd.
**Source:** https://github.com/notTag/golden-lock/issues/27 (P4, `type:todo`)
**Closes residual:** `ARCH.md` "Known residual" (root-selection trusts path markers), `BUGS/DONE/bug-009.md`, `TODO.md` P4 line, bug-003 option (b).

---

## 1. Problem

Every command picks its root implicitly from the working directory:

| Command | Root source (main.go / setup.go) |
|---|---|
| `lock`, `setup` | `rootForNewManifest(cwd)` → `FindRepoRoot`, else cwd (loose-dir fallback) |
| `unlock`, `verify`, `list` | `FindRepoRoot(cwd)`, error if none |

`FindRepoRoot` (lockfile.go:91) = unprivileged `git rev-parse` probe, then nearest `.git`, then nearest `golden-lock/golden.lock`. All `os.Stat`, path-based. Anyone who can plant a `.git` or manifest marker between cwd and the intended root steers WHICH directory is used. Safe against symlink-follow + hash laundering (O_NOFOLLOW walk below the root), but:

- CI / hooks cannot pin "verify THIS manifest" — a planted `.git` in a subdir makes verify check a different (attacker-controlled, possibly vacuous) root.
- Operators cannot run from outside the repo (`sudo golden-lock lock` from `/`, scripts with arbitrary cwd).
- No way to resolve exotic layouts deterministically.

## 2. Goal

An explicit flag that names the manifest and **skips discovery entirely** — no git probe, no marker walk, no loose-dir fallback.

Non-goals: env var (`GOLDEN_LOCK_FILE` — sudo env passthrough makes it an attacker-influenced input; skip), config file, relocating the manifest outside `<root>/golden-lock/`, multiple manifests per run.

## 3. CLI shape

```sh
golden-lock --lockfile <path> <command> [args]
golden-lock --lockfile=<path> <command> [args]
golden-lock --lockfile <path> <file>...          # bare-path lock shorthand still works
```

- Accepted **before or after** the subcommand: `dispatch` strips every occurrence (`extractLockfileFlag`) before routing, so subcommand FlagSets never see it. Scanning stops at a bare `--` (Go `flag` semantics), so a file literally named `--lockfile` can still follow it.
- Given twice → arg error (not last-wins). Missing/empty value → arg error. Both exit 5.
- `<path>` = path to the `golden.lock` file itself, abs or cwd-relative.

### Path → root derivation

The manifest location is fixed (`LockfilePath(root) = <root>/golden-lock/golden.lock`) and entry paths are root-relative, so the flag must name a file in the canonical layout:

```
--lockfile /repo/golden-lock/golden.lock  →  root = /repo
```

Validation (all → arg error, before any FS mutation):

1. `filepath.Abs` + `Clean`.
2. `Base(path) == LockfileName` and `Base(Dir(path)) == GoldenLockDir` — else `--lockfile must point at <dir>/golden-lock/golden.lock`.
3. `root = Dir(Dir(path))` must exist and be a directory.
4. Manifest file itself need not exist (`lock` creates it; `verify`/`list` report exit 3 as today).

ponytail: canonical-layout only. Arbitrary manifest location needs `ReadManifest`/`WriteManifestLocked` to take a path instead of a root — add if a real layout demands it.

## 4. Behavior per command

| Command | With `--lockfile` |
|---|---|
| `lock` | root = derived; no loose-dir note; creates `golden-lock/` + manifest on first lock as today. File args still cwd-relative → `NormalizePath(root, …)`; outside root → existing escape error. No-arg mode reads `<root>/golden-lock/proposal-locks/`. |
| `unlock` | root = derived. |
| `verify` | root = derived; manifest absent → exit 3 (unchanged semantics). |
| `list` | root = derived. |
| `setup` | root = derived; scaffolds there. |
| `version`, `help` | flag accepted, ignored. |

Implementation: `rootForNewManifest` (lock, setup) gains an `err` return and short-circuits to `rootFromLockfile` when the flag is set; `findRoot(cwd)` does the same for unlock/verify/list in place of `FindRepoRoot`. Bad flag shape → exit 5 for write commands, 3 for verify/list (same codes as "cannot find repo root"). Flag value is a package var that `dispatch` sets and clears on return.

## 5. Security notes

- Under sudo the flag value is operator-typed argv → same trust as the file args already are. No new attacker input.
- Root dir is still opened via the existing O_NOFOLLOW dir-fd walk; symlinked components below root still rejected. Manifest read still hard-rejects a symlinked/non-regular `golden.lock` (exit 3).
- Components ABOVE the root (`/repo` itself, its parents) are resolved by the kernel as today — unchanged trust boundary; document, don't widen.
- Skipping the git probe under the flag also removes the only unprivileged exec on the verify path.

## 6. Docs / tracking updates (same PR)

- `usage()` in main.go: `GLOBAL FLAGS:` block.
- `README.md`: flag + CI example `golden-lock --lockfile golden-lock/golden.lock verify`.
- `.github/workflows/golden-verify.yml`: pin the gate with `./gl --lockfile golden-lock/golden.lock verify` (CI builds from source, so the flag is available in the same PR).
- `ARCH.md`: rewrite "Known residual" → mitigated when flag used; residual remains for implicit discovery.
- `TODO.md`: remove P4 line. Issue #27 closed by PR.

## 7. Decisions (resolved 2026-10-21)

1. **Name:** `--lockfile` only. No `--root` alias.
2. **Position:** before AND after the subcommand.
3. ~~Coordination with `feat/end-of-options-marker`~~ — dropped. That branch has no commits past `main` and no working changes, so there is nothing to conflict with.

## 8. Done when

- [x] `golden-lock --lockfile <p> verify|list|lock|unlock|setup` uses `Dir(Dir(p))` as root with no discovery.
- [x] Bad shape / missing root dir → exit 5 (write cmds) or 3 (verify/list), clear message, no FS mutation.
- [x] Test: planted `.git` as cwd + `--lockfile` at the real manifest → verify exits 0 in both flag positions (discovery alone exits 3).
- [x] Test: bare-path shorthand after the flag still routes to lock.
- [x] `go test ./...` green; usage, README, ARCH, TODO updated.
