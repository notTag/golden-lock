# Setup & Run

How to set up the environment, build, and run this repo — prerequisites, install
commands, env vars, and the commands to start it locally.

## Prerequisites

- **Go 1.26** (see `go.mod`).
- **Linux, macOS, or WSL2.** Windows native is out of scope — different ownership
  model. Targets: `linux/amd64`, `darwin/amd64`, `darwin/arm64`.
- **root** (via `sudo`) only for the *write* path (`lock`/`unlock`). The *read*
  path (`verify`, hashing, manifest parse) needs no privilege.

The single external dependency is `golang.org/x/sys` (the `*at` / ownership /
immutable-flag syscalls). Everything else is stdlib.

## Build

```sh
go build -o golden-lock .
```

Produces one static binary. Put it on PATH:

```sh
sudo mv golden-lock /usr/local/bin/
```

The binary also answers to the alias name `gl` — copy or symlink it as `gl` if you
want the short form. Alias affects only help text (`progName()` from argv[0]
basename), not routing.

## Run

```sh
golden-lock <command> [arguments]
```

| Command | Effect | Privilege |
|---|---|---|
| `lock <file>...` | Hash each file, record in `golden.lock`, then root-own + `chmod 444` + set the immutable flag on the file(s) **and the manifest**. | **sudo** |
| `unlock <file>...` | Remove file(s) from the manifest, restore writable ownership/perms. The sanctioned change path. | **sudo** |
| `verify` | Recompute SHA-256 of every manifest entry and compare. CI-safe. | none |

```sh
# One-time locking ceremony (writes require root)
sudo golden-lock lock src/auth/tenant_isolation_test.go config/production.yaml

# Steady state — read-only, runs anywhere including CI
golden-lock verify

# Sanctioned change: an invariant genuinely moved
sudo golden-lock unlock config/production.yaml
# ...edit under reviewed PR...
sudo golden-lock lock config/production.yaml
```

Commands work from any subdirectory — the repo root is discovered (git toplevel
when unprivileged, else nearest `.git`/`golden.lock` ancestor).

## Env vars

- `SUDO_UID` / `SUDO_GID` — read by `unlock` to restore the original (non-root)
  owner. `sudo` sets these automatically; fall back to the real uid/gid if absent.

## Dev loop

```sh
go test ./...                 # unit + integration (write-path tests skip without root)
sudo go test ./...            # exercise the privileged write path end to end
go build -o golden-lock .
go vet ./...
```

See [TESTING.md](TESTING.md) for the root-only split and exit-code contract,
[../README.md](../README.md) for the full usage surface, and [../ARCH.md](../ARCH.md)
for design notes.
