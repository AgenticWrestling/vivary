package main

import (
	"strings"
	"testing"

	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/pkg/mus"
)

func TestValidateToolArgs(t *testing.T) {
	tests := []struct {
		name       string
		capability string
		args       []byte
		wantErr    string
	}{
		{
			name:       "valid browser read",
			capability: capabilities.BrowserPageReadName,
			args:       (&capabilities.Browser_Page_Read{URL: "https://example.com", Timeout: 10, WaitFor: "load"}).MarshalMUS(),
		},
		{
			name:       "truncated payload",
			capability: capabilities.BrowserPageReadName,
			args:       mus.AppendString(nil, "https://example.com"),
			wantErr:    `args must satisfy Browser_Page_Read schema`,
		},
		{
			name:       "trailing bytes",
			capability: capabilities.BrowserPageReadName,
			args:       append((&capabilities.Browser_Page_Read{URL: "https://example.com"}).MarshalMUS(), 0xff),
			wantErr:    `trailing payload bytes`,
		},
		{
			name:       "valid filesystem write",
			capability: capabilities.FilesystemFileWriteName,
			args:       (&capabilities.Filesystem_File_Write{Path: "out.txt", Content: "hello", Append: true, Encoding: "utf-8"}).MarshalMUS(),
		},
		{
			name:       "unknown capability",
			capability: "Nonexistent_Cap",
			args:       []byte("anything"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToolArgs(tt.capability, tt.args)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateToolArgs() unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateToolArgs() expected error, got nil")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("validateToolArgs() error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}
