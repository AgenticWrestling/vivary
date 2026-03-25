package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

const (
	proxyPortStart = 8700
	proxyPortEnd   = 8800
)

type proxyInstance struct {
	url      string
	listener net.Listener
	server   *http.Server
}

type proxyAllocator struct {
	mu           sync.Mutex
	host         string
	transport    *http.Transport
	instances    map[string]*proxyInstance
	listen       func(network, address string) (net.Listener, error)
	httpClient   *http.Client
	nextPortHint int
	log          *slog.Logger
}

func newProxyAllocator(host string, log *slog.Logger) *proxyAllocator {
	transport := &http.Transport{Proxy: nil}
	return &proxyAllocator{
		host:         host,
		transport:    transport,
		instances:    make(map[string]*proxyInstance),
		listen:       net.Listen,
		httpClient:   &http.Client{Transport: transport},
		nextPortHint: proxyPortStart,
		log:          log,
	}
}

func (p *proxyAllocator) Acquire(agentID string) (*proxyInstance, error) {
	p.mu.Lock()
	if inst, ok := p.instances[agentID]; ok {
		p.mu.Unlock()
		return inst, nil
	}
	ln, port, err := p.listenNextLocked()
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	handler := &agentProxyHandler{client: p.httpClient, transport: p.transport}
	srv := &http.Server{Handler: handler}
	inst := &proxyInstance{url: fmt.Sprintf("http://%s:%d", p.host, port), listener: ln, server: srv}
	p.instances[agentID] = inst
	p.mu.Unlock()
	go func() {
		_ = srv.Serve(ln)
	}()
	if p.log != nil {
		p.log.Info("browser proxy started", "agent", agentID, "url", inst.url)
	}
	return inst, nil
}

func (p *proxyAllocator) Release(agentID string) error {
	p.mu.Lock()
	inst, ok := p.instances[agentID]
	if ok {
		delete(p.instances, agentID)
	}
	p.mu.Unlock()
	if !ok {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if p.log != nil {
		p.log.Info("browser proxy stopped", "agent", agentID, "url", inst.url)
	}
	return inst.server.Shutdown(ctx)
}

func (p *proxyAllocator) ReleaseAll() {
	p.mu.Lock()
	ids := make([]string, 0, len(p.instances))
	for id := range p.instances {
		ids = append(ids, id)
	}
	p.mu.Unlock()
	for _, id := range ids {
		_ = p.Release(id)
	}
}

func (p *proxyAllocator) listenNextLocked() (net.Listener, int, error) {
	for attempt := 0; attempt <= proxyPortEnd-proxyPortStart; attempt++ {
		port := proxyPortStart + ((p.nextPortHint - proxyPortStart + attempt) % (proxyPortEnd - proxyPortStart + 1))
		ln, err := p.listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
		if err == nil {
			p.nextPortHint = port + 1
			if p.nextPortHint > proxyPortEnd {
				p.nextPortHint = proxyPortStart
			}
			return ln, port, nil
		}
	}
	return nil, 0, fmt.Errorf("no free proxy ports in range %d-%d", proxyPortStart, proxyPortEnd)
}

type agentProxyHandler struct {
	client    *http.Client
	transport *http.Transport
}

func (h *agentProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		h.handleConnect(w, r)
		return
	}
	outReq := r.Clone(r.Context())
	outReq.RequestURI = ""
	resp, err := h.client.Do(outReq)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, vals := range resp.Header {
		for _, v := range vals {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (h *agentProxyHandler) handleConnect(w http.ResponseWriter, r *http.Request) {
	dst, err := net.Dial("tcp", r.Host)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		dst.Close()
		http.Error(w, "hijack unsupported", http.StatusInternalServerError)
		return
	}
	clientConn, rw, err := hj.Hijack()
	if err != nil {
		dst.Close()
		return
	}
	_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = rw.Flush()
	go proxyCopy(dst, clientConn)
	go proxyCopy(clientConn, dst)
}

func proxyCopy(dst net.Conn, src net.Conn) {
	defer dst.Close()
	defer src.Close()
	_, _ = io.Copy(dst, src)
}

func detectAdvertiseHost() (string, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", err
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			ipNet, ok := addr.(*net.IPNet)
			if !ok || ipNet.IP == nil {
				continue
			}
			ip := ipNet.IP.To4()
			if ip == nil || ip.IsLoopback() {
				continue
			}
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no non-loopback IPv4 address found")
}
