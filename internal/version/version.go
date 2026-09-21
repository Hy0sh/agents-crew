// Package version reports what a binary was built from. Nothing is
// stamped at build time: the Go toolchain records the module version
// when installed from a tag and the commit otherwise, which spares this
// tool any ldflags dance.
package version

import (
	"runtime/debug"
	"strings"
)

// String returns the version to print for --version/-v.
func String() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "unknown"
	}

	var revision, buildTime string
	dirty := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = setting.Value
		case "vcs.time":
			buildTime = setting.Value
		case "vcs.modified":
			dirty = setting.Value == "true"
		}
	}

	v := info.Main.Version
	// A build straight from a working copy has no module version to report.
	if v == "" || v == "(devel)" {
		v = "devel"
	}

	var b strings.Builder
	b.WriteString(v)
	if revision != "" {
		b.WriteString(" (")
		b.WriteString(shortRev(revision))
		if buildTime != "" {
			b.WriteString(", ")
			b.WriteString(buildTime)
		}
		if dirty {
			b.WriteString(", uncommitted changes")
		}
		b.WriteString(")")
	}
	return b.String()
}

func shortRev(revision string) string {
	if len(revision) > 12 {
		return revision[:12]
	}
	return revision
}
