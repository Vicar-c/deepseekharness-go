// Package version exposes the build identity of the Go rewrite.
//
// Values are overridable at build time:
//
//	go build -ldflags "-X deekseekharness-go/internal/version.Version=0.2.2-go.1"
package version

import (
	"fmt"
	"runtime"
)

// Version is the semantic version reported by the CLI and the Web API.
var Version = "0.1.0-dev"

// Commit is the VCS revision the binary was built from.
var Commit = "unknown"

// BuildTime is the UTC build timestamp injected by the release build.
var BuildTime = "unknown"

// String renders a human readable version line.
func String() string {
	return fmt.Sprintf("%s (commit %s, built %s, %s)", Version, Commit, BuildTime, runtime.Version())
}
