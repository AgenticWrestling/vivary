package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVivgenCommand_GoldenSnippets(t *testing.T) {
	root := repoRootFromContract(t)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "generated.go")

	cmd := exec.Command("go", "run", "./cmd/vivgen", "-dir", "capabilities", "-pkg", "capabilities", "-output", outFile)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vivgen command failed: %v\n%s", err, output)
	}

	generated := mustReadGeneratedFile(t, outFile)
	got := extractGoldenSnippets(t, generated)
	want := mustReadGeneratedFile(t, filepath.Join(root, "cmd", "vivgen", "testdata", "generated_snippets.golden"))
	if strings.TrimSpace(got) != strings.TrimSpace(want) {
		t.Fatalf("golden mismatch\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

func TestVivgenCommand_GeneratesGoTypesFromKDL(t *testing.T) {
	root := repoRootFromContract(t)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "generated.go")

	cmd := exec.Command("go", "run", "./cmd/vivgen", "-dir", "capabilities", "-pkg", "capabilities", "-output", outFile)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vivgen command failed: %v\n%s", err, output)
	}

	generated := mustReadGeneratedFile(t, outFile)
	assertContains(t, generated, "type Browser_Page_Read struct")
	assertContains(t, generated, "URL")
	assertContains(t, generated, "Timeout")
	assertContains(t, generated, "WaitFor")
	assertContains(t, generated, "type Filesystem_File_Write struct")
	assertContains(t, generated, "`json:\"content\"`")
}

func TestVivgenCommand_GeneratesRegistry(t *testing.T) {
	root := repoRootFromContract(t)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "generated.go")

	cmd := exec.Command("go", "run", "./cmd/vivgen", "-dir", "capabilities", "-pkg", "capabilities", "-output", outFile)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vivgen command failed: %v\n%s", err, output)
	}

	generated := mustReadGeneratedFile(t, outFile)
	assertContains(t, generated, "func GeneratedRegistry() map[string]string")
	assertContains(t, generated, `"Browser_Page_Read"`)
	assertContains(t, generated, `Browser_Page_ReadSchema`)
	assertContains(t, generated, `"Filesystem_File_Write"`)
	assertContains(t, generated, `Filesystem_File_WriteSchema`)
	assertContains(t, generated, `"Commerce_Order_Create"`)
	assertContains(t, generated, `Commerce_Order_CreateSchema`)
}

func TestVivgenCommand_ExplainSchemaContainsKDLMetadata(t *testing.T) {
	root := repoRootFromContract(t)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "generated.go")

	cmd := exec.Command("go", "run", "./cmd/vivgen", "-dir", "capabilities", "-pkg", "capabilities", "-output", outFile)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vivgen command failed: %v\n%s", err, output)
	}

	generated := mustReadGeneratedFile(t, outFile)
	assertContains(t, generated, `"title": "Browser_Page_Read"`)
	assertContains(t, generated, `"description": "Fetch the readable text content of a web page, stripped of markup, scripts, and boilerplate"`)
	assertContains(t, generated, `"description": "Fully-qualified URL of the page to fetch"`)
	assertContains(t, generated, `"default": "networkidle"`)
	assertContains(t, generated, `"enum": [`)
	assertContains(t, generated, `"load"`)
	assertContains(t, generated, `"networkidle"`)
}

func TestVivgenCommand_OutputIsFormattedGo(t *testing.T) {
	root := repoRootFromContract(t)
	outDir := t.TempDir()
	outFile := filepath.Join(outDir, "generated.go")

	cmd := exec.Command("go", "run", "./cmd/vivgen", "-dir", "capabilities", "-pkg", "capabilities", "-output", outFile)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vivgen command failed: %v\n%s", err, output)
	}

	verify := exec.Command("gofmt", "-w", outFile)
	if out, err := verify.CombinedOutput(); err != nil {
		t.Fatalf("gofmt verification failed: %v\n%s", err, out)
	}
	generated := mustReadGeneratedFile(t, outFile)
	assertContains(t, generated, "package capabilities")
}

func TestVivgenCommand_GeneratedPackageCompilesAndIsUsable(t *testing.T) {
	root := repoRootFromContract(t)
	workdir := t.TempDir()
	outFile := filepath.Join(workdir, "generated.go")

	cmd := exec.Command("go", "run", "./cmd/vivgen", "-dir", "capabilities", "-pkg", "capabilities", "-output", outFile)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("vivgen command failed: %v\n%s", err, output)
	}

	goMod := fmt.Sprintf("module example.com/generated\n\ngo 1.23\n\nrequire vivary.dev/vivary v0.0.0\n\nreplace vivary.dev/vivary => %s\n", root)
	if err := os.WriteFile(filepath.Join(workdir, "go.mod"), []byte(goMod), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	if err := os.WriteFile(filepath.Join(workdir, "consumer_test.go"), []byte(`package capabilities

import "testing"

func TestGeneratedRegistryAndTypes(t *testing.T) {
	r := GeneratedRegistry()
	if _, ok := r["Browser_Page_Read"]; !ok {
		t.Fatal("missing Browser_Page_Read in registry")
	}
	_ = Browser_Page_Read{URL: "https://example.com", Timeout: 30, WaitFor: "networkidle"}
	_ = Filesystem_File_Write{Path: "output/report.md", Content: "hello"}
}
`), 0o644); err != nil {
		t.Fatalf("write consumer test: %v", err)
	}

	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = workdir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Fatalf("go mod tidy failed: %v\n%s", err, out)
	}

	verify := exec.Command("go", "test", ".")
	verify.Dir = workdir
	out, err := verify.CombinedOutput()
	if err != nil {
		t.Fatalf("generated package usability test failed: %v\n%s", err, out)
	}
	assertContains(t, string(out), "ok")
}

func repoRootFromContract(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

func mustReadGeneratedFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func assertContains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Fatalf("expected output to contain %q\noutput:\n%s", want, got)
	}
}

func extractGoldenSnippets(t *testing.T, generated string) string {
	t.Helper()
	sections := []struct{ start, end string }{
		{"func GeneratedRegistry() map[string]string {", "// Browser_Form_SubmitSchema"},
		{"const Browser_Page_ReadSchema = `", "// Browser_Page_ScreenshotSchema"},
		{"const Filesystem_File_WriteSchema = `", "// Media_Audio_TranscribeSchema"},
	}
	parts := make([]string, 0, len(sections))
	for _, s := range sections {
		start := strings.Index(generated, s.start)
		if start < 0 {
			t.Fatalf("missing start marker %q", s.start)
		}
		end := strings.Index(generated[start:], s.end)
		if end < 0 {
			t.Fatalf("missing end marker %q", s.end)
		}
		parts = append(parts, strings.TrimSpace(generated[start:start+end]))
	}
	return strings.Join(parts, "\n\n---SNIP---\n\n")
}
