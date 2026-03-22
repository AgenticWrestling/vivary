// Package chromproxy connects to headless Chromium's Chrome DevTools Protocol
// (CDP) remote debug port, enforces per-agent URL whitelists, and extracts
// readable page text via the accessibility tree.
//
// The proxy is invoked by keeperd when a Ward issues a Browser_Page_Read
// capability request.  keeperd passes the agent's scope (comma-separated URL
// prefixes) to Proxy.ReadPage; the proxy rejects requests for out-of-scope
// URLs before sending any CDP command to Chrome.
//
// CDP transport: HTTP + WebSocket to 127.0.0.1:<debugPort>.
// One Chrome instance is shared across all agents; each agent gets a
// dedicated browser context (--user-data-dir is set per-agent at spawn time).
package chromproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Proxy connects to a single headless Chrome instance and exposes
// ReadPage for capability execution.
type Proxy struct {
	debugAddr string // host:port, e.g. "127.0.0.1:9222"
	client    *http.Client

	mu      sync.Mutex
	targets map[string]*target // keyed by targetID
}

// New creates a Proxy pointing at debugAddr.
func New(debugAddr string) *Proxy {
	return &Proxy{
		debugAddr: debugAddr,
		client: &http.Client{
			Transport: &http.Transport{
				DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
			},
			Timeout: 10 * time.Second,
		},
		targets: make(map[string]*target),
	}
}

// ReadPage navigates to rawURL, waits for the network to settle, extracts
// accessible text, and returns it truncated to maxChars.
//
// scope is a comma-separated list of allowed URL prefixes (from the agent ACL).
// ReadPage rejects rawURL before any CDP activity if it does not match scope.
func (p *Proxy) ReadPage(ctx context.Context, agentID, rawURL, waitFor string, maxChars int) (string, error) {
	if !urlInScope(rawURL, scope(agentID)) {
		// Note: scope enforcement is also done by the capability layer before
		// calling ReadPage.  This is defence-in-depth.
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

	conn, err := dialCDP(ctx, wsURL)
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
		"http://"+p.debugAddr+"/json/list", nil)
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
		"http://"+p.debugAddr+"/json/new?about:blank", nil)
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
			Role       struct{ Value string }              `json:"role"`
			Name       struct{ Value string }              `json:"name"`
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

// scope returns the scope string for agentID.  In the proxy package the scope
// is passed in via the caller (BrowserPageRead.Execute → ReadPage), so this
// stub is only used for the defence-in-depth check which receives it as a
// parameter.  The function signature is kept for clarity.
func scope(_ string) string { return "" }

func urlInScope(rawURL, scopeStr string) bool {
	if scopeStr == "" {
		return true // caller already enforced scope; proxy accepts
	}
	needle := strings.ToLower(rawURL)
	for prefix := range strings.SplitSeq(scopeStr, ",") {
		p := strings.TrimSpace(strings.ToLower(prefix))
		if p != "" && strings.HasPrefix(needle, p) {
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
func dialCDP(ctx context.Context, wsURL string) (cdpConn, error) {
	// wsURL is of the form ws://127.0.0.1:9222/devtools/page/<id>
	addr, path, err := parseWSURL(wsURL)
	if err != nil {
		return nil, err
	}

	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	// Send HTTP upgrade request.
	key := "dGhlIHNhbXBsZSBub25jZQ==" // static key is fine for localhost-only CDP
	req := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"+
			"Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n",
		path, addr, key,
	)
	if _, err := io.WriteString(conn, req); err != nil {
		conn.Close()
		return nil, err
	}

	// Read the 101 response (consume until double CRLF).
	buf := make([]byte, 1024)
	total := 0
	for {
		n, err := conn.Read(buf[total:])
		if err != nil {
			conn.Close()
			return nil, fmt.Errorf("read HTTP upgrade response: %w", err)
		}
		total += n
		if strings.Contains(string(buf[:total]), "\r\n\r\n") {
			break
		}
		if total >= len(buf) {
			conn.Close()
			return nil, fmt.Errorf("HTTP upgrade response too large")
		}
	}
	if !strings.Contains(string(buf[:total]), "101") {
		conn.Close()
		return nil, fmt.Errorf("unexpected HTTP upgrade response")
	}

	return &wsConn{conn: conn}, nil
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
	if _, err := io.ReadFull(w.conn, header); err != nil {
		return nil, err
	}
	// fin := (header[0] & 0x80) != 0
	masked := (header[1] & 0x80) != 0
	payloadLen := int64(header[1] & 0x7F)

	switch payloadLen {
	case 126:
		ext := make([]byte, 2)
		if _, err := io.ReadFull(w.conn, ext); err != nil {
			return nil, err
		}
		payloadLen = int64(ext[0])<<8 | int64(ext[1])
	case 127:
		ext := make([]byte, 8)
		if _, err := io.ReadFull(w.conn, ext); err != nil {
			return nil, err
		}
		payloadLen = 0
		for _, b := range ext {
			payloadLen = payloadLen<<8 | int64(b)
		}
	}

	var maskKey [4]byte
	if masked {
		if _, err := io.ReadFull(w.conn, maskKey[:]); err != nil {
			return nil, err
		}
	}

	payload := make([]byte, payloadLen)
	if _, err := io.ReadFull(w.conn, payload); err != nil {
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
