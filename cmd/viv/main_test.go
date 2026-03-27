package main

import (
	"fmt"
	"strings"
	"testing"

	"vivary.dev/vivary/internal/ctl"
)

func TestFormatStatus_NoAgents(t *testing.T) {
	out := formatStatus(ctl.StatusPayload{
		DaemonVersion: "0.1.0-test",
		UptimeSeconds: 5,
	})

	if !strings.Contains(out, "keeperd 0.1.0-test  uptime 5s") {
		t.Fatalf("missing daemon header: %q", out)
	}
	if !strings.Contains(out, "no agents provisioned") {
		t.Fatalf("missing empty-state text: %q", out)
	}
}

func TestFormatStatus_IncludesKeeperOwnedRuntimeFields(t *testing.T) {
	out := formatStatus(ctl.StatusPayload{
		DaemonVersion: "0.1.0-test",
		UptimeSeconds: 125,
		Agents: []ctl.AgentStatus{{
			ID:            "agent-1",
			State:         "running",
			LastPromptSeq: 7,
			LastOutcome:   "success",
			InputTokens:   13,
			OutputTokens:  21,
			CostUSD:       "0.004200",
			ToolCalls:     3,
			LastEventAt:   "2026-03-24T12:00:00Z",
		}},
	})

	for _, want := range []string{
		"AGENT",
		"OUTCOME",
		"TOKENS",
		"COST",
		"TOOLS",
		"agent-1",
		"success",
		"in=13 out=21",
		"0.004200",
		"2026-03-24T12:00:00Z",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("formatted status missing %q: %q", want, out)
		}
	}
}

func TestFormatStatus_UsesFallbacksForMissingRuntimeFields(t *testing.T) {
	out := formatStatus(ctl.StatusPayload{
		DaemonVersion: "0.1.0-test",
		Agents: []ctl.AgentStatus{{
			ID:    "agent-2",
			State: "idle",
		}},
	})

	if !strings.Contains(out, "agent-2") {
		t.Fatalf("missing agent row: %q", out)
	}
	if !strings.Contains(out, "-") {
		t.Fatalf("expected placeholder fallback values: %q", out)
	}
	if !strings.Contains(out, "0") {
		t.Fatalf("expected zero-value numeric fields to remain visible: %q", out)
	}
}

func TestFormatStatus_SnapshotForCompletionFields(t *testing.T) {
	status := ctl.StatusPayload{
		DaemonVersion: "0.1.0-test",
		UptimeSeconds: 125,
		Agents: []ctl.AgentStatus{{
			ID:            "agent-a",
			State:         "running",
			LastPromptSeq: 23,
			LastOutcome:   "success",
			InputTokens:   120,
			OutputTokens:  55,
			CostUSD:       "0.002500",
			ToolCalls:     4,
			LastEventAt:   "2026-03-24T12:34:56Z",
		}},
	}

	want := strings.Join([]string{
		fmt.Sprintf("keeperd %s  uptime 2m5s", status.DaemonVersion),
		"",
		"AGENT    STATE    MODEL  LAST PROMPT  OUTCOME  TOKENS         COST      TOOLS  LAST EVENT",
		"agent-a  running  -      23           success  in=120 out=55  0.002500  4      2026-03-24T12:34:56Z",
		"",
	}, "\n")

	if got := formatStatus(status); got != want {
		t.Fatalf("formatStatus mismatch\nwant:\n%s\n got:\n%s", want, got)
	}
}
