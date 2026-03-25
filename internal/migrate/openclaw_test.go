package migrate

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInspectOpenClaw_BasicDiscovery(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "skills", "image-lab"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "credentials", "whatsapp", "work"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "extensions"), 0o755); err != nil {
		t.Fatal(err)
	}
	config := `{
		"channels": {
			"telegram": {"botToken": "123:abc"},
			"slack": {"appToken": "xapp-123", "botToken": "$SLACK_BOT_TOKEN", "signingSecret": "shh-secret"}
		},
		"plugins": {
			"entries": {
				"memory-lancedb-pro": {"enabled": true, "config": {"apiKey": "mem-key"}},
				"wechat": {"enabled": true, "config": {"clientId": "wx-app"}}
			}
		}
	}`
	for path, contents := range map[string]string{
		filepath.Join(root, "openclaw.json"):                                 config,
		filepath.Join(root, "skills", "image-lab", "SKILL.md"):               "---\nname: image-lab\ndescription: test\n---\nUse image tools.\n",
		filepath.Join(root, "credentials", "whatsapp", "work", "creds.json"): `{"session":"ok"}`,
		filepath.Join(root, "extensions", "plugin.ts"):                       "export const plugin = true\n",
	} {
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	d, err := InspectOpenClaw(root)
	if err != nil {
		t.Fatalf("InspectOpenClaw: %v", err)
	}
	if !d.ConfigParsed {
		t.Fatal("expected config to parse")
	}
	if len(d.Channels) != 2 {
		t.Fatalf("channels = %d, want 2", len(d.Channels))
	}
	if len(d.Plugins) != 2 {
		t.Fatalf("plugins = %d, want 2", len(d.Plugins))
	}
	if got := findCredentialKind(t, d, "channels.telegram.botToken"); got != "bot_token" {
		t.Fatalf("telegram botToken kind = %q", got)
	}
	if got := findCredentialKind(t, d, "plugins.entries.memory-lancedb-pro.config.apiKey"); got != "api_key" {
		t.Fatalf("memory apiKey kind = %q", got)
	}
	if !hasArtifact(d, "skill") || !hasArtifact(d, "credential_file") || !hasArtifact(d, "extension") {
		t.Fatalf("missing expected artifacts: %#v", d.Artifacts)
	}
	if d.PortabilityCounts["bridge_required"] == 0 {
		t.Fatal("expected bridge_required findings")
	}
}

func TestWriteKDL_RendersFindings(t *testing.T) {
	d := Discovery{
		Source:            "/tmp/openclaw",
		GeneratedAt:       testTime(),
		PortabilityCounts: map[string]int{"portable": 1},
		CategoryCounts:    map[string]int{"Memory": 1},
		Artifacts:         []ArtifactFinding{{Kind: "skill", Path: "skills/x/SKILL.md", Portability: "portable", Reason: "prompt asset"}},
	}
	out := filepath.Join(t.TempDir(), "findings.kdl")
	if err := WriteKDL(out, d); err != nil {
		t.Fatalf("WriteKDL: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatalf("read findings: %v", err)
	}
	text := string(data)
	for _, want := range []string{"migration-discovery \"openclaw\"", "artifact \"skill\"", "portability \"portable\" count=1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("expected KDL to contain %q\n%s", want, text)
		}
	}
}

func findCredentialKind(t *testing.T, d Discovery, ref string) string {
	t.Helper()
	for _, c := range d.Credentials {
		if c.RefPath == ref {
			return c.Kind
		}
	}
	t.Fatalf("credential %q not found", ref)
	return ""
}

func hasArtifact(d Discovery, kind string) bool {
	for _, a := range d.Artifacts {
		if a.Kind == kind {
			return true
		}
	}
	return false
}

func testTime() (t time.Time) {
	return time.Date(2026, 3, 24, 12, 0, 0, 0, time.UTC)
}
