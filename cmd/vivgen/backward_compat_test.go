package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVivgenBackwardCompat_KDLFieldRemovalFails(t *testing.T) {
	prev := mustSchemaForCapabilityFromDir(t, repoRootCompat(t), "Browser_Page_Read")
	modifiedDir := writeModifiedCapabilitiesDir(t, func(s string) string {
		return strings.Replace(s, "  field wait_for type=string required=#false default=networkidle {\n    description \"Page load event to wait for before extracting content\"\n    enum load domcontentloaded networkidle\n  }\n", "", 1)
	})
	next := mustSchemaForCapabilityFromDir(t, modifiedDir, "Browser_Page_Read")
	if err := checkGeneratedSchemaBackwardCompat(prev, next); err == nil {
		t.Fatal("expected backward-compat failure when removing field from KDL")
	}
}

func TestVivgenBackwardCompat_KDLFieldRenameFails(t *testing.T) {
	prev := mustSchemaForCapabilityFromDir(t, repoRootCompat(t), "Browser_Page_Read")
	modifiedDir := writeModifiedCapabilitiesDir(t, func(s string) string {
		return strings.Replace(s, "field wait_for", "field wait_until", 1)
	})
	next := mustSchemaForCapabilityFromDir(t, modifiedDir, "Browser_Page_Read")
	if err := checkGeneratedSchemaBackwardCompat(prev, next); err == nil {
		t.Fatal("expected backward-compat failure when renaming field in KDL")
	}
}

func TestVivgenBackwardCompat_KDLFieldAdditionPasses(t *testing.T) {
	prev := mustSchemaForCapabilityFromDir(t, repoRootCompat(t), "Browser_Page_Read")
	modifiedDir := writeModifiedCapabilitiesDir(t, func(s string) string {
		needle := "  returns     string description=\"Readable page text in markdown format\"\n}"
		replacement := "  field       max_chars type=int required=#false default=50000 {\n    description \"Maximum characters to return\"\n    min         1\n  }\n  returns     string description=\"Readable page text in markdown format\"\n}"
		return strings.Replace(s, needle, replacement, 1)
	})
	next := mustSchemaForCapabilityFromDir(t, modifiedDir, "Browser_Page_Read")
	if err := checkGeneratedSchemaBackwardCompat(prev, next); err != nil {
		t.Fatalf("expected additive KDL change to remain backward-compatible: %v", err)
	}
}

func mustSchemaForCapabilityFromDir(t *testing.T, rootDir, name string) *JSONSchema {
	t.Helper()
	caps, err := loadCapabilities(filepath.Join(rootDir, "capabilities"))
	if err != nil {
		t.Fatalf("loadCapabilities: %v", err)
	}
	cap := mustFindCapability(t, caps, name)
	return generateSchemaForCapability(cap)
}

func writeModifiedCapabilitiesDir(t *testing.T, mutate func(string) string) string {
	t.Helper()
	dir := t.TempDir()
	capDir := filepath.Join(dir, "capabilities")
	if err := os.MkdirAll(capDir, 0o755); err != nil {
		t.Fatalf("mkdir capabilities dir: %v", err)
	}
	original := mustReadFileCompat(t, filepath.Join(repoRootCompat(t), "capabilities", "capabilities.kdl"))
	modified := mutate(original)
	if err := os.WriteFile(filepath.Join(capDir, "capabilities.kdl"), []byte(modified), 0o644); err != nil {
		t.Fatalf("write capabilities.kdl: %v", err)
	}
	return dir
}

func repoRootCompat(t *testing.T) string {
	t.Helper()
	return repoRootFromContract(t)
}

func mustReadFileCompat(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func checkGeneratedSchemaBackwardCompat(prev, next *JSONSchema) error {
	prevProps := map[string]bool{}
	nextProps := map[string]bool{}
	for name := range prev.Properties {
		prevProps[name] = true
	}
	for name := range next.Properties {
		nextProps[name] = true
	}
	for name := range prevProps {
		if !nextProps[name] {
			return os.ErrInvalid
		}
	}
	return nil
}
