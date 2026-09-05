// Package version exposes build-time release metadata.
package version

// Version is replaced by the release build with -ldflags. Development builds
// deliberately identify themselves as dev.
var Version = "dev"
