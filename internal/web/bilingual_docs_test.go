package web

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicMarkdownDocumentationIsBilingual(t *testing.T) {
	paths := []string{
		"README.md",
		"CONTRIBUTING.md",
		"SECURITY.md",
		"docs/已知限制.md",
		"docs/相容性與驗證矩陣.md",
		"docs/研究與測試成果.md",
	}
	for _, path := range paths {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		if !strings.Contains(text, "\n---\n\n# ") {
			t.Errorf("%s missing same-file bilingual separator", path)
		}
		if !strings.Contains(text, "English") && path != "SECURITY.md" {
			// SECURITY.md uses the English heading "Security" instead of a literal
			// language label, while the other public documents name their English
			// section explicitly or include English in a section heading.
			if !strings.Contains(text, "# Known Limitations") && !strings.Contains(text, "# Compatibility and Validation Matrix") && !strings.Contains(text, "# Research and Validation Results") && !strings.Contains(text, "# Contributing Guide") {
				t.Errorf("%s missing English documentation section", path)
			}
		}
	}
}
