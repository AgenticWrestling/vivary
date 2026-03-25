package main

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	chromedapi "vivary.dev/vivary/internal/chromed"
	"vivary.dev/vivary/internal/chromproxy"
)

type pageReader interface {
	ReadPage(context.Context, string, string, chromproxy.WhitelistPolicy, string, int) (string, error)
}

type chromedClient interface {
	Acquire(context.Context, chromedapi.AcquireRequest) (chromedapi.AcquireResponse, error)
	Release(context.Context, string) (chromedapi.ReleaseResponse, error)
	Status(context.Context, string) (chromedapi.StatusResponse, error)
}

type browserManager struct {
	mu        sync.Mutex
	client    chromedClient
	proxyByID map[string]pageReader
	newProxy  func(string) pageReader
	proxies   *proxyAllocator
	log       *slog.Logger
}

func newBrowserManager(client chromedClient, proxies *proxyAllocator, log *slog.Logger) *browserManager {
	return &browserManager{client: client, proxyByID: make(map[string]pageReader), newProxy: func(addr string) pageReader { return chromproxy.New(addr) }, proxies: proxies, log: log}
}

func (m *browserManager) ReadPage(ctx context.Context, agentID, targetURL string, policy chromproxy.WhitelistPolicy, waitFor string, maxChars int) (string, error) {
	if m.client == nil {
		return "", fmt.Errorf("chromed client not configured")
	}
	if m.proxies == nil {
		return "", fmt.Errorf("browser proxy allocator not configured")
	}
	m.mu.Lock()
	proxy := m.proxyByID[agentID]
	m.mu.Unlock()
	if proxy == nil {
		proxyInst, err := m.proxies.Acquire(agentID)
		if err != nil {
			return "", fmt.Errorf("acquire browser proxy: %w", err)
		}
		acqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		resp, err := m.client.Acquire(acqCtx, chromedapi.AcquireRequest{AgentID: agentID, ProxyServer: proxyInst.url, TimeoutSec: 10})
		if err != nil {
			_ = m.proxies.Release(agentID)
			return "", fmt.Errorf("acquire browser session: %w", err)
		}
		proxy = m.newProxy(resp.DebugAddr)
		m.mu.Lock()
		m.proxyByID[agentID] = proxy
		m.mu.Unlock()
		if m.log != nil {
			m.log.Info("browser session acquired", "agent", agentID, "debug_addr", resp.DebugAddr)
		}
	}
	return proxy.ReadPage(ctx, agentID, targetURL, policy, waitFor, maxChars)
}

func (m *browserManager) Release(ctx context.Context, agentID string) error {
	if m.client == nil || agentID == "" {
		return nil
	}
	m.mu.Lock()
	delete(m.proxyByID, agentID)
	m.mu.Unlock()
	if m.proxies != nil {
		_ = m.proxies.Release(agentID)
	}
	if m.client == nil {
		return nil
	}
	_, err := m.client.Release(ctx, agentID)
	return err
}
