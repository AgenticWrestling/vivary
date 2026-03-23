package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"vivary.dev/vivary/internal/chromproxy"
)

// Browser_Page_Read navigates headless Chrome to a URL and returns the page's
// readable text via the accessibility tree (not raw HTML).
//
// The Ward invokes this as: Browser_Page_Read --url <url>
// keeperd's whitelisting proxy enforces the agent's browser.whitelist before
// forwarding any CDP verb to the Chrome remote debugging port.

const BrowserPageReadName = "Browser_Page_Read"

// BrowserPageRead implements Capability for Browser_Page_Read.
type BrowserPageRead struct {
	// ChromeProxy dispatches CDP read requests after scope validation.
	// Signature: func(ctx context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error)
	ChromeProxy func(ctx context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error)
}

func (b *BrowserPageRead) Name() string       { return BrowserPageReadName }
func (b *BrowserPageRead) Explain() string    { return Browser_Page_ReadSchema }
func (b *BrowserPageRead) AuditPayload() bool { return true }

func (b *BrowserPageRead) Execute(ctx context.Context, req Request) (Response, error) {
	var args Browser_Page_Read
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return DeniedResponse("args schema mismatch: " + err.Error()), nil
	}
	if args.URL == "" {
		return DeniedResponse("url is required"), nil
	}
	if args.WaitFor == "" {
		args.WaitFor = "networkidle"
	}
	if args.Timeout == 0 {
		args.Timeout = 30
	}

	// Scope check: URL must match one of the whitelist prefixes from the ACL.
	// Legacy string scope check.
	scope := ScopeFromContext(ctx)
	if !urlMatchesScope(args.URL, scope) {
		// Also check ECS-style constraints.
		constraints := ConstraintsFromContext(ctx)
		if !urlMatchesConstraints(args.URL, constraints) {
			return DeniedResponse(fmt.Sprintf("URL %q not in browser whitelist", args.URL)), nil
		}
	}

	if b.ChromeProxy == nil {
		return Response{OK: false, ErrorCode: "unavailable", ErrorDetail: "chrome proxy not initialised"}, nil
	}

	policy := buildBrowserWhitelistPolicy(scope, ConstraintsFromContext(ctx))

	// TODO: use args.Timeout
	text, err := b.ChromeProxy(ctx, req.AgentID, args.URL, policy, args.WaitFor, 32768)
	if err != nil {
		return Response{OK: false, ErrorCode: "chrome_error", ErrorDetail: err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"text": text})
	return Response{OK: true, Data: data}, nil
}

func buildBrowserWhitelistPolicy(scope string, constraints []ScopeConstraint) chromproxy.WhitelistPolicy {
	policy := chromproxy.WhitelistPolicy{}
	if scope != "" {
		for _, prefix := range strings.Split(scope, ",") {
			prefix = strings.TrimSpace(prefix)
			if prefix != "" {
				policy.Prefixes = append(policy.Prefixes, prefix)
			}
		}
	}
	for _, sc := range constraints {
		if sc.Entity != "Link" {
			continue
		}
		policy.Domains = append(policy.Domains, sc.Constraints["domain"]...)
		policy.DomainSuffixes = append(policy.DomainSuffixes, sc.Constraints["domain-suffix"]...)
		policy.PathPrefixes = append(policy.PathPrefixes, sc.Constraints["path-prefix"]...)
	}
	return policy
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
		if p == "" || !strings.HasPrefix(needle, p) {
			continue
		}
		// Guard against subdomain confusion: after matching the prefix the
		// very next character (if any) must be a path/query/fragment separator,
		// not a continuation of the hostname (e.g. "example.com.evil.com").
		// If the prefix itself already ends with a separator we are fine.
		rest := needle[len(p):]
		lastOfP := p[len(p)-1]
		if rest == "" || rest[0] == '/' || rest[0] == '?' || rest[0] == '#' ||
			lastOfP == '/' || lastOfP == '?' || lastOfP == '#' {
			return true
		}
	}
	return false
}

// urlMatchesConstraints returns true if rawURL matches any of the ECS-style
// scope constraints.
func urlMatchesConstraints(rawURL string, constraints []ScopeConstraint) bool {
	if len(constraints) == 0 {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	domain := strings.ToLower(u.Host)
	path := u.Path

	for _, sc := range constraints {
		if sc.Entity != "Link" {
			continue
		}
		// Check 'domain' constraint.
		if ds, ok := sc.Constraints["domain"]; ok {
			for _, d := range ds {
				if domain == strings.ToLower(d) {
					return true
				}
			}
		}
		// Check 'domain-suffix' constraint.
		if ds, ok := sc.Constraints["domain-suffix"]; ok {
			for _, s := range ds {
				suffix := strings.ToLower(s)
				if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
					return true
				}
			}
		}
		// Check 'path-prefix' constraint.
		if ps, ok := sc.Constraints["path-prefix"]; ok {
			for _, p := range ps {
				if strings.HasPrefix(path, p) {
					return true
				}
			}
		}
	}
	return false
}
