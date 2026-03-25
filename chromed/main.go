package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	socketPath := flag.String("socket", "/run/chromed.sock", "Unix socket path for chromed")
	profileRoot := flag.String("profile-root", "/var/lib/vivary/chromed/profiles", "root directory for per-agent Chrome profiles")
	chromeBinary := flag.String("chrome-binary", "/usr/bin/google-chrome-beta", "host Chrome binary path")
	containerName := flag.String("container-name", "", "LXD container name to track for lifecycle shutdown")
	monitorInterval := flag.Duration("container-monitor-interval", time.Second, "poll interval for container lifecycle checks")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	mgr := newManager(*profileRoot, *chromeBinary, log)
	srv := &server{
		socketPath:      *socketPath,
		manager:         mgr,
		log:             log,
		containerName:   *containerName,
		checkContainer:  containerRunning,
		monitorInterval: *monitorInterval,
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	if err := srv.Run(ctx); err != nil {
		log.Error("chromed exited", "err", err)
		os.Exit(1)
	}
}
