package migrate

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Discovery struct {
	Source              string
	GeneratedAt         time.Time
	ConfigPath          string
	ConfigParsed        bool
	TopLevelKeys        []string
	Artifacts           []ArtifactFinding
	Channels            []ChannelFinding
	Plugins             []PluginFinding
	Credentials         []CredentialFinding
	PortabilityCounts   map[string]int
	CategoryCounts      map[string]int
	UnknownConfigFields []string
}

type ArtifactFinding struct {
	Kind        string
	Path        string
	Portability string
	Reason      string
}

type ChannelFinding struct {
	Name        string
	Path        string
	Portability string
	Reason      string
}

type PluginFinding struct {
	Name        string
	Path        string
	Category    string
	Enabled     bool
	Portability string
	Reason      string
}

type CredentialFinding struct {
	RefPath      string
	Owner        string
	Kind         string
	Storage      string
	Portability  string
	Locator      string
	ValuePresent bool
}

func InspectOpenClaw(source string) (Discovery, error) {
	resolved, err := expandPath(source)
	if err != nil {
		return Discovery{}, err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return Discovery{}, fmt.Errorf("inspect openclaw source: %w", err)
	}
	if !info.IsDir() {
		return Discovery{}, fmt.Errorf("inspect openclaw source: %q is not a directory", resolved)
	}

	d := Discovery{
		Source:            resolved,
		GeneratedAt:       time.Now().UTC(),
		PortabilityCounts: map[string]int{},
		CategoryCounts:    map[string]int{},
	}

	if err := d.inspectConfig(); err != nil {
		return Discovery{}, err
	}
	if err := d.inspectFiles(); err != nil {
		return Discovery{}, err
	}

	sort.Slice(d.Artifacts, func(i, j int) bool { return d.Artifacts[i].Path < d.Artifacts[j].Path })
	sort.Slice(d.Channels, func(i, j int) bool { return d.Channels[i].Name < d.Channels[j].Name })
	sort.Slice(d.Plugins, func(i, j int) bool { return d.Plugins[i].Name < d.Plugins[j].Name })
	sort.Slice(d.Credentials, func(i, j int) bool { return d.Credentials[i].RefPath < d.Credentials[j].RefPath })
	sort.Strings(d.TopLevelKeys)
	sort.Strings(d.UnknownConfigFields)
	return d, nil
}

func WriteKDL(path string, d Discovery) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir findings dir: %w", err)
	}
	return os.WriteFile(path, []byte(renderKDL(d)), 0o644)
}

func expandPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("empty path")
	}
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home dir: %w", err)
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}
	return filepath.Abs(path)
}

func (d *Discovery) inspectConfig() error {
	configPath := filepath.Join(d.Source, "openclaw.json")
	data, err := os.ReadFile(configPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read openclaw.json: %w", err)
	}
	d.ConfigPath = configPath
	d.addArtifact(ArtifactFinding{
		Kind:        "config",
		Path:        filepath.Base(configPath),
		Portability: "portable_with_review",
		Reason:      "primary OpenClaw declarative config",
	})

	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		d.UnknownConfigFields = append(d.UnknownConfigFields, "openclaw.json parse failed")
		return nil
	}
	d.ConfigParsed = true
	for k := range root {
		d.TopLevelKeys = append(d.TopLevelKeys, k)
	}

	for _, k := range d.TopLevelKeys {
		switch k {
		case "agents", "channels", "commands", "models", "plugins", "skills", "webhooks", "gateway", "memory", "automation":
		default:
			d.UnknownConfigFields = append(d.UnknownConfigFields, k)
		}
	}

	if channels, ok := asMap(root["channels"]); ok {
		for name := range channels {
			d.Channels = append(d.Channels, ChannelFinding{
				Name:        name,
				Path:        "channels." + name,
				Portability: "bridge_required",
				Reason:      "chat/channel ingress should remain outside the VIVARY runtime core",
			})
			value := channels[name]
			d.scanCredentialRefs(value, "channels."+name, "channel:"+name)
		}
	}

	if pluginsRoot, ok := asMap(root["plugins"]); ok {
		if entries, ok := asMap(pluginsRoot["entries"]); ok {
			for name, raw := range entries {
				entry, _ := asMap(raw)
				enabled := true
				if v, ok := entry["enabled"].(bool); ok {
					enabled = v
				}
				category, portability, reason := classifyPlugin(name)
				d.Plugins = append(d.Plugins, PluginFinding{
					Name:        name,
					Path:        "plugins.entries." + name,
					Category:    category,
					Enabled:     enabled,
					Portability: portability,
					Reason:      reason,
				})
				d.CategoryCounts[category]++
				d.scanCredentialRefs(raw, "plugins.entries."+name, "plugin:"+name)
			}
		}
	}

	if modelsRoot, ok := asMap(root["models"]); ok {
		if providers, ok := asMap(modelsRoot["providers"]); ok {
			for name, raw := range providers {
				d.scanCredentialRefs(raw, "models.providers."+name, "provider:"+name)
			}
		}
	}

	for k, v := range root {
		switch k {
		case "channels", "plugins", "models":
			continue
		default:
			d.scanCredentialRefs(v, k, "config:"+k)
		}
	}

	return nil
}

func (d *Discovery) inspectFiles() error {
	return filepath.WalkDir(d.Source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := entry.Name()
		if entry.IsDir() {
			switch name {
			case ".git", "node_modules":
				return filepath.SkipDir
			}
			return nil
		}

		rel, err := filepath.Rel(d.Source, path)
		if err != nil {
			return err
		}
		lowerRel := strings.ToLower(filepath.ToSlash(rel))

		switch {
		case name == "SKILL.md":
			d.addArtifact(ArtifactFinding{Kind: "skill", Path: rel, Portability: "portable", Reason: "skill prompt asset can be imported as a VIVARY prompt fragment"})
		case strings.Contains(lowerRel, "/credentials/") || strings.HasPrefix(lowerRel, "credentials/"):
			d.addArtifact(ArtifactFinding{Kind: "credential_file", Path: rel, Portability: "portable_with_review", Reason: "credential-bearing file should be inventoried and redacted, not copied into prompts"})
			if strings.HasSuffix(lowerRel, "/creds.json") {
				d.Credentials = append(d.Credentials, CredentialFinding{
					RefPath:      filepath.ToSlash(rel),
					Owner:        credentialOwnerFromPath(lowerRel),
					Kind:         "device_pairing_session",
					Storage:      "credential_file",
					Portability:  "bridge_required",
					Locator:      filepath.ToSlash(rel),
					ValuePresent: true,
				})
				d.PortabilityCounts["bridge_required"]++
			}
		case strings.Contains(lowerRel, "/extensions/") || strings.HasPrefix(lowerRel, "extensions/"):
			if strings.HasSuffix(lowerRel, ".ts") || strings.HasSuffix(lowerRel, ".js") {
				d.addArtifact(ArtifactFinding{Kind: "extension", Path: rel, Portability: "unsupported", Reason: "workspace/global extension code is not imported into VIVARY core runtime"})
			}
		case strings.Contains(lowerRel, "standing") && strings.HasSuffix(lowerRel, ".md"):
			d.addArtifact(ArtifactFinding{Kind: "standing_order", Path: rel, Portability: "portable_with_review", Reason: "standing-order docs can become keeperd-managed schedules"})
		case strings.Contains(lowerRel, "slash") && (strings.HasSuffix(lowerRel, ".md") || strings.HasSuffix(lowerRel, ".json") || strings.HasSuffix(lowerRel, ".yaml") || strings.HasSuffix(lowerRel, ".yml")):
			d.addArtifact(ArtifactFinding{Kind: "slash_command", Path: rel, Portability: "portable", Reason: "slash command assets can become VIVARY prompt macros"})
		case strings.Contains(lowerRel, "/plugins/") && strings.HasSuffix(lowerRel, ".json"):
			d.addArtifact(ArtifactFinding{Kind: "plugin_manifest", Path: rel, Portability: "portable_with_review", Reason: "plugin metadata is migratable even when plugin code is not"})
		}
		return nil
	})
}

func (d *Discovery) addArtifact(a ArtifactFinding) {
	d.Artifacts = append(d.Artifacts, a)
	d.PortabilityCounts[a.Portability]++
}

func (d *Discovery) scanCredentialRefs(v any, path, owner string) {
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			next := path
			if next != "" {
				next += "."
			}
			next += k
			if kind, ok := classifyCredentialKey(k, next); ok {
				storage, locator, present := classifyCredentialValue(child)
				d.Credentials = append(d.Credentials, CredentialFinding{
					RefPath:      next,
					Owner:        owner,
					Kind:         kind,
					Storage:      storage,
					Portability:  credentialPortability(kind),
					Locator:      locator,
					ValuePresent: present,
				})
				d.PortabilityCounts[credentialPortability(kind)]++
			}
			d.scanCredentialRefs(child, next, owner)
		}
	case []any:
		for i, child := range x {
			d.scanCredentialRefs(child, fmt.Sprintf("%s[%d]", path, i), owner)
		}
	}
}

func classifyCredentialKey(key, path string) (string, bool) {
	lower := strings.ToLower(key)
	switch {
	case strings.Contains(lower, "bottoken") || strings.Contains(lower, "bot_token") || strings.HasSuffix(lower, "bottoken"):
		return "bot_token", true
	case strings.Contains(lower, "apptoken") || strings.Contains(lower, "app_token"):
		return "app_token", true
	case strings.Contains(lower, "apikey") || strings.Contains(lower, "api_key"):
		return "api_key", true
	case strings.Contains(lower, "signingsecret") || strings.Contains(lower, "signing_secret"):
		return "signing_secret", true
	case strings.Contains(lower, "webhooksecret") || strings.Contains(lower, "webhook_secret"):
		return "webhook_secret", true
	case strings.Contains(lower, "password"):
		return "password", true
	case strings.Contains(lower, "clientsecret") || strings.Contains(lower, "client_secret"):
		return "shared_secret", true
	case strings.Contains(lower, "clientid") || strings.Contains(lower, "client_id") || strings.Contains(lower, "connectionid"):
		return "client_id_or_app_id", true
	case strings.Contains(lower, "authtoken") || strings.Contains(lower, "auth_token"):
		if strings.Contains(strings.ToLower(path), "twilio") {
			return "account_sid_auth_token", true
		}
		return "shared_secret", true
	case strings.Contains(lower, "token"):
		if strings.Contains(strings.ToLower(path), "gateway") {
			return "gateway_access_token", true
		}
		return "oauth_access_token", true
	case lower == "cookie" || strings.Contains(lower, "cookie"):
		return "cookie_session", true
	case strings.Contains(lower, "aws_profile"):
		return "cloud_profile_reference", true
	case strings.Contains(lower, "aws_access_key_id") || strings.Contains(lower, "aws_secret_access_key"):
		return "cloud_access_key_pair", true
	case strings.Contains(lower, "aws_bearer_token"):
		return "cloud_bearer_token", true
	default:
		return "", false
	}
}

func classifyCredentialValue(v any) (storage, locator string, present bool) {
	s := strings.TrimSpace(fmt.Sprint(v))
	if s == "<nil>" || s == "" {
		return "unknown", "", false
	}
	if strings.HasPrefix(s, "${") && strings.HasSuffix(s, "}") {
		return "env_var", s, true
	}
	if strings.HasPrefix(s, "$") && len(s) > 1 {
		return "env_var", s, true
	}
	if strings.Contains(s, "/") && strings.Contains(strings.ToLower(s), "credential") {
		return "credential_file", s, true
	}
	return "inline", redactValue(s), true
}

func credentialPortability(kind string) string {
	switch kind {
	case "device_pairing_session", "qr_login_session", "cookie_session":
		return "bridge_required"
	case "cloud_profile_reference", "cloud_bearer_token", "cloud_access_key_pair":
		return "portable_with_review"
	default:
		return "portable_with_review"
	}
}

func credentialOwnerFromPath(lowerRel string) string {
	if strings.Contains(lowerRel, "credentials/whatsapp/") {
		return "channel:whatsapp"
	}
	return "credential_store"
}

func classifyPlugin(name string) (category, portability, reason string) {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "memory") || strings.Contains(lower, "mem"):
		return "Memory", "portable_with_review", "memory plugins may map to Memory capabilities or archive import"
	case strings.Contains(lower, "search") || strings.Contains(lower, "tavily") || strings.Contains(lower, "searx"):
		return "Search", "portable_with_review", "search plugins often map to Search capabilities"
	case strings.Contains(lower, "vault") || strings.Contains(lower, "obsidian"):
		return "Filesystem", "portable_with_review", "vault plugins may be imported as files plus filesystem/document scopes"
	case strings.Contains(lower, "browser"):
		return "Browser", "bridge_required", "browser plugins need review against the host-side chrome proxy model"
	case strings.Contains(lower, "slack") || strings.Contains(lower, "discord") || strings.Contains(lower, "telegram") || strings.Contains(lower, "whatsapp") || strings.Contains(lower, "wechat") || strings.Contains(lower, "wecom") || strings.Contains(lower, "qq") || strings.Contains(lower, "dingtalk") || strings.Contains(lower, "xianyu"):
		return "Messaging", "bridge_required", "chat/channel plugins should migrate as ingress bridges, not in-core plugins"
	default:
		return "Unknown", "unsupported", "dynamic plugin code has no safe in-core migration path yet"
	}
}

func asMap(v any) (map[string]any, bool) {
	m, ok := v.(map[string]any)
	return m, ok
}

func redactValue(s string) string {
	if len(s) <= 8 {
		return "present"
	}
	return s[:4] + "..." + s[len(s)-2:]
}

func renderKDL(d Discovery) string {
	var b strings.Builder
	b.WriteString("// Generated by viv migrate openclaw inspect\n")
	b.WriteString("migration-discovery \"openclaw\" {\n")
	fmt.Fprintf(&b, "    format-version %d\n", 1)
	fmt.Fprintf(&b, "    generated-at %q\n", d.GeneratedAt.Format(time.RFC3339))
	fmt.Fprintf(&b, "    source path=%q\n", d.Source)
	if d.ConfigPath != "" {
		fmt.Fprintf(&b, "    config path=%q parsed=%t\n", d.ConfigPath, d.ConfigParsed)
	}
	fmt.Fprintf(&b, "    summary artifacts=%d channels=%d plugins=%d credentials=%d\n", len(d.Artifacts), len(d.Channels), len(d.Plugins), len(d.Credentials))
	writeCountNodes(&b, "portability", d.PortabilityCounts)
	writeCountNodes(&b, "plugin-category", d.CategoryCounts)
	for _, key := range d.TopLevelKeys {
		fmt.Fprintf(&b, "    top-level-key %q\n", key)
	}
	for _, key := range d.UnknownConfigFields {
		fmt.Fprintf(&b, "    unknown-config-field %q\n", key)
	}
	for _, a := range d.Artifacts {
		fmt.Fprintf(&b, "    artifact %q path=%q portability=%q reason=%q\n", a.Kind, filepath.ToSlash(a.Path), a.Portability, a.Reason)
	}
	for _, c := range d.Channels {
		fmt.Fprintf(&b, "    channel %q path=%q portability=%q reason=%q\n", c.Name, c.Path, c.Portability, c.Reason)
	}
	for _, p := range d.Plugins {
		fmt.Fprintf(&b, "    plugin %q path=%q category=%q enabled=%t portability=%q reason=%q\n", p.Name, p.Path, p.Category, p.Enabled, p.Portability, p.Reason)
	}
	for _, c := range d.Credentials {
		fmt.Fprintf(&b, "    credential %q owner=%q kind=%q storage=%q portability=%q locator=%q present=%t\n", c.RefPath, c.Owner, c.Kind, c.Storage, c.Portability, c.Locator, c.ValuePresent)
	}
	b.WriteString("}\n")
	return b.String()
}

func writeCountNodes(b *strings.Builder, name string, counts map[string]int) {
	keys := make([]string, 0, len(counts))
	for k, v := range counts {
		if v > 0 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "    %s %q count=%d\n", name, k, counts[k])
	}
}
