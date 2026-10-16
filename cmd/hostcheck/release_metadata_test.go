package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReleaseMetadata(t *testing.T) {
	const fixture = `{"provider":"sample","version":"2.3.4","filename":"sample-v2.3.4.so"}`
	r, err := parseReleaseMetadata([]byte(fixture))
	if err != nil {
		t.Fatal(err)
	}
	if next, err := r.replacementFilename(); err != nil || next != "sample-v2.3.5.so" {
		t.Fatalf("replacement = %q, %v", next, err)
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "plugin-metadata.json"), []byte(fixture), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OPENCODE_PLUGIN_METADATA", "")
	if got, err := loadReleaseMetadata(dir); err != nil || got != r {
		t.Fatalf("file metadata = %+v, %v", got, err)
	}
	t.Setenv("OPENCODE_PLUGIN_METADATA", fixture)
	if got, err := loadReleaseMetadata(t.TempDir()); err != nil || got != r {
		t.Fatalf("environment metadata = %+v, %v", got, err)
	}
}

func TestReleaseMetadataRejectsInvalidInput(t *testing.T) {
	for _, raw := range []string{
		`not json`, `{}`, `null`,
		`{"provider":"../sample","version":"2.3.4","filename":"../sample-v2.3.4.so"}`,
		`{"provider":"sample","version":"2.3","filename":"sample-v2.3.so"}`,
		`{"provider":"sample","version":"2.3.4","filename":"other-v2.3.4.so"}`,
	} {
		if _, err := parseReleaseMetadata([]byte(raw)); err == nil {
			t.Fatalf("accepted invalid metadata %q", raw)
		}
	}
	for _, version := range []string{"invalid", "2.3.18446744073709551615", "2.3.18446744073709551616"} {
		if _, err := (releaseMetadata{Provider: "sample", Version: version}).replacementFilename(); err == nil {
			t.Fatalf("accepted non-incrementable version %q", version)
		}
	}
	t.Setenv("OPENCODE_PLUGIN_METADATA", "")
	if _, err := loadReleaseMetadata(t.TempDir()); err == nil {
		t.Fatal("accepted missing metadata")
	}
}
