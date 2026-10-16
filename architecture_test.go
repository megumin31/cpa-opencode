package main

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Keep dependency direction enforceable without a custom linter or generated
// interfaces. Tests may cross boundaries for integration; production may not.
func TestInternalDependencyDirection(t *testing.T) {
	const prefix = "github.com/megumin31/cpa-opencode-zen/internal/"
	allowed := map[string]string{
		"buildinfo":   "",
		"catalog":     "buildinfo",
		"fault":       "",
		"diagnostics": "fault",
		"transport":   "fault",
		"protocol":    "catalog diagnostics fault",
		"plugin":      "buildinfo catalog diagnostics fault protocol transport",
	}
	entries, err := os.ReadDir("internal")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		imports, ok := allowed[name]
		if !ok {
			t.Errorf("new package %s must declare its dependency boundary", name)
			continue
		}
		files, err := filepath.Glob(filepath.Join("internal", name, "*.go"))
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range files {
			if strings.HasSuffix(path, "_test.go") {
				continue
			}
			f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
			if err != nil {
				t.Fatal(err)
			}
			for _, spec := range f.Imports {
				target, err := strconv.Unquote(spec.Path.Value)
				if err != nil {
					t.Fatal(err)
				}
				if target == "C" {
					t.Errorf("C ABI types escaped root adapter: %s", path)
				}
				if !strings.HasPrefix(target, prefix) {
					continue
				}
				dependency := strings.TrimPrefix(target, prefix)
				found := false
				for _, candidate := range strings.Fields(imports) {
					if candidate == dependency {
						found = true
					}
				}
				if !found {
					t.Errorf("%s imports forbidden upper/cross layer %s", path, dependency)
				}
			}
		}
	}
}
