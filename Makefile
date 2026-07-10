# Makefile — build golden-lock and install it for local use.
#
# `make build`     compile the binary into the repo root.
# `make install`   build, then symlink the binary onto PATH and this repo's
#                  Claude skills into ~/.claude/skills/. Re-runnable.
# `make uninstall` remove the symlinks install created (leaves the binary).
#
# BINDIR is where the binary is symlinked (must be on PATH). /usr/local/bin
# needs sudo; override for a user-writable dir, e.g. `make install BINDIR=~/bin`.

BINDIR     ?= /usr/local/bin
SKILLS_DIR ?= $(HOME)/.claude/skills
# Build into bin/ — the bare name "golden-lock" collides with the golden-lock/
# data directory, which would make `go build -o golden-lock` drop the binary
# inside that dir. bin/golden-lock is already covered by .gitignore.
BINARY     := bin/golden-lock

.PHONY: build install uninstall

build:
	go build -o $(BINARY) .

install: build
	mkdir -p $(BINDIR)
	ln -sfn $(CURDIR)/$(BINARY) $(BINDIR)/golden-lock
	mkdir -p $(SKILLS_DIR)
	for skill in $(CURDIR)/.claude/skills/*/; do \
		ln -sfn "$$skill" "$(SKILLS_DIR)/$$(basename "$$skill")"; \
	done
	@echo "installed: $(BINDIR)/golden-lock + skills -> $(SKILLS_DIR)/"

uninstall:
	rm -f $(BINDIR)/golden-lock
	for skill in $(CURDIR)/.claude/skills/*/; do \
		rm -f "$(SKILLS_DIR)/$$(basename "$$skill")"; \
	done
	@echo "uninstalled symlinks (binary $(CURDIR)/$(BINARY) kept)"
