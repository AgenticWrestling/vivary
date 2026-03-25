package main

// chrome.go manages the headless Chromium sidecar process.
//
// keeperd launches a single shared Chromium instance at startup. The current
// MVP does not provide per-agent Chrome profile isolation; browser isolation is
// limited to keeper-side target/session bookkeeping plus capability/proxy
// whitelist enforcement.
//
// The sidecar is non-fatal: if Chromium is not found or fails to start,
// keeperd logs a warning and continues.  Browser_Page_Read capability
// requests will return an error until Chrome is available.
//
// The Chrome process is terminated when keeperd's context is cancelled
// (SIGINT/SIGTERM), cleaning up the child process gracefully.

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// launchChrome starts a headless Chromium sidecar and returns its cancel func.
// Returns (nil, nil) without error if the chrome-binary is empty (disabled).
// Returns (nil, non-fatal-warning) if Chromium is not found.
// The returned cancel func terminates the process; it is safe to call it
// multiple times.
func launchChrome(ctx context.Context, cfg OrchestratorConfig, log *slog.Logger) (cancel func(), err error) {
	if cfg.ChromeBinaryPath == "" {
		return func() {}, nil // Chrome sidecar explicitly disabled
	}

	// Resolve the binary: first try the configured path directly, then PATH.
	chromeBin, err := exec.LookPath(cfg.ChromeBinaryPath)
	if err != nil {
		// Try common alternative names.
		for _, alt := range []string{"chromium-browser", "google-chrome", "google-chrome-stable"} {
			if p, e := exec.LookPath(alt); e == nil {
				chromeBin = p
				break
			}
		}
		if chromeBin == "" {
			return func() {}, fmt.Errorf("chromium not found (tried %q and common names): %w", cfg.ChromeBinaryPath, err)
		}
	}

	// Ensure the user-data base directory exists.
	if err := os.MkdirAll(cfg.ChromeUserDataDir, 0o750); err != nil {
		return func() {}, fmt.Errorf("chrome user-data-dir: %w", err)
	}

	// Extract host:port from ChromeRemoteDebugAddr to get the port number.
	debugPort := chromeDebugPort(cfg.ChromeRemoteDebugAddr)

	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--no-sandbox",
		"--disable-dev-shm-usage",
		"--remote-debugging-address=127.0.0.1",
		"--remote-debugging-port=" + debugPort,
		"--user-data-dir=" + filepath.Join(cfg.ChromeUserDataDir, "default"),
		"--disable-extensions",
		"--disable-background-networking",
		"--safebrowsing-disable-auto-update",
		"about:blank",
	}

	cmd := exec.CommandContext(ctx, chromeBin, args...)
	cmd.Stdout = nil // suppress Chrome's verbose stdout
	cmd.Stderr = nil

	if err := cmd.Start(); err != nil {
		return func() {}, fmt.Errorf("start chromium: %w", err)
	}

	log.Info("chrome sidecar started",
		"pid", cmd.Process.Pid,
		"debug_addr", cfg.ChromeRemoteDebugAddr,
	)

	// Reap the process asynchronously.
	go func() {
		if err := cmd.Wait(); err != nil {
			if ctx.Err() != nil {
				return // expected; context was cancelled
			}
			log.Warn("chrome sidecar exited unexpectedly", "err", err)
		}
	}()

	// Give Chrome a moment to open the debug port before the proxy dials it.
	// This is best-effort; the proxy retries internally.
	time.Sleep(200 * time.Millisecond)

	cancelFn := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	}

	return cancelFn, nil
}

// chromeDebugPort extracts the port number from a "host:port" debug address.
func chromeDebugPort(addr string) string {
	for i := len(addr) - 1; i >= 0; i-- {
		if addr[i] == ':' {
			return addr[i+1:]
		}
	}
	return "9222" // fallback
}
