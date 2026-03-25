package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	openclawmigrate "vivary.dev/vivary/internal/migrate"
)

func runMigrate(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: viv migrate openclaw inspect [--source <dir>] [--out <file>]")
	}
	if args[0] != "openclaw" {
		return fmt.Errorf("unknown migrate target: %s", args[0])
	}
	if len(args) < 2 || args[1] != "inspect" {
		return fmt.Errorf("usage: viv migrate openclaw inspect [--source <dir>] [--out <file>]")
	}

	fs := flag.NewFlagSet("migrate openclaw inspect", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	defaultSource := filepath.Join(userHomeDir(), ".openclaw")
	defaultOut := filepath.Join("migrate", "findings.kdl")
	source := fs.String("source", defaultSource, "path to the OpenClaw install root")
	out := fs.String("out", defaultOut, "path to write KDL findings")
	if err := fs.Parse(args[2:]); err != nil {
		return err
	}

	d, err := openclawmigrate.InspectOpenClaw(*source)
	if err != nil {
		return err
	}
	if err := openclawmigrate.WriteKDL(*out, d); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "wrote OpenClaw discovery findings to %s\n", *out)
	fmt.Fprintf(stdout, "source: %s\n", d.Source)
	fmt.Fprintf(stdout, "artifacts=%d channels=%d plugins=%d credentials=%d\n", len(d.Artifacts), len(d.Channels), len(d.Plugins), len(d.Credentials))
	for _, tier := range []string{"portable", "portable_with_review", "bridge_required", "unsupported"} {
		if n := d.PortabilityCounts[tier]; n > 0 {
			fmt.Fprintf(stdout, "%s=%d\n", tier, n)
		}
	}
	_ = stderr
	return nil
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return home
}
