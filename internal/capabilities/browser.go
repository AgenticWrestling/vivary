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

type browserScope struct {
	domains        []string
	domainSuffixes []string
	pathPrefixes   []string
}

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

	constraints := ConstraintsFromContext(ctx)
	scope := normalizeBrowserScope(constraints)
	if !scope.allowsURL(args.URL) {
		return DeniedResponse(fmt.Sprintf("URL %q not in browser whitelist", args.URL)), nil
	}

	if b.ChromeProxy == nil {
		return Response{OK: false, ErrorCode: "unavailable", ErrorDetail: "chrome proxy not initialised"}, nil
	}

	policy := scope.whitelistPolicy()

	timeoutCtx, cancel := context.WithTimeout(ctx, time.Duration(args.Timeout)*time.Second)
	defer cancel()
	text, err := b.ChromeProxy(timeoutCtx, req.AgentID, args.URL, policy, args.WaitFor, 32768)
	if err != nil {
		return Response{OK: false, ErrorCode: "chrome_error", ErrorDetail: err.Error()}, nil
	}

	data, _ := json.Marshal(map[string]string{"text": text})
	return Response{OK: true, Data: data}, nil
}

func normalizeBrowserScope(constraints []ScopeConstraint) browserScope {
	scope := browserScope{}
	for _, sc := range constraints {
		if sc.Entity != "Link" {
			continue
		}
		scope.domains = append(scope.domains, sc.Constraints["domain"]...)
		scope.domainSuffixes = append(scope.domainSuffixes, sc.Constraints["domain-suffix"]...)
		scope.pathPrefixes = append(scope.pathPrefixes, sc.Constraints["path-prefix"]...)
	}
	return scope
}

func buildBrowserWhitelistPolicy(constraints []ScopeConstraint) chromproxy.WhitelistPolicy {
	return normalizeBrowserScope(constraints).whitelistPolicy()
}

func (s browserScope) whitelistPolicy() chromproxy.WhitelistPolicy {
	return chromproxy.WhitelistPolicy{
		Domains:        append([]string(nil), s.domains...),
		DomainSuffixes: append([]string(nil), s.domainSuffixes...),
		PathPrefixes:   append([]string(nil), s.pathPrefixes...),
	}
}

// urlMatchesConstraints returns true if rawURL matches any of the ECS-style
// scope constraints.
func urlMatchesConstraints(rawURL string, constraints []ScopeConstraint) bool {
	return normalizeBrowserScope(constraints).allowsURL(rawURL)
}

func (s browserScope) allowsURL(rawURL string) bool {
	if len(s.domains) == 0 && len(s.domainSuffixes) == 0 && len(s.pathPrefixes) == 0 {
		return false
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	domain := strings.ToLower(u.Host)
	domainAllowed := false

	for _, d := range s.domains {
		if domain == strings.ToLower(d) {
			domainAllowed = true
			break
		}
	}
	if !domainAllowed {
		for _, suffixValue := range s.domainSuffixes {
			suffix := strings.ToLower(suffixValue)
			if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
				domainAllowed = true
				break
			}
		}
	}
	if !domainAllowed {
		return false
	}
	if len(s.pathPrefixes) == 0 {
		return true
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	for _, prefix := range s.pathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
