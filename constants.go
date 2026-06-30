package main

import _ "embed"

// gettingStartedGuide is dropped at golden-lock/getting-started.md by setup
// (only when absent, so a hand-edited copy is never clobbered). Its contents are
// the repo-root getting-started.md, baked into the binary at build time.
//
//go:embed getting-started.md
var gettingStartedGuide string
