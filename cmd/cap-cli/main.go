// cap-cli is the generic capability CLI binary deployed inside each nspawn
// container.  It is symlinked (or copied) once per registered capability name,
// e.g. Browser_Page_Read → /usr/local/bin/Browser_Page_Read.
//
// When invoked:
//   - With --help: prints the capability's JSON Schema to stdout and exits 0.
//   - Otherwise: connects to the Ward tool socket, sends the args as JSON,
//     reads the ToolResponse, and prints the result text to stdout (exit 0) or
//     prints the error to stderr (exit 1).
//
// The Ward tool socket path is read from $WARD_TOOL_SOCK (default
// /run/ward-tool.sock).
//
// The capability name is taken from os.Args[0] (argv[0]) so a single binary
// can serve multiple capabilities when symlinked under different names.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	capName := filepath.Base(os.Args[0])

	// --help: print JSON Schema from Ward and exit.
	if len(os.Args) > 1 && (os.Args[1] == "--help" || os.Args[1] == "-h") {
		schema, err := fetchSchema(capName)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: cannot fetch schema: %v\n", capName, err)
			os.Exit(1)
		}
		fmt.Println(schema)
		return
	}

	// Parse remaining flags as JSON key=value pairs.
	// All capabilities use --field_name value syntax matching their JSON schema.
	args := parseArgs(os.Args[1:])

	sockPath := os.Getenv("WARD_TOOL_SOCK")
	if sockPath == "" {
		sockPath = "/run/ward-tool.sock"
	}

	resp, err := callWard(sockPath, capName, args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", capName, err)
		os.Exit(1)
	}
	if !resp.OK {
		fmt.Fprintf(os.Stderr, "%s: %s: %s\n", capName, resp.ErrorCode, resp.ErrorDetail)
		os.Exit(1)
	}

	// Print result to stdout.  If data contains a "text" field, print that;
	// otherwise pretty-print the entire data JSON.
	if resp.Data != nil {
		var d map[string]json.RawMessage
		if err := json.Unmarshal(resp.Data, &d); err == nil {
			if textRaw, ok := d["text"]; ok {
				var text string
				if err := json.Unmarshal(textRaw, &text); err == nil {
					fmt.Print(text)
					return
				}
			}
		}
		out, _ := json.MarshalIndent(json.RawMessage(resp.Data), "", "  ")
		fmt.Println(string(out))
	}
}

// parseArgs converts a flat flag list like [--url https://... --max_chars 1024]
// into a map suitable for JSON marshalling.  Values are kept as strings;
// the capability's schema on the keeperd side handles type coercion.
func parseArgs(argv []string) map[string]any {
	result := make(map[string]any)
	fs := flag.NewFlagSet("cap", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	// Register a dynamic string var for every --key found in argv.
	// First pass: collect keys.
	var keys []string
	for i := 0; i < len(argv); i++ {
		arg := argv[i]
		if strings.HasPrefix(arg, "--") {
			key := strings.TrimPrefix(arg, "--")
			if eq := strings.Index(key, "="); eq >= 0 {
				keys = append(keys, key[:eq])
			} else {
				keys = append(keys, key)
			}
		}
	}

	vals := make(map[string]*string, len(keys))
	for _, k := range keys {
		v := ""
		vals[k] = &v
		fs.StringVar(&v, k, "", "")
	}
	_ = fs.Parse(argv)

	for k, v := range vals {
		result[k] = *v
	}
	// Positional args land in fs.Args() — ignore for now.
	return result
}

// callWard connects to the Ward tool socket and sends the capability request.
func callWard(sockPath, capName string, args map[string]any) (*toolResponse, error) {
	argsJSON, err := json.Marshal(args)
	if err != nil {
		return nil, fmt.Errorf("marshal args: %w", err)
	}

	req, _ := json.Marshal(map[string]any{
		"capability": capName,
		"args":       json.RawMessage(argsJSON),
	})

	conn, err := net.DialTimeout("unix", sockPath, 3*time.Second)
	if err != nil {
		return nil, fmt.Errorf("connect to Ward: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(60 * time.Second))

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	// Signal EOF on the write side so Ward knows the request is complete.
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}

	respBytes, err := io.ReadAll(conn)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	var resp toolResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return &resp, nil
}

// fetchSchema requests the JSON Schema for capName from the Ward tool socket.
func fetchSchema(capName string) (string, error) {
	return callWardSchema(os.Getenv("WARD_TOOL_SOCK"), capName)
}

func callWardSchema(sockPath, capName string) (string, error) {
	if sockPath == "" {
		sockPath = "/run/ward-tool.sock"
	}
	req, _ := json.Marshal(map[string]any{
		"capability": capName,
		"args":       json.RawMessage(`{"__schema_only":true}`),
	})
	conn, err := net.DialTimeout("unix", sockPath, 3*time.Second)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write(req); err != nil {
		return "", err
	}
	if uc, ok := conn.(*net.UnixConn); ok {
		_ = uc.CloseWrite()
	}
	respBytes, err := io.ReadAll(conn)
	if err != nil {
		return "", err
	}
	var resp toolResponse
	if err := json.Unmarshal(respBytes, &resp); err != nil {
		return "", err
	}
	if !resp.OK {
		return "", fmt.Errorf("%s: %s", resp.ErrorCode, resp.ErrorDetail)
	}
	var d map[string]json.RawMessage
	if err := json.Unmarshal(resp.Data, &d); err != nil {
		return "", err
	}
	schemaRaw, ok := d["schema"]
	if !ok {
		return "", fmt.Errorf("no schema in response")
	}
	var schema string
	_ = json.Unmarshal(schemaRaw, &schema)
	return schema, nil
}

type toolResponse struct {
	OK          bool            `json:"ok"`
	Data        json.RawMessage `json:"data,omitempty"`
	ErrorCode   string          `json:"error_code,omitempty"`
	ErrorDetail string          `json:"error_detail,omitempty"`
}
