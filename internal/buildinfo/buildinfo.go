// Package buildinfo is the single source of plugin identity and release version.
// It has no runtime or CPA dependencies so build tooling can import it cheaply.
package buildinfo

const ProviderID = "opencode"

// Version is injected from the release tag. 0.0.0 denotes an unreleased build.
var Version = "0.0.0"

// PluginFilename is the Linux C-shared library name expected by the native host.
func PluginFilename() string {
	return ProviderID + "-v" + Version + ".so"
}

// BuildTag preserves the complete release tag, or dev for branch builds.
var BuildTag = "dev"
