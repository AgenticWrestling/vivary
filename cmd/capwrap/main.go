// capwrap is the generic capability wrapper binary deployed inside each nspawn
// container. It is symlinked (or copied) once per registered capability name,
// e.g. Browser_Page_Read -> /usr/bin/Browser_Page_Read.
//
// When invoked:
//   - With --help: prints the capability JSON schema to stdout and exits 0.
//   - Otherwise: parses CLI flags into a typed MUS argument payload, sends one
//     MUS ToolRequestPayload to Ward, reads one MUS CapabilityResponsePayload,
//     and prints the typed result to stdout.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/pkg/mus"
)

func main() {
	capName := filepath.Base(os.Args[0])

	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		schema, err := fetchSchema(capName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot fetch schema: %v\n", capName, err)
			os.Exit(1)
		}
		fmt.Println(schema)
		return
	}

	argPayload, err := buildArgumentPayload(capName, os.Args[1:])
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", capName, err)
		os.Exit(1)
	}

	resp, err := callWard(capabilities.ToolRequestPayload{
		Capability: capName,
		Mode:       capabilities.ToolRequestInvoke,
		Args:       argPayload,
	}, 60*time.Second)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", capName, err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "%s: %s: %s\n", capName, resp.ErrorCode, resp.ErrorDetail)
		os.Exit(1)
	}

	out, err := formatResult(capName, resp.Data)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: decode result: %v\n", capName, err)
		os.Exit(1)
	}
	if out != "" {
		fmt.Print(out)
	}
}

func buildArgumentPayload(capName string, argv []string) ([]byte, error) {
	info, ok := capabilities.GeneratedCapabilityInfoRegistry()[capName]
	if !ok {
		return nil, fmt.Errorf("capability %q not registered", capName)
	}
	rawArgs, err := parseArgs(argv)
	if err != nil {
		return nil, err
	}

	fieldsByName := make(map[string]capabilities.GeneratedField, len(info.Fields))
	for _, field := range info.Fields {
		fieldsByName[field.Name] = field
	}
	for name := range rawArgs {
		if _, ok := fieldsByName[name]; !ok {
			return nil, fmt.Errorf("unknown field %q", name)
		}
	}
	for _, field := range info.Fields {
		if field.Required {
			if _, ok := rawArgs[field.Name]; !ok {
				return nil, fmt.Errorf("missing required field %q", field.Name)
			}
		}
	}

	typed := make(map[string]any, len(rawArgs))
	for _, field := range info.Fields {
		raw, ok := rawArgs[field.Name]
		if !ok {
			continue
		}
		value, err := coerceFieldValue(field, raw)
		if err != nil {
			return nil, err
		}
		typed[field.Name] = value
	}

	jsonBytes, err := json.Marshal(typed)
	if err != nil {
		return nil, fmt.Errorf("marshal typed args: %w", err)
	}
	payload, ok := capabilities.NewArgumentPayload(capName)
	if !ok {
		return nil, fmt.Errorf("capability %q has no argument codec", capName)
	}
	if err := json.Unmarshal(jsonBytes, payload); err != nil {
		return nil, fmt.Errorf("decode typed args: %w", err)
	}
	return payload.MarshalMUS(), nil
}

func parseArgs(argv []string) (map[string]string, error) {
	result := make(map[string]string)
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if !strings.HasPrefix(arg, "--") {
			return nil, fmt.Errorf("unexpected positional argument %q", arg)
		}
		key := strings.TrimPrefix(arg, "--")
		value := ""
		if eq := strings.Index(key, "="); eq >= 0 {
			value = key[eq+1:]
			key = key[:eq]
		} else if i+1 < len(argv) && !strings.HasPrefix(argv[i+1], "--") {
			i++
			value = argv[i]
		} else {
			value = "true"
		}
		if key == "" {
			return nil, fmt.Errorf("empty flag name")
		}
		result[key] = value
	}
	return result, nil
}

func coerceFieldValue(field capabilities.GeneratedField, raw string) (any, error) {
	switch {
	case field.Type == "string":
		if len(field.Enum) > 0 && !contains(field.Enum, raw) {
			return nil, fmt.Errorf("field %q must be one of %s", field.Name, formatEnum(field.Enum))
		}
		return raw, nil
	case field.Type == "int" || field.Type == "integer":
		v, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q must be an integer", field.Name)
		}
		return v, nil
	case field.Type == "float" || field.Type == "number":
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("field %q must be a number", field.Name)
		}
		return v, nil
	case field.Type == "bool" || field.Type == "boolean":
		v, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("field %q must be a boolean", field.Name)
		}
		return v, nil
	case field.Type == "object":
		var obj map[string]any
		if err := json.Unmarshal([]byte(raw), &obj); err != nil {
			return nil, fmt.Errorf("field %q must be a valid JSON object", field.Name)
		}
		return json.RawMessage(raw), nil
	case strings.HasPrefix(field.Type, "list<"):
		if err := validateListField(field, raw); err != nil {
			return nil, err
		}
		return json.RawMessage(raw), nil
	default:
		return raw, nil
	}
}

func callWard(req capabilities.ToolRequestPayload, timeout time.Duration) (*capabilities.CapabilityResponsePayload, error) {
	sockPath := os.Getenv("WARD_TOOL_SOCK")
	if sockPath == "" {
		sockPath = "/run/ward-tool.sock"
	}

	conn, err := net.DialTimeout("unix", sockPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to Ward: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(timeout))

	if _, err := conn.Write(req.MarshalMUS()); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	respBytes, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp capabilities.CapabilityResponsePayload
	if err := resp.UnmarshalMUS(bytes.NewReader(respBytes)); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &resp, nil
}

func fetchSchema(capName string) (string, error) {
	resp, err := callWard(capabilities.ToolRequestPayload{
		Capability: capName,
		Mode:       capabilities.ToolRequestSchema,
	}, 5*time.Second)
	if err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s: %s", resp.ErrorCode, resp.ErrorDetail)
	}
	return decodeMUSString(resp.Data)
}

func formatResult(capName string, data []byte) (string, error) {
	info, ok := capabilities.GeneratedCapabilityInfoRegistry()[capName]
	if !ok {
		return decodeMUSString(data)
	}
	if info.ReturnType == "string" {
		return decodeMUSString(data)
	}
	jsonData, handled, err := capabilities.DecodeResultToJSON(capName, data)
	if err != nil {
		return "", err
	}
	if !handled {
		return decodeMUSString(data)
	}
	pretty, err := json.MarshalIndent(json.RawMessage(jsonData), "", "  ")
	if err != nil {
		return "", err
	}
	return string(pretty) + "\n", nil
}

func decodeMUSString(data []byte) (string, error) {
	return mus.ReadString(bytes.NewReader(data), mus.MaxPayloadBytes)
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func formatEnum(values []string) string {
	quoted := make([]string, len(values))
	for i, value := range values {
		quoted[i] = strconv.Quote(value)
	}
	return strings.Join(quoted, ", ")
}

func validateListField(field capabilities.GeneratedField, raw string) error {
	var decoded any
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		return fmt.Errorf("field %q must be a valid JSON array", field.Name)
	}
	inner := field.Type[5 : len(field.Type)-1]
	items, ok := decoded.([]any)
	if !ok {
		return fmt.Errorf("field %q must be a JSON array", field.Name)
	}
	for _, item := range items {
		if err := validateListElement(field.Name, inner, item); err != nil {
			return err
		}
	}
	return nil
}

func validateListElement(fieldName, typ string, value any) error {
	switch {
	case typ == "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("field %q must be an array of strings", fieldName)
		}
	case typ == "object":
		if _, ok := value.(map[string]any); !ok {
			return fmt.Errorf("field %q must be an array of JSON objects", fieldName)
		}
	case strings.HasPrefix(typ, "list<"):
		items, ok := value.([]any)
		if !ok {
			return fmt.Errorf("field %q must be a nested JSON array", fieldName)
		}
		inner := typ[5 : len(typ)-1]
		for _, item := range items {
			if err := validateListElement(fieldName, inner, item); err != nil {
				return err
			}
		}
	}
	return nil
}
