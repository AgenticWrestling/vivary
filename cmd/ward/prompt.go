package main

// prompt.go generates the system prompt injected into every LLM subprocess.
//
// The Ward reads agent.kdl from the container root at startup to discover
// which capabilities are granted to this agent, then builds a static prompt
// that lists each capability with its full JSON schema.  This tells the LLM:
//   - Which tools are available
//   - Their argument schemas (types, required fields, defaults)
//   - The calling convention (CLI flags: --field value)
//
// The prompt is generated once at startup and re-used for every run so that
// Claude Code does not need a schema-discovery round-trip on each prompt.

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	kdl "github.com/sblinch/kdl-go"
	"vivary.dev/vivary/internal/config"
)

const systemPromptHeader = `You are a Vivary AI agent running inside an isolated container.

You have access to a set of capability tools installed as CLI commands.
Call each tool by running its name as a shell command with "--field value" arguments.
On error, a tool writes a description to stderr and exits non-zero.
Write all output files using Filesystem_File_Write.

Available capabilities:

`

const systemPromptFooter = `
Complete your task and then stop.
Do not ask follow-up questions unless the task explicitly requires interaction.
`

// buildSystemPrompt reads agent.kdl from kdlPath, looks up each capability's
// schema in the global capRegistry, and returns the system prompt string.
//
// If agent.kdl cannot be read or contains no capabilities the Ward falls back
// to an empty system prompt — the agent can still run but Claude won't know
// about available tools.
func loadAgentPromptConfig(kdlPath string) (string, int) {
	data, err := os.ReadFile(kdlPath)
	if err != nil {
		return "", 1 // no agent.kdl — skip tool injection (dev/test path)
	}

	cfg, err := config.ParseAgentKDL(data)
	if err != nil {
		return "", 1
	}

	doc, err := kdl.Parse(bytes.NewReader(data))
	if err != nil {
		return "", cfg.SchemaErrorRetries
	}

	var capNames []string
	for _, node := range doc.Nodes {
		if node.Name.String() != "capabilities" {
			continue
		}
		for _, child := range node.Children {
			capNames = append(capNames, child.Name.String())
		}
	}

	if len(capNames) == 0 {
		return "", cfg.SchemaErrorRetries
	}

	var sb strings.Builder
	sb.WriteString(systemPromptHeader)

	for _, name := range capNames {
		schema, ok := capRegistry[name]
		if !ok {
			// Capability is granted but schema is unknown — list name only.
			fmt.Fprintf(&sb, "### %s\n(schema unavailable — call with --help for usage)\n\n", name)
			continue
		}
		fmt.Fprintf(&sb, "### %s\n```json\n%s\n```\n\n", name, schema)
	}

	sb.WriteString(systemPromptFooter)
	return sb.String(), cfg.SchemaErrorRetries
}

func buildSystemPrompt(kdlPath string) string {
	prompt, _ := loadAgentPromptConfig(kdlPath)
	return prompt
}
