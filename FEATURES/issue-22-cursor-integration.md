# Issue #22 — Cursor agent integration: design & findings

**Status:** investigation only (no code, no PR). Uncommitted, for review.
**Scope:** mirror the existing Claude Code / Ralph-loop "Agent integration" section for Cursor — (a) the corrective context message ("the test is the spec, fix the implementation") and (b) a `golden-lock verify` pre-acceptance gate wired into a Cursor agent workflow/config.

---

## 0. Command-name correction (carry into README + the issue checklist)

The issue body and `FEATURES/feat-001.md` say **`golden-test verify`**. That binary/command does not exist in this repo. The correct, current names are:

- Binary / command: **`golden-lock verify`**
- Manifest: **`golden-lock/golden.lock`** (under the `golden-lock/` state dir)

The existing README section (lines 138–164) already uses the correct names. The Cursor section must match it, not the stale `golden-test` wording.

---

## 1. What the existing Claude Code / Ralph-loop section looks like (the thing we mirror)

`README.md` → `## Agent integration` (lines 138–164). Structure to replicate:

1. A lead-in: "Add to your agent context (CLAUDE.md / system prompt / task spec):".
2. A fenced **corrective context block** (the verbatim message) telling the agent: locked files encode invariants; `EACCES`/`EPERM` on them is a signal to fix *your own code*, not route around; never `chmod/chown/chflags/chattr/sudo`/delete-recreate; the only sanctioned change path is `sudo golden-lock unlock <file>` under human review; do not stop — treat it as feedback and course-correct.
3. A one-line **pre-acceptance gate** instruction (line 164): "For a Ralph-style loop, run `golden-lock verify` as a pre-acceptance gate before marking a task complete; on non-zero, reject and retry."

The Cursor section reuses the *same corrective prose* (it is tool-agnostic) and swaps only the *delivery mechanism*: where Claude Code uses CLAUDE.md + a manual Ralph loop, Cursor uses a `.cursor/rules/*.mdc` rule + a `.cursor/hooks.json` `stop` hook.

Related repo context already in place: `README.md` `## CI integration` (the `golden-lock verify` GitHub Actions step) and `.github/workflows/golden-verify.yml` — the platform-independent CI gate stays the authoritative backstop regardless of editor.

---

## 2. Cursor's actual agent/automation surface (researched)

Sources: Cursor official docs via context7 (`/websites/cursor`), pages `cursor.com/docs/rules`, `cursor.com/docs/hooks`, `cursor.com/docs/reference/third-party-hooks`, `cursor.com/docs/cli/*`. (The live `docs.cursor.com` pages are a JS-rendered SPA, so direct fetch returned empty; content below is from context7's indexed snapshots of `cursor.com/docs/*`.)

### 2a. Cursor Rules — how a project pins agent guidance
- Project rules live in **`.cursor/rules/*.mdc`** (MDC format). Plain `.md` files in that dir are **ignored**; for plain markdown Cursor reads **`AGENTS.md`** instead.
- Each `.mdc` file has YAML frontmatter with three keys: `description`, `globs`, `alwaysApply`.
  - `alwaysApply: true` → rule is always in the agent's context (this is what we want for the corrective message).
  - `globs: <pattern>` + `alwaysApply: false` → auto-attached when a matching file is in context.
  - `description: ...` + `alwaysApply: false`, `globs` omitted → agent-selected by description.
- Rules can be organized in subfolders under `.cursor/rules/`.
- Legacy `.cursorrules` (single root file) still works but is the deprecated path; new projects should use `.cursor/rules/*.mdc`. [~85% confidence on the "deprecated" framing — docs steer to `.mdc`/`AGENTS.md`; exact deprecation status not restated in the snapshot.]

### 2b. Cursor Hooks — the pre-acceptance gate mechanism (this is the key finding)
Cursor supports **lifecycle hooks** configured in a **`hooks.json`** file. Project-level hooks go in **`.cursor/hooks.json`**; user-level in the user config dir. Schema: `{ "version": 1, "hooks": { <event>: [{ "command": "<path>", "matcher": "<regex>"? }] } }`.

Hook scripts: receive a JSON payload on **stdin**, write a JSON response to **stdout**, and use **exit codes** to signal (e.g. `exit 2` with `{"permission":"deny"}` blocks a tool action).

Documented events include: `sessionStart`, `sessionEnd`, `preToolUse`, `postToolUse`, `beforeShellExecution`, `afterShellExecution`, `afterFileEdit`, `beforeSubmitPrompt`, `afterMCPExecution`, `preCompact`, **`stop`** (with `loop_limit`), `subagentStart`, `subagentStop`, plus tab-edit/workspace variants.

The **`stop`** hook is the exact analog of the Ralph-loop pre-acceptance gate:
- Fires **when the agent loop ends** (`status`: `completed` | `aborted` | `error`).
- Input includes `loop_count` (how many times this stop hook has already auto-followed-up).
- Output `{"followup_message": "<text>"}` → when non-empty, **Cursor auto-submits it as the next user message**, continuing the loop. This *is* "reject and retry".
- Auto-follow-up cap defaults to **5 per script**, configurable via `loop_limit` (`null` = uncapped). Prevents runaway loops.

So: in the `stop` hook, run `golden-lock verify`; if it exits non-zero, return a `followup_message` carrying the corrective message → the Cursor agent is rejected and forced to keep fixing the implementation. On exit 0, return `{}` and the task is accepted.

Bonus defense-in-depth: `beforeShellExecution` (or `preToolUse` with a `matcher`) can return `{"permission":"deny"}` (exit 2) to proactively block lock-defeating commands (`chmod`/`chown`/`chflags`/`chattr`/`sudo golden-lock unlock`/delete-recreate of locked paths) before they run — catching the agent earlier than the manifest hash check. Optional add-on, not required for the three "Done When" items.

### 2c. Cursor CLI (headless) — for CI, not the in-editor gate
The Cursor CLI agent runs non-interactively with `-p`/`--print` (`agent -p "..." --output-format json|text`), intended for scripts/CI. Relevant only if you run the *Cursor agent itself* inside CI. For golden-lock's purposes the existing `golden-lock verify` GitHub Actions step (`.github/workflows/golden-verify.yml`) is the platform-independent CI gate and needs no Cursor-specific change.

---

## 3. Concrete integration design

Three artifacts, committed to the user's *target* repo (the one whose tests are locked), not to golden-lock itself. golden-lock's job is to *document* them (README) so users can drop them in.

### Artifact A — `.cursor/rules/golden-lock.mdc` (corrective message, always in context)

```markdown
---
description: Golden-lock immutable files — the test is the spec, fix the implementation
alwaysApply: true
---

Files listed in golden-lock/golden.lock are immutable: root-owned, chmod 444, and marked
immutable at the filesystem layer. They are locked on purpose — each encodes an
invariant (a test that is the spec, a config that must not drift, a fixture that
must stay byte-stable).

If you hit a permission error touching one of them — EACCES on a write, or EPERM on
a rename/move/chflags/chattr — do NOT treat it as an obstacle to route around. It is
a signal that you are about to change the invariant instead of meeting it. When it happens:

- Look at your OWN code first. A locked file that fails verification or blocks your
  edit almost always means your change is wrong or incomplete. Meet the invariant,
  don't rewrite it.
- Do not try to defeat the lock. No chmod, chown, chflags, chattr, sudo, deleting and
  recreating, or editing the file. The only sanctioned way to change a locked file is
  `sudo golden-lock unlock <file>` under human review — never something to do
  mid-task on your own.
- Do not stop. This error is feedback, not a fatal failure. Course-correct your
  work and keep going — do not abort the task, halt the loop, or report the run as
  blocked because a locked file refused a write.
```

(Body is byte-identical to the README's existing corrective block, so the two editors share one canonical message. For teams that prefer plain markdown, the same body in `AGENTS.md` is the documented fallback.)

### Artifact B — `.cursor/hooks.json` (wire the gate)

```json
{
  "version": 1,
  "hooks": {
    "stop": [{ "command": ".cursor/hooks/golden-verify.sh", "loop_limit": 10 }]
  }
}
```

### Artifact C — `.cursor/hooks/golden-verify.sh` (pre-acceptance gate)

```sh
#!/usr/bin/env sh
# Cursor `stop` hook: pre-acceptance gate for golden-lock.
# Runs when the agent thinks it's done. If a locked file drifted, golden-lock verify
# exits non-zero; we reject by auto-submitting a corrective follow-up so the agent
# keeps fixing the implementation instead of the (locked) test.
input=$(cat)   # stop-hook JSON on stdin: { status, loop_count, ... } — not needed here

if golden-lock verify >/dev/null 2>&1; then
  printf '{}\n'                       # exit 0: hashes match — accept, no follow-up
else
  printf '%s\n' '{"followup_message":"golden-lock verify failed: a locked file (the spec) was changed or your implementation does not satisfy it. Do NOT modify any file under golden-lock/golden.lock and do NOT try to defeat the lock (no chmod/chown/chflags/chattr/sudo/unlock). Treat the locked test as the spec and fix the implementation, then continue."}'
fi
exit 0
```

Notes:
- `loop_limit: 10` caps auto-retries (default is 5; `null` = uncapped) so a genuinely-stuck agent can't loop forever.
- The hook calls the same `golden-lock verify` used by CI — single source of truth. CI (`golden-verify.yml`) remains the authoritative backstop; the hook is the fast in-editor pre-acceptance gate.
- Optional `beforeShellExecution` deny-hook (section 2b) can be offered as a hardening add-on but is **not** needed to satisfy the issue.

### README section outline (mirror of `## Agent integration`)

Add a subsection right after the existing Claude Code / Ralph-loop paragraph, e.g. `### Cursor`:

1. One-line lead-in: Cursor pins agent guidance via `.cursor/rules/*.mdc` and runs gates via `.cursor/hooks.json` lifecycle hooks.
2. **Corrective message** → drop the same block into `.cursor/rules/golden-lock.mdc` with `alwaysApply: true` (or `AGENTS.md`). Show Artifact A.
3. **Pre-acceptance gate** → register a `stop` hook that runs `golden-lock verify`; on non-zero, return a `followup_message` so Cursor auto-submits a rejection and the agent retries — the Ralph-loop "reject and retry" expressed in Cursor's native loop. Show Artifacts B + C.
4. One line noting CI (`golden-verify.yml`) stays the authoritative gate; the hook is the in-editor fast path.

---

## 4. Map to issue "Done When"

| Done-When item | Satisfied by | Notes |
|---|---|---|
| README has a Cursor integration section | New `### Cursor` subsection under `## Agent integration` (outline in §3) | Fix stale `golden-test` → `golden-lock` while here |
| Corrective message documented for Cursor agent context | Artifact A: `.cursor/rules/golden-lock.mdc` (`alwaysApply: true`), AGENTS.md fallback | Body reused verbatim from existing README block |
| `golden-lock verify` wired as a pre-acceptance gate in a Cursor agent workflow/config | Artifacts B + C: `.cursor/hooks.json` `stop` hook → `golden-verify.sh` → `followup_message` on failure | Native analog of Ralph-loop "reject and retry"; `loop_limit` bounds it |

---

## 5. Uncertainties / confidence flags

- Cursor **Rules** (`.cursor/rules/*.mdc`, frontmatter `description`/`globs`/`alwaysApply`): documented and stable. High confidence.
- Cursor **Hooks** (`hooks.json`, `stop` hook, `followup_message`, `loop_limit`, stdin/stdout JSON, `permission: deny`/exit 2): documented at `cursor.com/docs/hooks`. High confidence on the contract. [~80% on availability tier — hooks are a relatively new feature and may be gated by Cursor version / not GA on all plans; the README should say "requires a Cursor version that supports `.cursor/hooks.json`."]
- `.cursorrules` deprecation framing: [~85%] — docs steer to `.mdc`/`AGENTS.md`; legacy file still read.
- Exact `stop`-hook input field names beyond `status`/`loop_count` (e.g. `conversation_id`, `generation_id`) appear in a docs TypeScript sample but aren't needed by our shell hook.

---

## 6. Feasibility verdict

**Fully feasible now — not blocked on any missing Cursor capability.** Cursor's `stop` hook + `followup_message` loop is a precise, documented analog of the Ralph-loop pre-acceptance gate, and `.cursor/rules/*.mdc` (`alwaysApply: true`) cleanly delivers the corrective message. Implementation is pure documentation in golden-lock's README (plus optional sample `.cursor/` files to ship as copy-paste templates). Only caveats: confirm the user's Cursor version supports `hooks.json`, and correct the stale `golden-test` → `golden-lock` naming.
```