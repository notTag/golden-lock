# Efficacy and intended guarantees

Golden Lock is designed to stop an automated coding agent from silently
rewriting an invariant-bearing file when changing the implementation would be
harder. Typical examples include tests used as specifications, stable fixtures,
schemas, and critical configuration.

It is a guardrail against an expedient development process, not a security
boundary against a determined administrator or a hostile actor with root
access.

## Protection model

Golden Lock combines two layers:

1. **Local prevention.** A locked file and the manifest are owned by root, made
   read-only, and—where supported—given the operating system's immutable flag.
   The immutable flag blocks both direct writes and replace-by-rename, the
   latter being how many editors and agent tools update files.
2. **Portable detection.** `golden-lock verify` recomputes path-bound SHA-256
   digests and compares them with `golden-lock/golden.lock`. This works in CI
   without elevated privileges.

The local layer provides immediate feedback on a machine where locking has been
applied. The verification layer provides a consistent repository check across
machines and fresh clones.

## What the implementation enforces

The privileged lock path uses file-descriptor-based, symlink-resistant path
resolution. Each path component is opened without following symlinks, and the
same resolved file descriptor is used for freezing and hashing. This binds the
recorded digest to the inode that was actually locked and avoids reopening a
path between security-sensitive operations.

Digests include both the normalized repository-relative path and file content.
A manifest entry therefore cannot be reassigned to a different path merely by
moving or relabeling it. Manifest hashes are validated when read, malformed
manifests are rejected, and privileged manifest updates use verified directory
file descriptors and atomic replacement.

If the underlying filesystem cannot store an immutable flag, Golden Lock emits
a per-file warning and continues in detection-only mode. It does not silently
claim that local immutability was established.

## Limits of the guarantee

Filesystem ownership and immutable flags are not preserved by Git. A fresh
clone therefore does not inherit the local prevention layer; locking must be
applied again on each machine.

The committed manifest is not a cryptographic signature. A commit that changes
both a protected file and its recorded digest can pass `golden-lock verify`.
Consequently, the portable layer is a review tripwire: its value depends on
branch protection and human review of changes to `golden-lock/golden.lock`.
CODEOWNERS protection for the manifest is recommended.

Local prevention also does not constrain an agent already running as root, and
some container or overlay filesystems cannot persist immutable flags. In those
environments, CI verification and repository governance are the effective
controls.

## Current gaps

The main remaining verification gap is privileged CI coverage. Tests for the
root-only lock and unlock pipeline exist, but they skip when the suite runs as
a normal user. CI should include a dedicated root-capable test job so changes to
ownership, permissions, immutable flags, and freeze-before-hash ordering are
exercised automatically.

Fresh-clone reapplication is currently manual. Planned commands such as
`apply` and `install-hooks` would make it easier to re-establish local locks
consistently after checkout. Until those commands ship, teams should document
the locking step in their onboarding or environment-bootstrap process.

## Assessment

Within its stated threat model, Golden Lock provides an effective local barrier
and a useful portable tamper signal. Its strongest deployment combines all
three controls:

- local locking for immediate agent feedback;
- CI verification on every protected branch; and
- required human review of manifest changes.

None of these controls alone proves that a committed file is the approved
version. Together, they make accidental or expedient invariant changes harder
to perform silently and easier to detect during review.
