// Package buildinfo carries the identity of the running binary: what was built,
// from which commit, and when. The values are stamped at link time; the fallbacks
// are what a plain `go build` produces.
package buildinfo

import "strings"

// Stamped with -ldflags "-X github.com/protonspy/dc-streaming-p2p/internal/buildinfo.Version=…".
var (
	Version = "dev"
	Commit  = "unknown"
	Date    = "unknown"
)

// Name is the binary this package describes.
const Name = "central"

// String renders the build for a version flag or a startup log line.
func String() string {
	parts := []string{Name, Version}
	if Commit != "unknown" && Commit != "" {
		parts = append(parts, "("+ShortCommit()+")")
	}
	if Date != "unknown" && Date != "" {
		parts = append(parts, "built "+Date)
	}
	return strings.Join(parts, " ")
}

// ShortCommit is the commit abbreviated the way git logs it, and the full value
// when it is already shorter than that.
func ShortCommit() string {
	const short = 7
	if len(Commit) <= short {
		return Commit
	}
	return Commit[:short]
}
