package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---- validateCapName -------------------------------------------------------

func TestValidateCapName_Valid(t *testing.T) {
	valid := []string{
		"Browser_Page_Read",
		"Filesystem_File_Write",
		"Calendar_Event_Create",
		"Email_Message_List",
	}
	for _, name := range valid {
		if err := validateCapName(name); err != nil {
			t.Errorf("validateCapName(%q) unexpected error: %v", name, err)
		}
	}
}

func TestValidateCapName_Invalid(t *testing.T) {
	cases := []struct {
		name   string
		reason string
	}{
		{"browser_page_read", "lowercase segments"},
		{"BrowserPageRead", "no underscores"},
		{"Browser_Page", "only two segments"},
		{"Browser_Page_Read_Extra", "four segments"},
		{"_Browser_Page_Read", "leading underscore"},
		{"", "empty"},
		{"Spreadsheet_Row_List_Google", "four segments"},
	}
	for _, tc := range cases {
		if err := validateCapName(tc.name); err == nil {
			t.Errorf("validateCapName(%q) should fail (%s) but didn't", tc.name, tc.reason)
		}
	}
}

func TestValidateCapName_VendorTerms(t *testing.T) {
	// vendor-specific terms must be rejected even if the pattern matches
	vendor := []string{
		"Gmail_Message_Read",
		"Google_Sheet_Write",
		"Slack_Channel_Post",
		"Drive_File_Read",
	}
	for _, name := range vendor {
		if err := validateCapName(name); err == nil {
			t.Errorf("validateCapName(%q) should reject vendor term but didn't", name)
		}
	}
}

// ---- validateJSONKey -------------------------------------------------------

func TestValidateJSONKey_Valid(t *testing.T) {
	for _, key := range []string{"url", "max_chars", "wait_for", "path", "content"} {
		if err := validateJSONKey(key); err != nil {
			t.Errorf("validateJSONKey(%q) unexpected error: %v", key, err)
		}
	}
}

func TestValidateJSONKey_Invalid(t *testing.T) {
	for _, key := range []string{"MaxChars", "max-chars", "MAX_CHARS", "max chars", ""} {
		if err := validateJSONKey(key); err == nil {
			t.Errorf("validateJSONKey(%q) should fail but didn't", key)
		}
	}
}

// ---- generateSchema --------------------------------------------------------

// writeTempGoFile writes a Go source file into dir and returns its path.
func writeTempGoFile(t *testing.T, dir, filename, src string) string {
	t.Helper()
	path := filepath.Join(dir, filename)
	if err := os.WriteFile(path, []byte(src), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	return path
}

func TestGenerateSchema_BasicStruct(t *testing.T) {
	dir := t.TempDir()
	src := `package capabilities

// TestBasicReadArgs is a test capability struct.
type TestBasicReadArgs struct {
	URL     string ` + "`" + `json:"url" description:"The URL to fetch."` + "`" + `
	MaxLen  int    ` + "`" + `json:"max_len,omitempty" description:"Maximum length."` + "`" + `
}
`
	inFile := writeTempGoFile(t, dir, "basic.go", src)

	schema, err := generateSchema(inFile, "TestBasicReadArgs", "Test_Basic_Read")
	if err != nil {
		t.Fatalf("generateSchema: %v", err)
	}

	// Must be valid JSON.
	var parsed map[string]any
	if err := json.Unmarshal([]byte(schema), &parsed); err != nil {
		t.Fatalf("schema is not valid JSON: %v\n%s", err, schema)
	}
	if parsed["title"] != "Test_Basic_Read" {
		t.Errorf("title = %v, want Test_Basic_Read", parsed["title"])
	}
	props, _ := parsed["properties"].(map[string]any)
	if _, ok := props["url"]; !ok {
		t.Error("schema missing 'url' property")
	}
	if _, ok := props["max_len"]; !ok {
		t.Error("schema missing 'max_len' property")
	}
	// url has no omitempty → required
	req, _ := parsed["required"].([]any)
	hasURL := false
	for _, r := range req {
		if r == "url" {
			hasURL = true
		}
	}
	if !hasURL {
		t.Error("'url' should be in required (no omitempty)")
	}
}

func TestGenerateSchema_MissingDescription(t *testing.T) {
	dir := t.TempDir()
	src := `package capabilities

type NodescArgs struct {
	URL string ` + "`" + `json:"url"` + "`" + ` // no description tag
}
`
	inFile := writeTempGoFile(t, dir, "nodesc.go", src)
	_, err := generateSchema(inFile, "NodescArgs", "Test_Nodesc_Read")
	if err == nil {
		t.Error("generateSchema should fail for missing 'description' tag")
	}
	if !strings.Contains(err.Error(), "description") {
		t.Errorf("error should mention 'description', got: %v", err)
	}
}

func TestGenerateSchema_BadJSONKey(t *testing.T) {
	dir := t.TempDir()
	src := `package capabilities

type BadKeyArgs struct {
	URL string ` + "`" + `json:"BadURL" description:"bad key"` + "`" + `
}
`
	inFile := writeTempGoFile(t, dir, "badkey.go", src)
	_, err := generateSchema(inFile, "BadKeyArgs", "Test_Badkey_Read")
	if err == nil {
		t.Error("generateSchema should fail for non-snake_case JSON key")
	}
}

func TestGenerateSchema_StructNotFound(t *testing.T) {
	dir := t.TempDir()
	src := `package capabilities
type OtherArgs struct {}
`
	inFile := writeTempGoFile(t, dir, "missing.go", src)
	_, err := generateSchema(inFile, "NonExistentArgs", "Test_Missing_Read")
	if err == nil {
		t.Error("generateSchema should fail when struct not found")
	}
}

func TestGenerateSchema_EnumAndDefault(t *testing.T) {
	dir := t.TempDir()
	src := `package capabilities

type EnumArgs struct {
	WaitFor string ` + "`" + `json:"wait_for,omitempty" description:"Wait strategy." enum:"networkidle,load,domcontentloaded" default:"networkidle"` + "`" + `
}
`
	inFile := writeTempGoFile(t, dir, "enum.go", src)
	schema, err := generateSchema(inFile, "EnumArgs", "Test_Enum_Wait")
	if err != nil {
		t.Fatalf("generateSchema: %v", err)
	}
	var parsed map[string]any
	_ = json.Unmarshal([]byte(schema), &parsed)
	props, _ := parsed["properties"].(map[string]any)
	waitFor, _ := props["wait_for"].(map[string]any)
	if waitFor["enum"] == nil {
		t.Error("expected 'enum' field in wait_for property")
	}
	if waitFor["default"] == nil {
		t.Error("expected 'default' field in wait_for property")
	}
}

// ---- checkBackwardCompat ---------------------------------------------------

func TestBackwardCompat_NoChange(t *testing.T) {
	prev := `{"properties":{"url":{},"max_len":{}}}`
	next := `{"properties":{"url":{},"max_len":{},"new_field":{}}}`
	if err := checkBackwardCompat([]byte(prev), []byte(next)); err != nil {
		t.Errorf("no breaking change, but got error: %v", err)
	}
}

func TestBackwardCompat_RemovedField(t *testing.T) {
	prev := `{"properties":{"url":{},"max_len":{}}}`
	next := `{"properties":{"url":{}}}` // max_len removed
	if err := checkBackwardCompat([]byte(prev), []byte(next)); err == nil {
		t.Error("removed field should fail backward-compat check")
	}
}

func TestBackwardCompat_RenamedField(t *testing.T) {
	prev := `{"properties":{"max_len":{}}}`
	next := `{"properties":{"max_chars":{}}}` // renamed
	if err := checkBackwardCompat([]byte(prev), []byte(next)); err == nil {
		t.Error("renamed field should fail backward-compat check")
	}
}

func TestBackwardCompat_AddedFieldOnly(t *testing.T) {
	prev := `{"properties":{"url":{}}}`
	next := `{"properties":{"url":{},"extra":{}}}` // only added
	if err := checkBackwardCompat([]byte(prev), []byte(next)); err != nil {
		t.Errorf("adding fields is allowed, got error: %v", err)
	}
}

func TestBackwardCompat_EmptyPrev(t *testing.T) {
	prev := `{"properties":{}}`
	next := `{"properties":{"url":{}}}`
	if err := checkBackwardCompat([]byte(prev), []byte(next)); err != nil {
		t.Errorf("empty prev schema should always pass, got: %v", err)
	}
}
