package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoNintendoImports enforces the PRD §10 architecture check: the core
// must not import Nintendo/Pretendo protocol packages.
func TestNoNintendoImports(t *testing.T) {
	banned := []string{
		"pretendonetwork", "pretendo", "nintendo", "nex", "nasc", "nnas",
	}
	// "nex" and friends only count as path segments of imports, not substring
	// noise, so check import lines for these tokens conservatively.
	root := ".."
	var files []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !info.IsDir() && strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			files = append(files, path)
		}
		if info.IsDir() && (info.Name() == ".git" || info.Name() == "proto") {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if !strings.HasPrefix(line, `"`) && !strings.HasPrefix(line, "_ ") {
				continue
			}
			lower := strings.ToLower(line)
			for _, b := range banned {
				if strings.Contains(lower, b) {
					t.Errorf("%s: banned import %q (token %q)", f, line, b)
				}
			}
		}
	}
}
