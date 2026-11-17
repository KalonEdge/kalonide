// Package version holds the build version, overridable at link time:
//
//	go build -ldflags "-X github.com/KalonEdge/kalonide/internal/version.Version=1.2.3"
package version

// Version is the Kalonide CLI version.
var Version = "0.1.0-dev"
