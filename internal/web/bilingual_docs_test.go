package web

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestPublicMarkdownDocumentationLanguagesAreAvailable(t *testing.T) {
	sameFilePaths := []string{
		"README.md",
		"CONTRIBUTING.md",
		"SECURITY.md",
	}
	for _, path := range sameFilePaths {
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

	paired := []string{
		"getting-started.md",
		"architecture.md",
		"hermes-hindsight.md",
		"deployment.md",
		"compatibility.md",
		"known-limitations.md",
		"research-evidence.md",
		"model-capabilities.md",
		"api-contracts.md",
		"runtime-settings.md",
	}
	for _, name := range paired {
		for _, language := range []string{"zh-TW", "en"} {
			path := filepath.Join("..", "..", "docs", language, name)
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read %s: %v", path, err)
			}
			if len(strings.TrimSpace(string(raw))) < 80 {
				t.Errorf("%s is unexpectedly empty", path)
			}
		}
	}

	legacyRoutes := map[string][]string{
		"docs/Hermes整合指南.md":                              {"zh-TW/hermes-hindsight.md", "en/hermes-hindsight.md"},
		"docs/部署與反向代理.md":                                 {"zh-TW/deployment.md", "en/deployment.md"},
		"docs/已知限制.md":                                    {"zh-TW/known-limitations.md", "en/known-limitations.md"},
		"docs/相容性與驗證矩陣.md":                                {"zh-TW/compatibility.md", "en/compatibility.md"},
		"docs/研究與測試成果.md":                                 {"zh-TW/research-evidence.md", "en/research-evidence.md"},
		"docs/MODEL_CAPABILITY_EVIDENCE.md":               {"zh-TW/model-capabilities.md", "en/model-capabilities.md"},
		"docs/MEMORY_PROVIDER_COMPATIBILITY_MODE_PLAN.md": {"zh-TW/hermes-hindsight.md", "en/hermes-hindsight.md", "history/"},
	}
	for path, required := range legacyRoutes {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		text := string(raw)
		for _, marker := range required {
			if !strings.Contains(text, marker) {
				t.Errorf("%s missing route to %s", path, marker)
			}
		}
		if lines := strings.Count(text, "\n") + 1; lines > 40 {
			t.Errorf("legacy route %s grew to %d lines; keep it as a short pointer", path, lines)
		}
	}
}

func TestDocumentationProgressiveLoadingLayout(t *testing.T) {
	limits := map[string]int{
		"AGENTS.md":       80,
		"README.md":       180,
		"CONTRIBUTING.md": 120,
		"docs/README.md":  100,
	}
	for path, maxLines := range limits {
		raw, err := os.ReadFile(filepath.Join("../..", path))
		if err != nil {
			t.Fatal(err)
		}
		if lines := strings.Count(string(raw), "\n") + 1; lines > maxLines {
			t.Errorf("%s has %d lines, max %d; move deep content behind the topic router", path, lines, maxLines)
		}
	}
}

func TestPublicMarkdownHasSingleTrailingNewline(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	check := func(path string) error {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		trimmed := bytes.TrimRight(raw, "\r\n")
		if !bytes.Equal(raw[len(trimmed):], []byte("\n")) {
			rel, relErr := filepath.Rel(repoRoot, path)
			if relErr != nil {
				rel = path
			}
			t.Errorf("%s must end with exactly one LF newline and no blank EOF line", rel)
		}
		return nil
	}
	entries, err := os.ReadDir(repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".md" {
			continue
		}
		if err := check(filepath.Join(repoRoot, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	err = filepath.WalkDir(filepath.Join(repoRoot, "docs"), func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".md" {
			return nil
		}
		return check(path)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicMarkdownRelativeLinksResolve(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	linkPattern := regexp.MustCompile(`\[[^\]]*\]\(([^)]+)\)`)
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, match := range linkPattern.FindAllStringSubmatch(string(raw), -1) {
			target := strings.TrimSpace(match[1])
			if target == "" || strings.HasPrefix(target, "#") || strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target = strings.SplitN(target, "#", 2)[0]
			if target == "" {
				continue
			}
			resolved := filepath.Clean(filepath.Join(filepath.Dir(path), target))
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s has broken relative link %q", path, match[1])
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicMarkdownDoesNotContainPrivateOpsMarkers(t *testing.T) {
	repoRoot := filepath.Clean(filepath.Join("..", ".."))
	forbidden := []string{
		"home-nas-agent",
		"gabriel920",
		"/Users/gabrielchen",
		"LOCAL_NAS_SHARED_PASSWORD",
	}
	err := filepath.WalkDir(repoRoot, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".md" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		text := string(raw)
		for _, marker := range forbidden {
			if strings.Contains(text, marker) {
				t.Errorf("%s contains private operations marker %q", path, marker)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
