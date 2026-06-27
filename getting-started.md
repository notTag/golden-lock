# golden-lock — getting started

golden-lock freezes chosen files so they can't be silently changed: it hashes
each file, records the hash in `golden-lock/golden.lock`, and makes the
file root-owned + read-only + immutable.

## Lock a few files directly

    sudo golden-lock lock path/to/a_test.go config/production.yaml

## Lock many files in one pass (proposal-locks)

Drop one or more list files under `golden-lock/proposal-locks/`. Each
line is a repo-root-relative path; blank lines and `#` comments are ignored:

    # golden-lock/proposal-locks/core.txt
    src/auth/tenant_isolation_test.go
    config/production.yaml

Then lock the union of every list with no arguments:

    sudo golden-lock lock

## Verify (no privilege — safe for CI)

    golden-lock verify

Exit 0 = all match, 1 = hash mismatch, 2 = missing file, 3 = no/!manifest.

## Unlock

    sudo golden-lock unlock path/to/a_test.go
