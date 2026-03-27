package main

import (
	"encoding/json"
	"strings"
	"testing"

	"vivary.dev/vivary/internal/capabilities"
)

func TestValidateToolArgs(t *testing.T) {
	// Initialize the capability registry for tests.
	capRegistry = capabilities.GeneratedRegistry()

	tests := []struct {
		name       string
		capability string
		args       string
		wantErr    string
	}{
		{
			name:       "valid browser read",
			capability: "Browser_Page_Read",
			args:       `{"url":"https://example.com","timeout":10,"wait_for":"load"}`,
			wantErr:    "",
		},
		{
			name:       "missing required field",
			capability: "Browser_Page_Read",
			args:       `{"timeout":10}`,
			wantErr:    `missing required field "url"`,
		},
		{
			name:       "type mismatch integer for string",
			capability: "Browser_Page_Read",
			args:       `{"url":123}`,
			wantErr:    `field "url" must be a string`,
		},
		{
			name:       "type mismatch string for integer",
			capability: "Browser_Page_Read",
			args:       `{"url":"https://example.com","timeout":"fast"}`,
			wantErr:    `field "timeout" must be an integer`,
		},
		{
			name:       "enum mismatch",
			capability: "Browser_Page_Read",
			args:       `{"url":"https://example.com","wait_for":"immediately"}`,
			wantErr:    `field "wait_for" must be one of "domcontentloaded", "load", "networkidle"`,
		},
		{
			name:       "unknown field",
			capability: "Browser_Page_Read",
			args:       `{"url":"https://example.com","extra":"field"}`,
			wantErr:    `unknown field "extra"`,
		},
		{
			name:       "valid filesystem write",
			capability: "Filesystem_File_Write",
			args:       `{"path":"out.txt","content":"hello","append":true,"encoding":"utf-8"}`,
			wantErr:    "",
		},
		{
			name:       "bool type mismatch",
			capability: "Filesystem_File_Write",
			args:       `{"path":"out.txt","content":"hello","append":"yes"}`,
			wantErr:    `field "append" must be a boolean`,
		},
		{
			name:       "unknown capability (no schema)",
			capability: "Nonexistent_Cap",
			args:       `{"any":"args"}`,
			wantErr:    "", // Should pass if no schema is found
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateToolArgs(tt.capability, json.RawMessage(tt.args))
			if tt.wantErr == "" {
				if err != nil {
					t.Errorf("validateToolArgs() unexpected error: %v", err)
				}
			} else {
				if err == nil {
					t.Error("validateToolArgs() expected error, got nil")
				} else if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("validateToolArgs() error = %v, wantErr %v", err, tt.wantErr)
				}
			}
		})
	}
}
