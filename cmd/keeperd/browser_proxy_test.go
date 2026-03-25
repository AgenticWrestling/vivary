package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestProxyAllocatorSkipsUsedPorts(t *testing.T) {
	busy, err := net.Listen("tcp", "127.0.0.1:8700")
	if err != nil {
		t.Fatalf("listen busy port: %v", err)
	}
	defer busy.Close()

	alloc := newProxyAllocator("10.0.0.2", nil)
	inst, err := alloc.Acquire("agent1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer alloc.ReleaseAll()
	if !strings.HasSuffix(inst.url, ":8701") {
		t.Fatalf("proxy url = %q, want port 8701", inst.url)
	}
}

func TestAgentProxyHandlerHTTPForward(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "proxy ok")
	}))
	defer target.Close()

	alloc := newProxyAllocator("127.0.0.1", nil)
	inst, err := alloc.Acquire("agent1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer alloc.ReleaseAll()

	client := &http.Client{Transport: &http.Transport{Proxy: func(*http.Request) (*url.URL, error) { return url.Parse(inst.url) }}}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("GET via proxy: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "proxy ok" {
		t.Fatalf("body = %q", body)
	}
}

func TestAgentProxyHandlerConnectTunnel(t *testing.T) {
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen target: %v", err)
	}
	defer target.Close()
	go func() {
		conn, err := target.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 4)
		_, _ = io.ReadFull(conn, buf)
		_, _ = conn.Write([]byte("pong"))
	}()

	alloc := newProxyAllocator("127.0.0.1", nil)
	inst, err := alloc.Acquire("agent1")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	defer alloc.ReleaseAll()

	proxyConn, err := net.Dial("tcp", strings.TrimPrefix(inst.url, "http://"))
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	defer proxyConn.Close()
	_, _ = fmt.Fprintf(proxyConn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target.Addr().String(), target.Addr().String())
	reader := bufio.NewReader(proxyConn)
	line, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	if !strings.Contains(line, "200") {
		t.Fatalf("unexpected CONNECT response: %q", line)
	}
	for {
		line, err = reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read headers: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	_, _ = proxyConn.Write([]byte("ping"))
	buf := make([]byte, 4)
	proxyConn.SetReadDeadline(time.Now().Add(time.Second))
	_, err = io.ReadFull(reader, buf)
	if err != nil {
		t.Fatalf("read tunneled response: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("tunneled body = %q", buf)
	}
}
