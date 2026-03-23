// viv is the operator CLI/TUI for the VIVARY runtime.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"net"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"vivary.dev/vivary/internal/ctl"
	"vivary.dev/vivary/internal/switchboard"
	"vivary.dev/vivary/pkg/mus"
)

const cliVersion = "0.1.0-dev"

func main() {
	socketPath := flag.String("socket", "./keeper.sock", "path to keeper.sock")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		runTUI(*socketPath)
		return
	}

	switch args[0] {
	case "tui":
		runTUI(*socketPath)
		return
	case "status":
		c := mustDialClient(*socketPath)
		defer c.conn.Close()
		c.cmdStatus()
	case "agent":
		c := mustDialClient(*socketPath)
		defer c.conn.Close()
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "viv agent <create|destroy|list> ...")
			os.Exit(1)
		}
		switch args[1] {
		case "list":
			c.cmdAgentList()
		case "create":
			c.cmdAgentCreate(args[2:])
		case "destroy":
			c.cmdAgentDestroy(args[2:])
		default:
			fmt.Fprintf(os.Stderr, "unknown agent subcommand: %s\n", args[1])
			os.Exit(1)
		}
	case "prompt":
		c := mustDialClient(*socketPath)
		defer c.conn.Close()
		c.cmdPrompt(args[1:])
	case "ping":
		c := mustDialClient(*socketPath)
		defer c.conn.Close()
		c.cmdPing()
	case "version":
		fmt.Println(cliVersion)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `viv — VIVARY operator CLI

Usage:
  viv [--socket <path>] <command> [args]

Commands:
  tui                        Launch the BubbleTea operator TUI (default)
  status                     Show daemon and agent status
  agent list                 List provisioned agents
  agent create --id <id> --template <path>
                             Provision a new agent workspace
  agent destroy --id <id>    Destroy an agent workspace
  prompt --agent <id> --seq <n> <text>
                             Dispatch a prompt to an agent
  ping                       Ping keeperd (liveness check)
  version                    Print CLI version`)
}

// ---- Client ----------------------------------------------------------------

type client struct {
	conn net.Conn
	seq  switchboard.SeqCounter
}

func (c *client) send(msgType switchboard.MsgType, payload []byte) error {
	hdr := switchboard.SwarmHeader{
		Version: 0,
		Type:    msgType,
		FromID:  ctl.CtlIdentity,
		ToID:    "keeper",
		SeqNo:   c.seq.Next(),
	}
	return switchboard.WriteFrame(c.conn, hdr, payload)
}

func (c *client) recv() (switchboard.SwarmHeader, []byte, error) {
	return switchboard.ReadFrame(c.conn)
}

// ---- Commands --------------------------------------------------------------

func (c *client) cmdPing() {
	start := time.Now()
	if err := c.send(switchboard.MsgType_Ping, nil); err != nil {
		fatal("send ping: %v", err)
	}
	hdr, _, err := c.recv()
	if err != nil {
		fatal("recv pong: %v", err)
	}
	if hdr.Type != switchboard.MsgType_Pong {
		fatal("expected Pong, got %s", hdr.Type)
	}
	fmt.Printf("pong from %s in %v\n", hdr.FromID, time.Since(start).Round(time.Microsecond))
}

func (c *client) cmdStatus() {
	if err := c.send(switchboard.MsgType_CtlStatus, nil); err != nil {
		fatal("send: %v", err)
	}
	_, payload, err := c.recv()
	if err != nil {
		fatal("recv: %v", err)
	}
	var status ctl.StatusPayload
	if err := status.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		fatal("decode: %v", err)
	}

	fmt.Printf("keeperd %s  uptime %s\n\n", status.DaemonVersion, fmtDuration(time.Duration(status.UptimeSeconds)*time.Second))
	if len(status.Agents) == 0 {
		fmt.Println("no agents provisioned")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATE\tLAST PROMPT\tLAST EVENT")
	for _, a := range status.Agents {
		fmt.Fprintf(tw, "%s\t%s\t%d\t%s\n", a.ID, a.State, a.LastPromptSeq, a.LastEventAt)
	}
	tw.Flush()
}

func (c *client) cmdAgentList() {
	if err := c.send(switchboard.MsgType_CtlAgentList, nil); err != nil {
		fatal("send: %v", err)
	}
	_, payload, err := c.recv()
	if err != nil {
		fatal("recv: %v", err)
	}
	var list ctl.AgentListPayload
	if err := list.UnmarshalMUS(bytes.NewReader(payload)); err != nil {
		fatal("decode: %v", err)
	}
	if len(list.Agents) == 0 {
		fmt.Println("no agents")
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "AGENT\tSTATE")
	for _, a := range list.Agents {
		fmt.Fprintf(tw, "%s\t%s\n", a.ID, a.State)
	}
	tw.Flush()
}

func (c *client) cmdAgentCreate(args []string) {
	fs := flag.NewFlagSet("agent create", flag.ExitOnError)
	id := fs.String("id", "", "agent ID (required)")
	template := fs.String("template", "", "btrfs template path (required)")
	provider := fs.String("provider", "", "LLM provider name from providers.kdl (e.g. anthropic)")
	_ = fs.Parse(args)

	if *id == "" || *template == "" {
		fmt.Fprintln(os.Stderr, "viv agent create --id <id> --template <path> [--provider <name>]")
		os.Exit(1)
	}

	payload := ctl.AgentCreatePayload{ID: *id, Template: *template, Provider: *provider}
	if err := c.send(switchboard.MsgType_CtlAgentCreate, payload.MarshalMUS()); err != nil {
		fatal("send: %v", err)
	}
	_, respPayload, err := c.recv()
	if err != nil {
		fatal("recv: %v", err)
	}
	expectOK(respPayload)
}

func (c *client) cmdAgentDestroy(args []string) {
	fs := flag.NewFlagSet("agent destroy", flag.ExitOnError)
	id := fs.String("id", "", "agent ID (required)")
	_ = fs.Parse(args)
	if *id == "" {
		fmt.Fprintln(os.Stderr, "viv agent destroy --id <id>")
		os.Exit(1)
	}
	payload := ctl.AgentDestroyPayload{ID: *id}
	if err := c.send(switchboard.MsgType_CtlAgentDestroy, payload.MarshalMUS()); err != nil {
		fatal("send: %v", err)
	}
	_, respPayload, err := c.recv()
	if err != nil {
		fatal("recv: %v", err)
	}
	expectOK(respPayload)
}

func (c *client) cmdPrompt(args []string) {
	fs := flag.NewFlagSet("prompt", flag.ExitOnError)
	agentID := fs.String("agent", "", "target agent ID")
	seq := fs.Uint64("seq", 0, "prompt sequence number")
	_ = fs.Parse(args)

	text := strings.Join(fs.Args(), " ")
	if *agentID == "" || text == "" {
		fmt.Fprintln(os.Stderr, "vivary prompt --agent <id> [--seq <n>] <text>")
		os.Exit(1)
	}
	if *seq == 0 {
		*seq = uint64(time.Now().UnixMilli())
	}

	payload := ctl.PromptPayload{AgentID: *agentID, Seq: *seq, Text: text}
	if err := c.send(switchboard.MsgType_CtlPrompt, payload.MarshalMUS()); err != nil {
		fatal("send: %v", err)
	}
	_, respPayload, err := c.recv()
	if err != nil {
		fatal("recv: %v", err)
	}
	expectOK(respPayload)
}

// ---- helpers ---------------------------------------------------------------

func dialKeeper(socketPath string) (net.Conn, error) {
	return net.DialTimeout("unix", socketPath, 3*time.Second)
}

func mustDialClient(socketPath string) *client {
	conn, err := dialKeeper(socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vivary: cannot connect to keeperd at %q: %v\n", socketPath, err)
		fmt.Fprintln(os.Stderr, "  Is keeperd running?  Try: keeperd --workspace <path>")
		os.Exit(1)
	}
	return &client{conn: conn}
}

func expectOK(payload []byte) {
	if len(payload) == 0 {
		fatal("empty response from keeperd")
	}
	if payload[0] == 1 {
		fmt.Println("ok")
		return
	}
	r := bytes.NewReader(payload[1:])
	errStr, err := mus.ReadString(r, 4096)
	if err != nil {
		fatal("failed to decode error: %v (raw: %q)", err, payload)
	}
	fatal("error: %s", errStr)
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "vivary: "+format+"\n", a...)
	os.Exit(1)
}

func fmtDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh%dm%ds", h, m, s)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%ds", m, s)
	}
	return fmt.Sprintf("%ds", s)
}
