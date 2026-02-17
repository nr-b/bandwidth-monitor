// Package version holds build-time version information injected via ldflags.
//
// When building a tagged release the Makefile / CI sets:
//
//	-ldflags "-X bandwidth-monitor/version.Version=v0.1.0"
//
// For untagged (development) builds it falls back to the git commit:
//
//	-ldflags "-X bandwidth-monitor/version.Commit=abc1234"
//
// If neither is set (plain `go build`) the values stay "dev" / "unknown".
package version

import "fmt"

// Version is the semantic version tag (e.g. "v0.1.0").
// Set at build time via -ldflags "-X bandwidth-monitor/version.Version=...".
var Version = "dev"

// Commit is the short git commit hash.
// Set at build time via -ldflags "-X bandwidth-monitor/version.Commit=...".
var Commit = "unknown"

// String returns a human-readable version string.
// Tagged releases: "v0.1.0"
// Dev builds:      "dev (abc1234)"
func String() string {
	if Version != "dev" {
		return Version
	}
	if Commit != "unknown" {
		return fmt.Sprintf("dev (%s)", Commit)
	}
	return "dev"
}

// UserAgent returns the value to use in HTTP User-Agent headers.
// Tagged releases: "bandwidth-monitor/v0.1.0"
// Dev builds:      "bandwidth-monitor/dev (abc1234)"
func UserAgent() string {
	return "bandwidth-monitor/" + String()
}
