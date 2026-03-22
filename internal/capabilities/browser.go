package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// Browser_Page_Read navigates headless Chrome to a URL and returns the page's
// readable text via the accessibility tree (not raw HTML).
//
// The Ward invokes this as: Browser_Page_Read --url <url>
// keeperd's whitelisting proxy enforces the agent's browser.whitelist before
// forwarding any CDP verb to the Chrome remote debugging port.
//
// Scope: the ACL scope string for this capability is a comma-separated list of
// allowed URL prefixes.  An empty scope means no URLs are permitted.

const BrowserPageReadName = "Browser_Page_Read"

const browserPageReadSchema = `{
  "$schema": "http://json-schema.org/draft-07/schema#",
  "title": "Browser_Page_Read",
  "description": "Navigate to a URL and return the readable text of the page via the accessibility tree.",
  "type": "object",
  "required": ["url"],
  "properties": {
    "url": {
      "type": "string",
      "description": "The fully-qualified HTTPS URL to load.",
      "examples": ["https://example.com/docs"]
    },
    "wait_for": {
      "type": "string",
      "description": "Wait strategy before extracting text.",
      "enum": ["networkidle", "domcontentloaded", "load"],
      "default": "networkidle"
    },
    "max_chars": {
      "type": "integer",
      "description": "Truncate extracted text to this many characters (default 32768).",
      "default": 32768,
      "minimum": 1,
      "maximum": 262144
    }
  },
  "additionalProperties": false
}`

// BrowserPageReadArgs is the decoded argument struct for Browser_Page_Read.
type BrowserPageReadArgs struct {
	URL      string `json:"url"`
	WaitFor  string `json:"wait_for,omitempty"`
	MaxChars int    `json:"max_chars,omitempty"`
}

// BrowserPageRead implements Capability for Browser_Page_Read.
// The ChromeProxy field must be set to a real proxy before use; in tests it can
// be replaced with a stub.
type BrowserPageRead struct {
	// ChromeProxy dispatches CDP read requests after scope validation.
	// Signature: func(ctx context.Context, agentID, targetURL, waitFor string, maxChars int) (string, error)
	ChromeProxy func(ctx context.Context, agentID, targetURL, waitFor string, maxChars int) (string, error)
}

func (b *BrowserPageRead) Name() string        { return BrowserPageReadName }
func (b *BrowserPageRead) Explain() string      { return browserPageReadSchema }
func (b *BrowserPageRead) AuditPayload() bool   { return true }

func (b *BrowserPageRead) Execute(ctx context.Context, req Request) (Response, error) {
	var args BrowserPageReadArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return DeniedResponse("args schema mismatch: " + err.Error()), nil
	}
	if args.URL == "" {
		return DeniedResponse("url is required"), nil
	}
	if args.WaitFor == "" {
		args.WaitFor = "networkidle"
	}
	if args.MaxChars == 0 {
		args.MaxChars = 32768
	}

	// Scope check: URL must match one of the whitelist prefixes from the ACL.
	scope := ScopeFromContext(ctx)
	if !urlMatchesScope(args.URL, scope) {
		return DeniedResponse(fmt.Sprintf("URL %q not in browser whitelist", args.URL)), nil
	}

	if b.ChromeProxy == nil {
		return Response{OK: false, ErrorCode: "unavailable", ErrorDetail: "chrome proxy not initialised"}, nil
	}

	text, err := b.ChromeProxy(ctx, req.AgentID, args.URL, args.WaitFor, args.MaxChars)
	if err != nil {
		return Response{OK: false, ErrorCode: "chrome_error", ErrorDetail: err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"text": text})
	return Response{OK: true, Data: data}, nil
}

// urlMatchesScope returns true if rawURL starts with any of the comma-separated
// whitelist prefixes in scope.  Both URL and prefix are normalised to lowercase.
func urlMatchesScope(rawURL, scope string) bool {
	if scope == "" {
		return false
	}
	// Validate that the supplied URL is well-formed before any prefix match.
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	needle := strings.ToLower(rawURL)
	for _, prefix := range strings.Split(scope, ",") {
		p := strings.TrimSpace(strings.ToLower(prefix))
		if p != "" && strings.HasPrefix(needle, p) {
			return true
		}
	}
	return false
}
