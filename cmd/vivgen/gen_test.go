package main

import "testing"

func TestGenerateSchemaForCapability_Basic(t *testing.T) {
	min := 1.0
	max := 120.0
	cap := kdlCapability{
		Name:        "Web_Page_Read",
		Description: "Fetch a page",
		Fields: []kdlField{
			{Name: "url", Type: "string", Required: true, Description: "URL to fetch", Example: "https://example.com"},
			{Name: "timeout", Type: "int", Required: false, Description: "Timeout seconds", Default: int64(30), Min: &min, Max: &max},
		},
	}

	schema := generateSchemaForCapability(cap)
	if schema.Title != "Web_Page_Read" {
		t.Fatalf("title = %q, want Web_Page_Read", schema.Title)
	}
	if schema.Description != "Fetch a page" {
		t.Fatalf("description = %q", schema.Description)
	}
	if schema.Type != "object" {
		t.Fatalf("type = %q, want object", schema.Type)
	}
	if schema.AdditionalProperties {
		t.Fatal("additionalProperties should be false")
	}
	if len(schema.Required) != 1 || schema.Required[0] != "url" {
		t.Fatalf("required = %#v, want [url]", schema.Required)
	}
	if schema.Properties["url"].Description != "URL to fetch" {
		t.Fatalf("url description = %q", schema.Properties["url"].Description)
	}
	if len(schema.Properties["url"].Examples) != 1 || schema.Properties["url"].Examples[0] != "https://example.com" {
		t.Fatalf("url examples = %#v", schema.Properties["url"].Examples)
	}
	if schema.Properties["timeout"].Default != int64(30) {
		t.Fatalf("timeout default = %#v", schema.Properties["timeout"].Default)
	}
	if schema.Properties["timeout"].Minimum == nil || *schema.Properties["timeout"].Minimum != 1 {
		t.Fatalf("timeout minimum = %#v, want 1", schema.Properties["timeout"].Minimum)
	}
	if schema.Properties["timeout"].Maximum == nil || *schema.Properties["timeout"].Maximum != 120 {
		t.Fatalf("timeout maximum = %#v, want 120", schema.Properties["timeout"].Maximum)
	}
}

func TestGenerateSchemaForCapability_EnumAndDefault(t *testing.T) {
	cap := kdlCapability{
		Name: "Web_Page_Read",
		Fields: []kdlField{{
			Name:        "wait_for",
			Type:        "string",
			Description: "Wait strategy",
			Enum:        []string{"load", "domcontentloaded", "networkidle"},
			Default:     "networkidle",
		}},
	}

	schema := generateSchemaForCapability(cap)
	field := schema.Properties["wait_for"]
	if len(field.Enum) != 3 {
		t.Fatalf("enum len = %d, want 3", len(field.Enum))
	}
	if field.Default != "networkidle" {
		t.Fatalf("default = %#v", field.Default)
	}
}

func TestKDLTypeToJSONType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"string", "string"},
		{"int", "integer"},
		{"float", "number"},
		{"bool", "boolean"},
		{"object", "object"},
		{"list<string>", "array"},
		{"mystery", "string"},
	} {
		if got := kdlTypeToJSONType(tc.in); got != tc.want {
			t.Fatalf("kdlTypeToJSONType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestKDLTypeToGoType(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"string", "string"},
		{"int", "int"},
		{"float", "float64"},
		{"bool", "bool"},
		{"object", "json.RawMessage"},
		{"list<string>", "[]string"},
		{"list<int>", "[]int"},
		{"list<object>", "[]json.RawMessage"},
		{"mystery", "string"},
	} {
		if got := kdlTypeToGoType(tc.in); got != tc.want {
			t.Fatalf("kdlTypeToGoType(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSnakeToPascal(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want string
	}{
		{"url", "URL"},
		{"wait_for", "WaitFor"},
		{"max_results", "MaxResults"},
		{"canonical_url", "CanonicalURL"},
	} {
		if got := snakeToPascal(tc.in); got != tc.want {
			t.Fatalf("snakeToPascal(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
