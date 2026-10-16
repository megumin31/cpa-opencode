package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// The independent host module consumes buildmeta output instead of duplicating
// the production module's release constants or importing its internal packages.
type releaseMetadata struct {
	Provider string `json:"provider"`
	Version  string `json:"version"`
	Filename string `json:"filename"`
}

func loadReleaseMetadata(directory string) (releaseMetadata, error) {
	raw := []byte(os.Getenv("OPENCODE_PLUGIN_METADATA"))
	if len(raw) == 0 {
		var err error
		raw, err = os.ReadFile(filepath.Join(directory, "plugin-metadata.json"))
		if err != nil {
			return releaseMetadata{}, fmt.Errorf("read plugin metadata (run make build, or set OPENCODE_PLUGIN_METADATA from cmd/buildmeta): %w", err)
		}
	}
	return parseReleaseMetadata(raw)
}

func parseReleaseMetadata(raw []byte) (releaseMetadata, error) {
	var release releaseMetadata
	if err := json.Unmarshal(raw, &release); err != nil {
		return release, fmt.Errorf("invalid plugin metadata: %w", err)
	}
	if !regexp.MustCompile(`^[a-z0-9-]+$`).MatchString(release.Provider) ||
		!regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`).MatchString(release.Version) ||
		release.Filename != release.Provider+"-v"+release.Version+".so" {
		return release, fmt.Errorf("inconsistent plugin provider/version/filename metadata")
	}
	return release, nil
}

func (r releaseMetadata) replacementFilename() (string, error) {
	parts := strings.Split(r.Version, ".")
	if len(parts) != 3 {
		return "", fmt.Errorf("invalid plugin version %q", r.Version)
	}
	patch, err := strconv.ParseUint(parts[2], 10, 64)
	if err != nil || patch == ^uint64(0) {
		return "", fmt.Errorf("cannot increment plugin patch version %q", r.Version)
	}
	return fmt.Sprintf("%s-v%s.%s.%d.so", r.Provider, parts[0], parts[1], patch+1), nil
}
