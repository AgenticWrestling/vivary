package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Filesystem_File_Write writes content to a file within the agent's allowed
// output directory.
//
// Scope: the ACL scope string is the absolute host path to the allowed write
// prefix (e.g. "/var/lib/vivary/agents/agent-1/output").
// Requests for paths outside that prefix, absolute paths, paths containing
// ".." components, or paths that resolve to symlinks targeting outside the
// prefix are all denied.

const FilesystemFileWriteName = "Filesystem_File_Write"

const filesystemFileWriteSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Filesystem_File_Write",
  "description": "Write text or binary content to a file in the agent's output directory.",
  "type": "object",
  "required": ["path", "content"],
  "properties": {
    "path": {
      "type": "string",
      "description": "Relative path within the agent output directory.  Must not start with '/' or contain '..'.",
      "examples": ["results/report.txt", "data/out.json"]
    },
    "content": {
      "type": "string",
      "description": "UTF-8 text content to write.  Binary data should be base64-encoded.",
      "examples": ["Hello, world!"]
    },
    "append": {
      "type": "boolean",
      "description": "If true, append to an existing file instead of overwriting.",
      "default": false
    }
  },
  "additionalProperties": false
}`

// FilesystemFileWriteArgs is the decoded argument struct.
type FilesystemFileWriteArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Append  bool   `json:"append,omitempty"`
}

// FilesystemFileWrite implements Capability for Filesystem_File_Write.
type FilesystemFileWrite struct{}

func (f *FilesystemFileWrite) Name() string      { return FilesystemFileWriteName }
func (f *FilesystemFileWrite) Explain() string   { return filesystemFileWriteSchema }
func (f *FilesystemFileWrite) AuditPayload() bool { return true }

func (f *FilesystemFileWrite) Execute(ctx context.Context, req Request) (Response, error) {
	var args FilesystemFileWriteArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return DeniedResponse("args schema mismatch: " + err.Error()), nil
	}

	// Basic structural validation before touching the filesystem.
	if args.Path == "" {
		return DeniedResponse("path is required"), nil
	}
	if filepath.IsAbs(args.Path) {
		return DeniedResponse("absolute paths are not allowed"), nil
	}
	if containsDotDot(args.Path) {
		return DeniedResponse("path traversal via '..' is not allowed"), nil
	}

	// Scope check: the ACL scope is the allowed prefix root.
	scope := ScopeFromContext(ctx)
	if scope == "" {
		return DeniedResponse("no filesystem write scope configured for this agent"), nil
	}

	// Resolve the target path and check it is strictly under scope.
	target := filepath.Join(scope, args.Path)
	clean := filepath.Clean(target)
	if !strings.HasPrefix(clean, filepath.Clean(scope)+string(filepath.Separator)) {
		return DeniedResponse(fmt.Sprintf("resolved path %q escapes allowed prefix %q", clean, scope)), nil
	}

	// Symlink escape check: walk every path component and ensure no component is
	// a symlink that would redirect outside the scope root.
	if err := checkNoSymlinkEscape(scope, clean); err != nil {
		return DeniedResponse("symlink escape detected: " + err.Error()), nil
	}

	// Create parent directories (within scope).
	if err := os.MkdirAll(filepath.Dir(clean), 0o750); err != nil {
		return Response{OK: false, ErrorCode: "io_error", ErrorDetail: err.Error()}, nil
	}

	flag := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if args.Append {
		flag = os.O_WRONLY | os.O_CREATE | os.O_APPEND
	}
	fh, err := os.OpenFile(clean, flag, 0o640)
	if err != nil {
		return Response{OK: false, ErrorCode: "io_error", ErrorDetail: err.Error()}, nil
	}
	defer fh.Close()

	if _, err := fh.WriteString(args.Content); err != nil {
		return Response{OK: false, ErrorCode: "io_error", ErrorDetail: err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"path": clean, "status": "written"})
	return Response{OK: true, Data: data}, nil
}

// containsDotDot returns true if any path element is "..".
func containsDotDot(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// checkNoSymlinkEscape walks clean from the scope root to the target, and
// returns an error if any path component is a symlink that resolves outside
// the scope prefix.
func checkNoSymlinkEscape(scope, clean string) error {
	// Strip the scope prefix to get the relative portion.
	rel, err := filepath.Rel(scope, clean)
	if err != nil {
		return err
	}
	parts := strings.Split(rel, string(filepath.Separator))
	cur := scope
	for _, p := range parts {
		cur = filepath.Join(cur, p)
		fi, err := os.Lstat(cur)
		if os.IsNotExist(err) {
			// Path does not exist yet — no symlink to check.
			break
		}
		if err != nil {
			return err
		}
		if fi.Mode()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(cur)
			if err != nil {
				return fmt.Errorf("could not resolve symlink %q: %w", cur, err)
			}
			if !strings.HasPrefix(resolved, filepath.Clean(scope)+string(filepath.Separator)) {
				return fmt.Errorf("symlink %q → %q escapes scope", cur, resolved)
			}
		}
	}
	return nil
}
