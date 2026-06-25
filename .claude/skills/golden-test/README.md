# golden-test (skill)

Front end for the `golden-lock` CLI in this repo. Scans a repo's tests,
classifies which guard core functionality, prints an immutability table with
break-out recommendations, and — on confirmation — extracts each core test into
a `<name>.golden.<ext>` sibling and locks it (`sudo golden-lock lock`).

Canonical source lives in this repo at `.claude/skills/golden-test/`. It is
symlinked into the skills repo (`~/Code/AI/skills/golden-test`) and from there
into the Claude skills dir (`~/.claude/skills/golden-test`).

Invoke with `/golden-test`.
