# [002] Skill: scan project for core functionality and propose lock candidates

- Status: Done
- Created: 2026-06-20
- Done: 2026-07-10
- Priority: med
- Bump: minor

## What
A skill that scans the current project directory, determines which code/tests
constitute core functionality, and proposes a ranked list of candidate items that
should be locked (made immutable golden artifacts). Output is a proposal the user
confirms — the skill recommends, it does not auto-lock.

## Why
Before locking anything with `golden-test`, someone has to decide *what* is core
enough to protect (security invariants, auth/isolation, money/idempotency, data
integrity, public-API contracts). Today that judgment is manual. A scanning skill
surfaces the candidates automatically, lowering the effort and reducing the chance a
critical test is left unlocked.

## Done When
- [x] Skill scans the cwd project tree (source + tests) and classifies items by whether they guard core functionality.
- [x] Emits a ranked table of lock candidates with a rationale per item (why it's core).
- [x] Distinguishes "already covered by a test" vs "core logic with no guarding test".
- [x] Recommends which candidates warrant extraction into their own `*.golden.*` sibling before locking.
- [x] Stops at a proposal — requires explicit user confirmation before any lock action.

## Notes
- Overlaps with the existing `golden-test` skill, which already classifies *tests* and
  marks immutable goldens. Decide whether this is a new skill or an enhancement to
  `golden-test`'s scan phase — likely the latter, generalized from tests to all core code.
- Candidate heuristics: security invariants, auth/isolation boundaries, money/idempotency,
  data integrity, public-API contracts (mirror the categories `golden-test` already uses).

### Decision (2026-06-20)
- Shipped as a **new separate skill**, `.claude/skills/golden-scan/`, not an enhancement
  to `golden-test`. Clean separation: `golden-scan` = read-only proposal over source+tests
  (decide WHAT is core); `golden-test` = extract + lock the chosen set (unchanged).
- `golden-scan` is non-destructive by construction — no write/move/chmod/chown/sudo. It
  ranks candidates by risk, tags each Covered / Uncovered / Is-test, recommends `*.golden.*`
  extraction, then stops and hands off to `/golden-test`.
- Verified against `.claude/skills/golden-scan/SKILL.md`: the shipped skill is
  read-only, scans source and tests, ranks core candidates, reports coverage
  gaps and extraction recommendations, and stops at a proposal.
