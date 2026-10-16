// Package buildinfo is the single source of plugin identity and release version.
// It has no runtime or CPA dependencies so build tooling can import it cheaply.
package buildinfo

const (
	ProviderID = "opencode"
	Version    = "0.1.0"
)

// PluginFilename is the Linux C-shared library name expected by the native host.
func PluginFilename() string {
	return ProviderID + "-v" + Version + ".so"
}
