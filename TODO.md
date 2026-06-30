# TODO — golden-lock

- [ ] [P2] Rename GitHub repo + local dir `golden-test` → `golden-lock` (deferred from feat-003). Run `gh repo rename golden-lock`, update the local checkout path + git remote URL, update PR base branches, then sweep the remaining `notTag/golden-test` references in docs/TODO. Do this as a deliberate step — it breaks paths, remotes, and any open-PR bases mid-flight.
- [ ] [P2] Lock the first golden test(s): run the `/golden-test` skill to scan the repo, extract core tests to `*.golden.*` siblings, and lock them with `sudo golden-lock lock` (creates `golden.lock`).
- [ ] [P2] After first lock, make the `golden-verify` GitHub check a **required status check** via branch protection / ruleset on `main` (blocks tamper merges; doable with `gh api`). Depends on the lock above.
- [ ] [P4] Decide repo visibility — `notTag/golden-test` is currently **private**; flip to public with `gh repo edit notTag/golden-test --visibility public` if desired.
- [ ] [P4] Future enhancement: add an explicit `--lockfile` global flag to sidestep `FindRepoRoot` path-based root selection (documented residual in `ARCH.md`).
- [ ] [P3] Add golden-lock version command/flag (`golden-lock version` / `--version`) (#31)
- [ ] [P3] Add golden-lock help command/flag (`golden-lock help` / `--help`) (#32)
- [ ] [P3] `golden-lock unlock` without any params should unlock all files listed under the proposal-lock dir — essentially the opposite of what `golden-lock lock` does. (#34)
- [ ] [P3] Restructure directory tree — root is too flat (~10 loose `.md` docs + 10 `.go` files). Full reorg: consolidate docs into `docs/` subfolders (keep `README.md`/`CLAUDE.md` in root), then evaluate splitting Go source into subpackages. ⚠️ `golden-lock` is a single `package main` — moving `.go` files changes package boundaries and can break the build; do that part carefully (`internal/` subpackages, fix imports, `go build ./...` + `go test ./...` after each move).
