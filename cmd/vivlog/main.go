// vivlog is the audit trail inspection CLI.
//
// It reads the SQLite WAL written by keeperd and provides Unix-style filters
// and output modes for debugging capability failures, protocol issues, and
// prompt runs.
//
// Usage:
//
//	vivlog tail [--n <count>]
//	vivlog show --agent <id>
//	vivlog grep --msg-type <type>
//	vivlog decode --seq <n>
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"vivary.dev/vivary/internal/audit"
	"vivary.dev/vivary/internal/capabilities"
	"vivary.dev/vivary/internal/ctl"
)

func main() {
	dbPath := flag.String("db", "./audit.db", "path to audit.db")
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		printUsage()
		os.Exit(1)
	}

	db, err := audit.Open(*dbPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vivlog: open %q: %v\n", *dbPath, err)
		os.Exit(1)
	}
	defer db.Close()

	l := &logCLI{db: db}

	switch args[0] {
	case "tail":
		l.cmdTail(args[1:])
	case "show":
		l.cmdShow(args[1:])
	case "grep":
		l.cmdGrep(args[1:])
	case "decode":
		l.cmdDecode(args[1:])
	case "security":
		l.cmdSecurity(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand: %s\n", args[0])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `vivlog — VIVARY audit trail inspector

Usage:
  vivlog [--db <path>] <command> [args]

Commands:
  tail [--n <count>]              Show the most recent N frames (default 20)
  show --agent <id>               Show all frames for an agent
  grep --msg-type <type>          Filter by MsgType name
  decode --seq <n>                Decode a specific frame by SeqNo
  security [--agent <id>]         Show security events`)
}

// ---- CLI -------------------------------------------------------------------

type logCLI struct {
	db *audit.DB
}

func (l *logCLI) cmdSecurity(args []string) {
	fs := flag.NewFlagSet("security", flag.ExitOnError)
	agentID := fs.String("agent", "", "agent ID")
	jsonOut := fs.Bool("json", false, "output as JSON lines")
	_ = fs.Parse(args)

	events, err := l.db.QuerySecurityEvents(audit.SecurityEventFilter{Agent: *agentID})
	if err != nil {
		fatal("security: %v", err)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, e := range events {
			_ = enc.Encode(e)
		}
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTS\tAGENT\tKIND\tDETAIL")
	for _, e := range events {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\n",
			e.ID,
			e.Ts.Format("2006-01-02T15:04:05Z"),
			e.Agent,
			e.Kind,
			e.Detail,
		)
	}
	tw.Flush()
}

func (l *logCLI) cmdTail(args []string) {
	fs := flag.NewFlagSet("tail", flag.ExitOnError)
	n := fs.Int("n", 20, "number of records")
	jsonOut := fs.Bool("json", false, "output as JSON lines")
	_ = fs.Parse(args)

	records, err := l.db.Tail(*n)
	if err != nil {
		fatal("tail: %v", err)
	}
	printRecords(records, *jsonOut)
}

func (l *logCLI) cmdShow(args []string) {
	fs := flag.NewFlagSet("show", flag.ExitOnError)
	agentID := fs.String("agent", "", "agent ID")
	jsonOut := fs.Bool("json", false, "output as JSON lines")
	_ = fs.Parse(args)

	if *agentID == "" {
		fmt.Fprintln(os.Stderr, "vivlog show --agent <id>")
		os.Exit(1)
	}

	records, err := l.db.QueryFrames(audit.FrameFilter{AgentID: *agentID})
	if err != nil {
		fatal("show: %v", err)
	}
	printRecords(records, *jsonOut)
}

func (l *logCLI) cmdGrep(args []string) {
	fs := flag.NewFlagSet("grep", flag.ExitOnError)
	msgType := fs.String("msg-type", "", "MsgType name to match")
	agentID := fs.String("agent", "", "optional agent filter")
	jsonOut := fs.Bool("json", false, "output as JSON lines")
	_ = fs.Parse(args)

	if *msgType == "" {
		fmt.Fprintln(os.Stderr, "vivlog grep --msg-type <type>")
		os.Exit(1)
	}

	records, err := l.db.QueryFrames(audit.FrameFilter{
		MsgType: *msgType,
		AgentID: *agentID,
	})
	if err != nil {
		fatal("grep: %v", err)
	}
	printRecords(records, *jsonOut)
}

func (l *logCLI) cmdDecode(args []string) {
	fs := flag.NewFlagSet("decode", flag.ExitOnError)
	seqNo := fs.Uint64("seq", 0, "SeqNo to decode")
	_ = fs.Parse(args)

	if *seqNo == 0 {
		fmt.Fprintln(os.Stderr, "vivlog decode --seq <n>")
		os.Exit(1)
	}

	records, err := l.db.QueryFrames(audit.FrameFilter{SeqNo: *seqNo})
	if err != nil {
		fatal("decode: %v", err)
	}
	if len(records) == 0 {
		fmt.Fprintf(os.Stderr, "no frame found with seq_no=%d\n", *seqNo)
		os.Exit(1)
	}
	// Full decode: pretty-print header fields + payload MUS if available.
	for _, r := range records {
		fmt.Printf("id:       %d\n", r.ID)
		fmt.Printf("ts:       %s\n", r.Ts.Format(time.RFC3339Nano))
		fmt.Printf("msg_type: %s\n", r.MsgType)
		fmt.Printf("from_id:  %s\n", r.FromID)
		fmt.Printf("to_id:    %s\n", r.ToID)
		fmt.Printf("seq_no:   %d\n", r.SeqNo)
		if r.Payload != nil {
			fmt.Println("payload:")
			decoded, err := decodePayload(r.MsgType, r.Payload)
			if err == nil {
				b, _ := json.MarshalIndent(decoded, "  ", "  ")
				fmt.Printf("  %s\n", string(b))
			} else {
				fmt.Printf("  (raw) %q\n", r.Payload)
				fmt.Printf("  (decode error: %v)\n", err)
			}
		} else {
			fmt.Println("payload:  (not stored)")
		}
		fmt.Println()
	}
}

func decodePayload(msgType string, payload []byte) (any, error) {
	r := bytes.NewReader(payload)
	switch msgType {
	case "CtlStatus":
		var p ctl.StatusPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CtlAgentList":
		var p ctl.AgentListPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CtlAgentCreate":
		var p ctl.AgentCreatePayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CtlAgentDestroy":
		var p ctl.AgentDestroyPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CtlPrompt":
		var p ctl.PromptPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CtlApproval":
		var p ctl.ApprovalPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CapabilityRequest":
		var p capabilities.CapabilityRequestPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CapabilityResponse":
		var p capabilities.CapabilityResponsePayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "CompletionEvent":
		var p ctl.CompletionEventPayload
		err := p.UnmarshalMUS(r)
		return p, err
	case "FailureEvent":
		var p ctl.FailureEventPayload
		err := p.UnmarshalMUS(r)
		return p, err
	}
	return nil, fmt.Errorf("unsupported msg_type for decoding: %s", msgType)
}

// ---- Output formatting -----------------------------------------------------

func printRecords(records []audit.FrameRecord, jsonOut bool) {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		for _, r := range records {
			type jsonRecord struct {
				ID      int64           `json:"id"`
				Ts      string          `json:"ts"`
				MsgType string          `json:"msg_type"`
				FromID  string          `json:"from_id"`
				ToID    string          `json:"to_id"`
				SeqNo   uint64          `json:"seq_no"`
				Payload json.RawMessage `json:"payload,omitempty"`
			}
			jr := jsonRecord{
				ID: r.ID, Ts: r.Ts.Format(time.RFC3339Nano),
				MsgType: r.MsgType, FromID: r.FromID, ToID: r.ToID, SeqNo: r.SeqNo,
			}
			if r.Payload != nil {
				jr.Payload = r.Payload
			}
			_ = enc.Encode(jr)
		}
		return
	}

	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "ID\tTS\tTYPE\tFROM\tTO\tSEQ")
	for _, r := range records {
		fmt.Fprintf(tw, "%d\t%s\t%s\t%s\t%s\t%d\n",
			r.ID,
			r.Ts.Format("2006-01-02T15:04:05Z"),
			r.MsgType,
			r.FromID,
			r.ToID,
			r.SeqNo,
		)
	}
	tw.Flush()
}

func fatal(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "vivlog: "+format+"\n", a...)
	os.Exit(1)
}
