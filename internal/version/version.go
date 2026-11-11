// Package version holds laman's version, set by release builds:
//
//	go build -ldflags "-X laman/internal/version.Version=0.2.0"
//
// Local builds say "dev" and never look for updates.
package version

var Version = "dev"

// Repo is where releases are published (GitHub owner/name).
const Repo = "raditzfarhan/laman"

// Dev reports whether this is a local build rather than a release.
func Dev() bool { return Version == "dev" || Version == "" }
