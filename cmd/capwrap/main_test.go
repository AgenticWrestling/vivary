package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/pkg/mus"
)

func TestBuildArgumentPayload(t *testing.T) {
	tests := []struct {
		name    string
		capName string
		argv    []string
		check   func(t *testing.T, payload []byte)
		wantErr string
	}{
		{
			name:    "browser read valid",
			capName: capabilities.BrowserPageReadName,
			argv:    []string{"--url", "https://example.com", "--timeout", "10", "--wait_for", "load"},
			check: func(t *testing.T, payload []byte) {
				t.Helper()
				var args capabilities.Browser_Page_Read
				if err := args.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if args.URL != "https://example.com" || args.Timeout != 10 || args.WaitFor != "load" {
					t.Fatalf("decoded args = %+v", args)
				}
			},
		},
		{
			name:    "filesystem append shorthand bool",
			capName: capabilities.FilesystemFileWriteName,
			argv:    []string{"--path", "out.txt", "--content", "hello", "--append"},
			check: func(t *testing.T, payload []byte) {
				t.Helper()
				var args capabilities.Filesystem_File_Write
				if err := args.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if !args.Append || args.Path != "out.txt" || args.Content != "hello" {
					t.Fatalf("decoded args = %+v", args)
				}
			},
		},
		{
			name:    "missing required field",
			capName: capabilities.BrowserPageReadName,
			argv:    []string{"--timeout", "5"},
			wantErr: `missing required field "url"`,
		},
		{
			name:    "unknown field",
			capName: capabilities.FilesystemFileWriteName,
			argv:    []string{"--path", "out.txt", "--content", "hello", "--extra", "true"},
			wantErr: `unknown field "extra"`,
		},
		{
			name:    "enum mismatch",
			capName: capabilities.BrowserPageReadName,
			argv:    []string{"--url", "https://example.com", "--wait_for", "soon"},
			wantErr: `field "wait_for" must be one of`,
		},
		{
			name:    "bool mismatch",
			capName: capabilities.FilesystemFileWriteName,
			argv:    []string{"--path", "out.txt", "--content", "hello", "--append", "maybe"},
			wantErr: `field "append" must be a boolean`,
		},
		{
			name:    "nested list valid json",
			capName: "Spreadsheet_Range_Update",
			argv:    []string{"--spreadsheet_id", "sheet-1", "--range", "A1:B2", "--values", `[["a","b"],["c","d"]]`},
			check: func(t *testing.T, payload []byte) {
				t.Helper()
				var args capabilities.Spreadsheet_Range_Update
				if err := args.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if len(args.Values) != 2 || len(args.Values[0]) != 2 || args.Values[1][1] != "d" {
					t.Fatalf("decoded values = %#v", args.Values)
				}
			},
		},
		{
			name:    "list object valid json",
			capName: "Browser_Form_Submit",
			argv:    []string{"--url", "https://example.com", "--fields", `[{"selector":"#email","value":"user@example.com"}]`},
			check: func(t *testing.T, payload []byte) {
				t.Helper()
				var args capabilities.Browser_Form_Submit
				if err := args.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
					t.Fatalf("decode payload: %v", err)
				}
				if len(args.Fields) != 1 {
					t.Fatalf("decoded fields = %#v", args.Fields)
				}
			},
		},
		{
			name:    "nested list invalid shape",
			capName: "Spreadsheet_Range_Update",
			argv:    []string{"--spreadsheet_id", "sheet-1", "--range", "A1:B2", "--values", `{"not":"an-array"}`},
			wantErr: `field "values" must be a JSON array`,
		},
		{
			name:    "list object invalid shape",
			capName: "Browser_Form_Submit",
			argv:    []string{"--url", "https://example.com", "--fields", `["bad"]`},
			wantErr: `field "fields" must be an array of JSON objects`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload, err := buildArgumentPayload(tt.capName, tt.argv)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("buildArgumentPayload() unexpected error: %v", err)
				}
				if tt.check != nil {
					tt := tt
					tt.check(t, payload)
				}
				return
			}
			if err == nil {
				t.Fatal("buildArgumentPayload() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("buildArgumentPayload() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestFormatResult(t *testing.T) {
	t.Run("string result", func(t *testing.T) {
		out, err := formatResult(capabilities.BrowserPageReadName, mus.AppendString(nil, "page text"))
		if err != nil {
			t.Fatal(err)
		}
		if out != "page text" {
			t.Fatalf("out = %q", out)
		}
	})

	t.Run("object result", func(t *testing.T) {
		payload := (&capabilities.Filesystem_File_Write_Result{Path: "/tmp/out.txt", BytesWritten: 5}).MarshalMUS()
		out, err := formatResult(capabilities.FilesystemFileWriteName, payload)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal([]byte(out), &decoded); err != nil {
			t.Fatalf("unmarshal output: %v", err)
		}
		if decoded["path"] != "/tmp/out.txt" {
			t.Fatalf("decoded path = %#v", decoded["path"])
		}
	})
}
