# TODO — golden-test

- [ ] [P2] Lock the first golden test(s): run the `/golden-test` skill to scan the repo, extract core tests to `*.golden.*` siblings, and lock them with `sudo golden-test lock` (creates `golden-test.lock`).
- [ ] [P2] After first lock, make the `golden-verify` GitHub check a **required status check** via branch protection / ruleset on `main` (blocks tamper merges; doable with `gh api`). Depends on the lock above.
- [ ] [P4] Decide repo visibility — `notTag/golden-test` is currently **private**; flip to public with `gh repo edit notTag/golden-test --visibility public` if desired.
- [ ] [P4] Future enhancement: add an explicit `--lockfile` global flag to sidestep `FindRepoRoot` path-based root selection (documented residual in `ARCH.md`).
- [ ] [P3] Restructure directory tree — root is too flat (~10 loose `.md` docs + 10 `.go` files). Full reorg: consolidate docs into `docs/` subfolders (keep `README.md`/`CLAUDE.md` in root), then evaluate splitting Go source into subpackages. ⚠️ `golden-test` is a single `package main` — moving `.go` files changes package boundaries and can break the build; do that part carefully (`internal/` subpackages, fix imports, `go build ./...` + `go test ./...` after each move).
