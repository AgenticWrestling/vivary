package main

import (
	"bytes"
	"fmt"

	"vivary.dev/vivary/internal/capabilities"
)

func validateToolArgs(capability string, args []byte) error {
	payload, ok := capabilities.NewArgumentPayload(capability)
	if !ok {
		return nil
	}
	r := bytes.NewReader(args)
	if err := payload.UnmarshalMUS(r); err != nil {
		return fmt.Errorf("args must satisfy %s schema: %w", capability, err)
	}
	if r.Len() != 0 {
		return fmt.Errorf("args must satisfy %s schema: trailing payload bytes", capability)
	}
	return nil
}
