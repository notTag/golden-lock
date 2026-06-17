# [001] Cursor SDK agent integration

- Status: open
- Created: 2026-06-04
- Priority: med
- Bump: patch

## What
Extend golden-test's agent-integration guidance to Cursor's SDK/agent, mirroring the existing Claude Code / Ralph-loop section: the corrective context message ("the test is the spec, fix the implementation") plus a `golden-test verify` pre-acceptance gate wired into a Cursor agent workflow/config.

## Why
Cursor-driven agents optimize for passing tests the same way Claude Code does and can game assertions when they can write to test files. They need the same immutable-test enforcement and corrective context to redirect toward fixing the implementation instead of the test.

## Done When
- [ ] README has a Cursor SDK integration section
- [ ] Corrective message documented for Cursor agent context
- [ ] `golden-test verify` wired as a pre-acceptance gate in a Cursor agent workflow/config
