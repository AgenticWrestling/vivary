package capabilities

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

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

	// Scope check: URL must match the ECS-style constraints from the ACL.
	constraints := ConstraintsFromContext(ctx)
	if !urlMatchesConstraints(args.URL, constraints) {
		return DeniedResponse(fmt.Sprintf("URL %q not in browser whitelist", args.URL)), nil
	}

	if b.ChromeProxy == nil {
		return Response{OK: false, ErrorCode: "unavailable", ErrorDetail: "chrome proxy not initialised"}, nil
	}

	policy := buildBrowserWhitelistPolicy(constraints)

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
	defer cancel()
	text, err := b.ChromeProxy(timeoutCtx, req.AgentID, args.URL, policy, args.WaitFor, 32768)
	if err != nil {
		return Response{OK: false, ErrorCode: "chrome_error", ErrorDetail: err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"text": text})
	return Response{OK: true, Data: data}, nil
}

func buildBrowserWhitelistPolicy(constraints []ScopeConstraint) chromproxy.WhitelistPolicy {
	policy := chromproxy.WhitelistPolicy{}
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
