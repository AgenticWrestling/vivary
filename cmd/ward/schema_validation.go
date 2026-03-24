package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

type schemaDoc struct {
	Type                 string                    `json:"type"`
	Required             []string                  `json:"required"`
	AdditionalProperties any                       `json:"additionalProperties"`
	Properties           map[string]schemaProperty `json:"properties"`
}

type schemaProperty struct {
	Type string   `json:"type"`
	Enum []string `json:"enum"`
}

func validateToolArgs(capability string, args json.RawMessage) error {
	schema, ok := capRegistry[capability]
	if !ok || strings.TrimSpace(schema) == "" {
		return nil
	}

	var doc schemaDoc
	if err := json.Unmarshal([]byte(schema), &doc); err != nil {
		return fmt.Errorf("schema parse failed for %s: %w", capability, err)
	}

	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	var raw map[string]json.RawMessage
	if err := dec.Decode(&raw); err != nil {
		return fmt.Errorf("args must satisfy %s schema: %w", capability, err)
	}

	if doc.Type != "" && doc.Type != "object" {
		return nil
	}

	required := make(map[string]struct{}, len(doc.Required))
	for _, name := range doc.Required {
		required[name] = struct{}{}
		if _, ok := raw[name]; !ok {
			return fmt.Errorf("args must satisfy %s schema: missing required field %q", capability, name)
		}
	}

	if deniesAdditionalProperties(doc.AdditionalProperties) {
		for name := range raw {
			if _, ok := doc.Properties[name]; !ok {
				return fmt.Errorf("args must satisfy %s schema: unknown field %q", capability, name)
			}
		}
	}

	for name, prop := range doc.Properties {
		value, ok := raw[name]
		if !ok {
			continue
		}
		if err := validateSchemaProperty(name, value, prop); err != nil {
			return fmt.Errorf("args must satisfy %s schema: %w", capability, err)
		}
	}

	return nil

}

func deniesAdditionalProperties(v any) bool {
	b, ok := v.(bool)
	return ok && !b
}

func validateSchemaProperty(name string, value json.RawMessage, prop schemaProperty) error {
	if err := validateSchemaType(name, value, prop.Type); err != nil {
		return err
	}
	if len(prop.Enum) == 0 {
		return nil
	}
	var got string
	if err := json.Unmarshal(value, &got); err != nil {
		return fmt.Errorf("field %q must be one of %s", name, formatEnum(prop.Enum))
	}
	for _, want := range prop.Enum {
		if got == want {
			return nil
		}
	}
	return fmt.Errorf("field %q must be one of %s", name, formatEnum(prop.Enum))
}

func validateSchemaType(name string, value json.RawMessage, typ string) error {
	switch typ {
	case "", "any":
		return nil
	case "string":
		var v string
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("field %q must be a string", name)
		}
	case "integer":
		var v int64
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("field %q must be an integer", name)
		}
	case "number":
		var v float64
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("field %q must be a number", name)
		}
	case "boolean":
		var v bool
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("field %q must be a boolean", name)
		}
	case "array":
		var v []json.RawMessage
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("field %q must be an array", name)
		}
	case "object":
		var v map[string]json.RawMessage
		if err := json.Unmarshal(value, &v); err != nil {
			return fmt.Errorf("field %q must be an object", name)
		}
	}
	return nil
}

func formatEnum(values []string) string {
	quoted := make([]string, len(values))
	copy(quoted, values)
	sort.Strings(quoted)
	for i, v := range quoted {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return strings.Join(quoted, ", ")
}
