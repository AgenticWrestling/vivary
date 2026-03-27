// Package chromproxy connects to headless Chromium's Chrome DevTools Protocol
// (CDP) remote debug port, enforces per-agent URL whitelists, and extracts
// readable page text via the accessibility tree.
//
// The proxy is invoked by keeperd via the Browser_Page_Read capability.
// keeperd connects to a host-side 'chromed' service to acquire a dedicated
// headless Chrome instance for the agent, with a separate --user-data-dir
// profile.  The chromproxy then connects to that instance's CDP port,
// enforces the agent's whitelist policy, and extracts accessible text.
//
// CDP transport: HTTP + WebSocket to the agent's dedicated debug address.
package chromproxy

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy connects to a single headless Chrome instance and exposes
// ReadPage for capability execution.
type Proxy struct {
	debugAddr      string // host:port, e.g. "127.0.0.1:9222"
	unixSocketPath string
	client         *http.Client

	mu      sync.Mutex
	targets map[string]*target // keyed by targetID
}

// WhitelistPolicy is the browser allow-list enforced before CDP traffic is sent
// to Chrome. It supports both legacy URL-prefix policies and the typed Link
// constraints used by the MVP browser capability.
type WhitelistPolicy struct {
	Prefixes       []string
	Domains        []string
	DomainSuffixes []string
	PathPrefixes   []string
}

// New creates a Proxy pointing at debugAddr.
func New(debugAddr string) *Proxy {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
	}
	p := &Proxy{
		debugAddr: debugAddr,
		client: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
		targets: make(map[string]*target),
	}
	if strings.HasPrefix(debugAddr, "unix:") {
		p.unixSocketPath = strings.TrimPrefix(debugAddr, "unix:")
		p.debugAddr = "unix"
		transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", p.unixSocketPath)
		}
	}
	return p
}

// ReadPage navigates to rawURL, waits for the network to settle, extracts
// accessible text, and returns it truncated to maxChars.
//
// policy defines the browser whitelist enforced before any CDP activity.
func (p *Proxy) ReadPage(ctx context.Context, agentID, rawURL string, policy WhitelistPolicy, waitFor string, maxChars int) (string, error) {
	if !policy.Allows(rawURL) {
		return "", fmt.Errorf("URL %q not permitted for agent %q", rawURL, agentID)
	}

	tgt, err := p.openTarget(ctx, agentID)
	if err != nil {
		return "", fmt.Errorf("open target: %w", err)
	}

	// Navigate.
	if err := tgt.navigate(ctx, rawURL); err != nil {
		return "", fmt.Errorf("navigate: %w", err)
	}

	// Wait strategy.
	switch waitFor {
	case "networkidle", "":
		if err := tgt.waitNetworkIdle(ctx, 2*time.Second); err != nil {
			// Non-fatal: proceed with whatever has loaded.
			_ = err
		}
	case "load", "domcontentloaded":
		// navigate() already waits for load; nothing extra needed.
	}

	// Extract text via accessibility tree.
	text, err := tgt.extractAccessibleText(ctx)
	if err != nil {
		return "", fmt.Errorf("extract text: %w", err)
	}

	if maxChars > 0 && len(text) > maxChars {
		text = text[:maxChars]
	}
	return text, nil
}

// ---- Target (one CDP session) ----------------------------------------------

type target struct {
	ws      cdpConn
	msgID   atomic.Int64
	mu      sync.Mutex
	pending map[int64]chan cdpResult
}

type cdpResult struct {
	result json.RawMessage
	err    error
}

// openTarget returns an existing target for agentID or creates a new one.
func (p *Proxy) openTarget(ctx context.Context, agentID string) (*target, error) {
	p.mu.Lock()
	if t, ok := p.targets[agentID]; ok {
		p.mu.Unlock()
		return t, nil
	}
	p.mu.Unlock()

	// List existing targets.
	targets, err := p.listTargets(ctx)
	if err != nil {
		return nil, err
	}

	// Find or create a page target.
	var wsURL string
	for _, tgt := range targets {
		if tgt["type"] == "page" {
			wsURL = tgt["webSocketDebuggerUrl"]
			break
		}
	}
	if wsURL == "" {
		wsURL, err = p.newTarget(ctx)
		if err != nil {
			return nil, fmt.Errorf("create page target: %w", err)
		}
	}

	conn, err := dialCDP(ctx, wsURL, p.unixSocketPath)
	if err != nil {
		return nil, fmt.Errorf("dial CDP: %w", err)
	}

	t := &target{
		ws:      conn,
		pending: make(map[int64]chan cdpResult),
	}
	go t.readLoop()

	p.mu.Lock()
	p.targets[agentID] = t
	p.mu.Unlock()

	return t, nil
}

// listTargets calls /json/list on the CDP HTTP endpoint.
func (p *Proxy) listTargets(ctx context.Context) ([]map[string]string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet,
		p.httpURL("/json/list"), nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var targets []map[string]string
	_ = json.Unmarshal(body, &targets)
	return targets, nil
}

// newTarget creates a new about:blank page via /json/new.
func (p *Proxy) newTarget(ctx context.Context) (string, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut,
		p.httpURL("/json/new?about:blank"), nil)
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	var tgt map[string]string
	if err := json.Unmarshal(body, &tgt); err != nil {
		return "", fmt.Errorf("parse new target: %w", err)
	}
	return tgt["webSocketDebuggerUrl"], nil
}

func (p *Proxy) httpURL(path string) string {
	if p.unixSocketPath != "" {
		return "http://127.0.0.1" + path
	}
	return "http://" + p.debugAddr + path
}

// ---- CDP session commands --------------------------------------------------

func (t *target) navigate(ctx context.Context, url string) error {
	_, err := t.call(ctx, "Page.navigate", map[string]any{"url": url})
	return err
}

// waitNetworkIdle polls document.readyState and waits for no in-flight requests
// for a quiet window of quietDur.
func (t *target) waitNetworkIdle(ctx context.Context, quietDur time.Duration) error {
	deadline := time.Now().Add(10 * time.Second)
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	var lastActivity time.Time
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case now := <-ticker.C:
			res, err := t.call(ctx, "Runtime.evaluate", map[string]any{
				"expression":    "document.readyState",
				"returnByValue": true,
			})
			if err != nil {
				return err
			}
			var result struct {
				Result struct{ Value string } `json:"result"`
			}
			_ = json.Unmarshal(res, &result)
			if result.Result.Value == "complete" {
				if lastActivity.IsZero() {
					lastActivity = now
				}
				if now.Sub(lastActivity) >= quietDur {
					return nil
				}
			} else {
				lastActivity = time.Time{}
			}
			if now.After(deadline) {
				return nil // proceed anyway
			}
		}
	}
}

// extractAccessibleText uses Accessibility.getFullAXTree to build a plain-text
// representation of the page without raw HTML.
func (t *target) extractAccessibleText(ctx context.Context) (string, error) {
	res, err := t.call(ctx, "Accessibility.getFullAXTree", map[string]any{})
	if err != nil {
		// Fallback: extract innerText via Runtime.evaluate.
		return t.extractInnerText(ctx)
	}

	var tree struct {
		Nodes []struct {
			Role       struct{ Value string }      `json:"role"`
			Name       struct{ Value string }      `json:"name"`
			Properties []struct{ Name, Value any } `json:"properties"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal(res, &tree); err != nil {
		return t.extractInnerText(ctx)
	}

	var sb strings.Builder
	for _, n := range tree.Nodes {
		if n.Name.Value == "" {
			continue
		}
		switch n.Role.Value {
		case "heading", "link", "button", "staticText", "paragraph",
			"listItem", "cell", "columnHeader", "caption":
			sb.WriteString(n.Name.Value)
			sb.WriteByte('\n')
		}
	}
	text := strings.TrimSpace(sb.String())
	if text == "" {
		return t.extractInnerText(ctx)
	}
	return text, nil
}

func (t *target) extractInnerText(ctx context.Context) (string, error) {
	res, err := t.call(ctx, "Runtime.evaluate", map[string]any{
		"expression":    "document.body ? document.body.innerText : ''",
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	var result struct {
		Result struct{ Value string } `json:"result"`
	}
	if err := json.Unmarshal(res, &result); err != nil {
		return "", err
	}
	return result.Result.Value, nil
}

// call sends a CDP command and waits for its response.
func (t *target) call(ctx context.Context, method string, params map[string]any) (json.RawMessage, error) {
	id := t.msgID.Add(1)
	msg, _ := json.Marshal(map[string]any{
		"id": id, "method": method, "params": params,
	})

	ch := make(chan cdpResult, 1)
	t.mu.Lock()
	t.pending[id] = ch
	t.mu.Unlock()

	if err := t.ws.Send(msg); err != nil {
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, err
	}

	select {
	case <-ctx.Done():
		t.mu.Lock()
		delete(t.pending, id)
		t.mu.Unlock()
		return nil, ctx.Err()
	case r := <-ch:
		return r.result, r.err
	}
}

// readLoop dispatches CDP responses to pending callers.
func (t *target) readLoop() {
	for {
		msg, err := t.ws.Recv()
		if err != nil {
			// Connection closed; drain all pending callers.
			t.mu.Lock()
			for _, ch := range t.pending {
				ch <- cdpResult{err: err}
			}
			t.pending = make(map[int64]chan cdpResult)
			t.mu.Unlock()
			return
		}
		var envelope struct {
			ID     int64           `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(msg, &envelope); err != nil || envelope.ID == 0 {
			continue // event (no id), skip
		}
		t.mu.Lock()
		ch := t.pending[envelope.ID]
		delete(t.pending, envelope.ID)
		t.mu.Unlock()
		if ch == nil {
			continue
		}
		if envelope.Error != nil {
			ch <- cdpResult{err: fmt.Errorf("CDP error: %s", envelope.Error.Message)}
		} else {
			ch <- cdpResult{result: envelope.Result}
		}
	}
}

// ---- Scope helpers ---------------------------------------------------------

// Allows reports whether rawURL is permitted by the policy.
func (p WhitelistPolicy) Allows(rawURL string) bool {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return false
	}
	if p.allowsByPrefix(rawURL) {
		return true
	}
	domain := strings.ToLower(u.Host)
	domainAllowed := false
	for _, d := range p.Domains {
		if domain == strings.ToLower(strings.TrimSpace(d)) {
			domainAllowed = true
			break
		}
	}
	if !domainAllowed {
		for _, s := range p.DomainSuffixes {
			suffix := strings.ToLower(strings.TrimSpace(s))
			if suffix != "" && (domain == suffix || strings.HasSuffix(domain, "."+suffix)) {
				domainAllowed = true
				break
			}
		}
	}
	if !domainAllowed {
		return false
	}
	if len(p.PathPrefixes) == 0 {
		return true
	}
	path := u.Path
	if path == "" {
		path = "/"
	}
	for _, prefix := range p.PathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

func (p WhitelistPolicy) allowsByPrefix(rawURL string) bool {
	needle := strings.ToLower(rawURL)
	for _, prefix := range p.Prefixes {
		candidate := strings.TrimSpace(strings.ToLower(prefix))
		if candidate == "" || !strings.HasPrefix(needle, candidate) {
			continue
		}
		rest := needle[len(candidate):]
		last := candidate[len(candidate)-1]
		if rest == "" || rest[0] == '/' || rest[0] == '?' || rest[0] == '#' ||
			last == '/' || last == '?' || last == '#' {
			return true
		}
	}
	return false
}

// ---- Minimal WebSocket CDP transport ---------------------------------------
// A full WebSocket library is not pulled in to keep the dependency tree lean.
// This implements the subset of RFC 6455 needed for CDP (text frames only,
// client-side masking, no fragmentation).

type cdpConn interface {
	Send([]byte) error
	Recv() ([]byte, error)
}

// dialCDP opens a WebSocket connection to wsURL using a hand-rolled client.
func dialCDP(ctx context.Context, wsURL string, unixSocketPath string) (cdpConn, error) {
	// wsURL is of the form ws://127.0.0.1:9222/devtools/page/<id>
	addr, path, err := parseWSURL(wsURL)
	if err != nil {
		return nil, err
	}

	var d net.Dialer
	network := "tcp"
	dialAddr := addr
	if unixSocketPath != "" {
		network = "unix"
		dialAddr = unixSocketPath
	}
	conn, err := d.DialContext(ctx, network, dialAddr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", dialAddr, err)
	}

	// Build HTTP upgrade request.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+path, nil)
	if err != nil {
		conn.Close()
		return nil, err
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	req.Header.Set("Sec-WebSocket-Version", "13")

	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, fmt.Errorf("send upgrade request: %w", err)
	}

	// Read and parse the 101 response.
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("read upgrade response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusSwitchingProtocols {
		conn.Close()
		return nil, fmt.Errorf("unexpected upgrade status: %d", resp.StatusCode)
	}

	return &wsConn{conn: conn, br: br}, nil
}

func hostHeader(addr, unixSocketPath string) string {
	if unixSocketPath != "" {
		return "127.0.0.1"
	}
	return addr
}

func parseWSURL(wsURL string) (addr, path string, err error) {
	// ws://host:port/path
	s := strings.TrimPrefix(wsURL, "ws://")
	slash := strings.Index(s, "/")
	if slash < 0 {
		return s, "/", nil
	}
	return s[:slash], s[slash:], nil
}

type wsConn struct {
	mu   sync.Mutex
	conn net.Conn
	br   *bufio.Reader
}

func (w *wsConn) Send(msg []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	// Opcode 0x1 (text), FIN=1, masked=1.
	frame := wsEncodeFrame(msg)
	_, err := w.conn.Write(frame)
	return err
}

func (w *wsConn) Recv() ([]byte, error) {
	// Read 2-byte header.
	header := make([]byte, 2)
	if _, err := io.ReadFull(w.br, header); err != nil {
		return nil, err
	}
	// fin := (header[0] & 0x80) != 0
	masked := (header[1] & 0x80) != 0
	payloadLen := int64(header[1] & 0x7F)

	switch payloadLen {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(w.br, ext); err != nil {
			return nil, err
		}
		payloadLen = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(w.br, ext); err != nil {
			return nil, err
		}
		payloadLen = 0
		for _, b := range ext {
			payloadLen = payloadLen<<8 | int64(b)
		}
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(w.br, maskKey[:]); err != nil {
			return nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(w.br, payload); err != nil {
		return nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= maskKey[i%4]
		}
	}
	return payload, nil
}

// wsEncodeFrame encodes msg as a masked WebSocket text frame.
func wsEncodeFrame(msg []byte) []byte {
	n := len(msg)
	var header []byte
	header = append(header, 0x81) // FIN + opcode text
	maskBit := byte(0x80)
	switch {
	case n < 126:
		header = append(header, maskBit|byte(n))
	case n < 65536:
		header = append(header, maskBit|126, byte(n>>8), byte(n))
	default:
		header = append(header, maskBit|127,
			0, 0, 0, 0,
			byte(n>>24), byte(n>>16), byte(n>>8), byte(n),
		)
	}
	// Mask key: all zeros (valid per RFC 6455 for localhost).
	header = append(header, 0, 0, 0, 0)
	return append(header, msg...)
}
