package main

// version.go — the `version` subcommand and `--version`/`-v` flag.
//
// Prints a single line of the form:
//
//	golden-lock <version> (<revision>[-dirty]) <go-version>
//
// The version field defaults to "dev" and is overridable at build time via
//	go build -ldflags "-X main.version=v1.2.3"
// The revision, dirty marker, and Go toolchain version are read from the
// embedded build info and are omitted gracefully when unavailable.

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
)

// version is the release version string. It defaults to "dev" for local builds
// and is overridden at release time with -ldflags "-X main.version=...".
var version = "dev"

// shortRevisionLen caps the VCS revision in the version line to a readable
// abbreviated hash.
const shortRevisionLen = 12

// runVersion prints the version line to stdout and returns ExitWriteOK. It
// needs no privilege.
func runVersion(_ []string) int {
	fmt.Fprintln(os.Stdout, versionString())
	return ExitWriteOK
}

// versionString builds the user-facing version line, enriching the build-time
// version with the VCS revision, a dirty marker, and the Go toolchain version
// drawn from the embedded build info when that info is present.
func versionString() string {
	line := fmt.Sprintf("%s %s", progName(), version)

	buildInfo, ok := debug.ReadBuildInfo()
	if !ok {
		return line
	}

	revision, dirty := vcsRevision(buildInfo)
	if revision != "" {
		line += " (" + revision
		if dirty {
			line += "-dirty"
		}
		line += ")"
	}

	line += " " + runtime.Version()
	return line
}

// vcsRevision extracts the abbreviated VCS revision and dirty state from the
// build info settings. It returns an empty revision when the binary was built
// without VCS stamping.
func vcsRevision(buildInfo *debug.BuildInfo) (revision string, dirty bool) {
	for _, setting := range buildInfo.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}

	if len(revision) > shortRevisionLen {
		revision = revision[:shortRevisionLen]
	}
	return revision, dirty
}
