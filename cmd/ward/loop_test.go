package main

import (
	"encoding/json"
	"testing"
)

// TestWardLoop verifies that the loop detector triggers at the configured
// threshold and that distinct (capability, args) pairs do not interfere.
func TestWardLoop_TriggerAtThreshold(t *testing.T) {
	ld := &loopDetector{threshold: 3}
	args := json.RawMessage(`{"url":"https://example.com"}`)

	for i := range 2 {
		if ld.check("Browser_Page_Read", args) {
			t.Fatalf("loop triggered too early at iteration %d", i)
		}
	}
	// Third call should trigger.
	if !ld.check("Browser_Page_Read", args) {
		t.Fatal("loop should have triggered at threshold")
	}
}

func TestWardLoop_DistinctArgsNoTrigger(t *testing.T) {
	ld := &loopDetector{threshold: 2}
	for i := range 5 {
		args, _ := json.Marshal(map[string]int{"n": i})
		if ld.check("MyTool", args) {
			t.Fatalf("loop incorrectly triggered for distinct args at i=%d", i)
		}
	}
}

func TestWardLoop_DistinctCapabilitiesNoTrigger(t *testing.T) {
	ld := &loopDetector{threshold: 2}
	args := json.RawMessage(`{}`)
	caps := []string{"Cap_A_Do", "Cap_B_Do", "Cap_C_Do"}
	for _, cap := range caps {
		if ld.check(cap, args) {
			t.Fatalf("loop incorrectly triggered for capability %s", cap)
		}
	}
}
