package agent

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/feature"
	"github.com/nao23/mbx/internal/mcpgateway"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/shell"
)

//go:embed sandbox-prompt.md
var sandboxPrompt string

type claude struct {
	cfg       *config.Config
	telemetry []string // OTEL endpoint hosts
}

func newClaude(cfg *config.Config) *claude {
	return &claude{cfg: cfg, telemetry: telemetryHosts(cfg)}
}

func claudeSecret(cfg *config.Config) *feature.Secret {
	return &feature.Secret{Env: "CLAUDE_CODE_OAUTH_TOKEN", Hosts: []string{"api.anthropic.com"}, Source: cfg.Claude.SecretSource}
}

func (c *claude) Feature(workspace string, gateway *mcpgateway.Registration) feature.Feature {
	f := feature.Feature{
		Name:    "claude",
		Secret:  claudeSecret(c.cfg),
		Allow:   append([]string{"platform.claude.com"}, c.telemetry...),
		Flags:   c.flags(workspace),
		Patches: c.patches(workspace, gateway),
	}
	if gateway != nil {
		port := c.cfg.MCPGateway.Port
		f.Flags = append(f.Flags, msb.Flag{
			Name:  "--vsock",
			Value: mcpgateway.SocketPath(gateway.ID) + ":" + strconv.Itoa(port),
			Note:  "The MCP gateway's socket for this sandbox, at a vsock port",
		})
		f.Setup = &feature.Setup{
			Comment: fmt.Sprintf("Relay 127.0.0.1:%d to the MCP gateway's socket (vsock port %d)", port, port),
			Script:  mcpgateway.BridgeScript(port),
		}
	}
	return f
}

func (c *claude) MCPServers() []string {
	servers := slices.Clone(c.cfg.Claude.MCPServers)
	for _, display := range c.cfg.Claude.ClaudeAIConnectors {
		servers = append(servers, mcpgateway.ClaudeAIPrefix+display)
	}
	return servers
}

func (c *claude) flags(workspace string) []msb.Flag {
	cc := c.cfg.Claude
	flags := []msb.Flag{{
		Name:  "--env",
		Value: "CLAUDE_CONFIG_DIR=" + cc.ConfigDir,
		Note:  "Claude Code config dir (lives in the VM and is removed with the sandbox)",
	}}
	host := config.HostClaudeDir()
	if cc.HostShare.Sessions {
		// projects is mounted whole, as msb doesn't create a missing mount source and the workspace's
		// directory appears only when a session starts
		for _, p := range sharedSessionPaths {
			flags = append(flags, msb.Flag{
				Name:  "--volume",
				Value: filepath.Join(host, p) + ":" + cc.ConfigDir + "/" + p,
				Note:  "Session history shared with the host (read-write)",
			})
		}
	}
	if cc.HostShare.Config {
		// Read-only so claude in the VM can't carry changes back to the host
		for _, f := range []string{"CLAUDE.md", "settings.json"} {
			if src := filepath.Join(host, f); fileExists(src) {
				flags = append(flags, msb.Flag{Name: "--volume", Value: src + ":" + cc.ConfigDir + "/" + f + ":ro", Note: "Host config (read-only)"})
			}
		}
	}
	skills := hostSkills()
	for _, name := range c.sharedSkills(skills) {
		flags = append(flags, msb.Flag{
			Name:  "--volume",
			Value: skills[name] + ":" + cc.ConfigDir + "/skills/" + name + ":ro",
			Note:  "Skills shared with the host (read-only)",
		})
	}
	// The consent to the org's remote settings given on the host skips the prompt in the VM. Read-only so
	// the VM can't consent itself
	if consent := filepath.Join(host, "remote-settings-consent.json"); fileExists(consent) {
		flags = append(flags, msb.Flag{
			Name:  "--volume",
			Value: consent + ":" + cc.ConfigDir + "/remote-settings-consent.json:ro",
			Note:  "Consent to org settings (given on the host, read-only)",
		})
	}
	return flags
}

// hostSkills returns the skills in the host's ~/.claude/skills (directories with SKILL.md) by name, with
// symlinks resolved, as a symlink to outside ~/.claude would dangle in the VM.
func hostSkills() map[string]string {
	skills := map[string]string{}
	entries, _ := os.ReadDir(filepath.Join(config.HostClaudeDir(), "skills"))
	for _, e := range entries {
		dir, err := filepath.EvalSymlinks(filepath.Join(config.HostClaudeDir(), "skills", e.Name()))
		if err == nil && fileExists(filepath.Join(dir, "SKILL.md")) {
			skills[e.Name()] = dir
		}
	}
	return skills
}

// sharedSkills returns the names in host_share.skills ("*" for all) that are host skills, sorted. Others
// are skipped (Prepare warns), as a skill may be on one machine and not another.
func (c *claude) sharedSkills(skills map[string]string) []string {
	var names []string
	for _, name := range c.cfg.Claude.HostShare.Skills {
		if name == "*" {
			names = slices.Collect(maps.Keys(skills))
			break
		}
		if _, ok := skills[name]; ok {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// Prepare creates the shared parts of the host's ~/.claude that are missing, as msb doesn't create a
// missing mount source.
func (c *claude) Prepare(r *msb.Runner, workspace string) error {
	skills := hostSkills()
	for _, name := range c.cfg.Claude.HostShare.Skills {
		if _, ok := skills[name]; !ok && name != "*" {
			warn("claude.host_share.skills: no skill " + name + " in ~/.claude/skills; not shared")
		}
	}
	if !c.cfg.Claude.HostShare.Sessions {
		return nil
	}
	host := config.HostClaudeDir()
	r.Step(
		msb.OnHost,
		"Check the shared session history in the host's ~/.claude",
		"Mounted read-write into the VM; it all exists once the host's claude has been used",
	)
	var missing []string
	for _, p := range sharedSessionPaths {
		if !r.Check(filepath.Join(host, p)) {
			missing = append(missing, p)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	r.Step(msb.OnHost, "Create the missing parts of the shared session history")
	for _, p := range missing {
		path := filepath.Join(host, p)
		var err error
		if p == "history.jsonl" {
			err = r.Touch(path)
		} else {
			err = r.MkdirAll(path)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

var sharedSessionPaths = []string{"projects", "file-history", "history.jsonl"}

// patches seeds claude's state, once at creation so claude's own updates aren't overwritten.
func (c *claude) patches(workspace string, gateway *mcpgateway.Registration) []any {
	cc := c.cfg.Claude
	patches := []any{map[string]any{"text": map[string]any{
		"path": cc.ConfigDir + "/.claude.json", "content": c.claudeJSONSeed(workspace, gateway)}}}
	// Without the org settings cache, claude asks for consent again even though it was given on the host
	for _, f := range []string{
		"remote-settings.json",
		"remote-settings.json.signature.json",
		"remote-settings.json.signature-iat.json",
		"remote-settings.json.synced-at",
	} {
		if src := filepath.Join(config.HostClaudeDir(), f); fileExists(src) {
			patches = append(patches, map[string]any{"copy_file": map[string]any{
				"src": src, "dst": cc.ConfigDir + "/" + f}})
		}
	}
	return patches
}

// claudeJSONSeed returns the VM's .claude.json: only the host state that skips startup prompts
// (onboarding, workspace trust), plus the MCP servers.
func (c *claude) claudeJSONSeed(workspace string, gateway *mcpgateway.Registration) string {
	seed := map[string]any{}
	data, err := os.ReadFile(config.HostClaudeJSON())
	if err == nil {
		var host map[string]any
		if err := json.Unmarshal(data, &host); err != nil {
			warn("host .claude.json not readable; claude may show its first-run prompts: " + err.Error())
		}
		for _, k := range []string{"hasCompletedOnboarding", "lastOnboardingVersion", "hasSeenAutoModeEntryWarning"} {
			if v, ok := host[k]; ok {
				seed[k] = v
			}
		}
		if hostTrusts(host, workspace) {
			seed["projects"] = map[string]any{workspace: map[string]any{"hasTrustDialogAccepted": true}}
		}
	}
	if gateway != nil {
		servers := map[string]any{}
		for _, s := range gateway.Servers {
			servers[mcpgateway.VMServerName(s)] = map[string]any{"type": "http", "url": mcpgateway.URL(c.cfg.MCPGateway.Port, s)}
		}
		seed["mcpServers"] = servers
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(seed)
	return buf.String()
}

func (c *claude) Command(r *msb.Runner, args []string) ([]string, string) {
	r.Step(msb.OnVM, "Start claude", "The global CLAUDE.md is written for the host, so append a description of the sandbox to the system prompt")
	// dry-run shows where the long prompt comes from instead of its contents
	prompt, shownPrompt := sandboxPrompt, "<mbx's built-in prompt"
	if p := c.cfg.Resolve(c.cfg.Claude.ExtraSystemPrompt); p != "" {
		extra, err := os.ReadFile(p)
		if err != nil {
			warn("extra system prompt not loaded: " + err.Error())
		} else {
			prompt += "\n" + string(extra)
		}
		shownPrompt += " + contents of " + r.Disp(p)
	}
	command := []string{"claude", "--append-system-prompt", prompt}
	shown := "claude --append-system-prompt " + shownPrompt + ">"
	if len(args) > 0 {
		shown += " " + shell.Join(args)
	}
	return append(command, args...), shown
}

// hostTrusts reports whether the host claude trusts dir or a parent, as Claude Code applies a parent's trust
func hostTrusts(host map[string]any, dir string) bool {
	projects, _ := host["projects"].(map[string]any)
	for {
		if p, ok := projects[dir].(map[string]any); ok && p["hasTrustDialogAccepted"] == true {
			return true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return false
		}
		dir = parent
	}
}

var otelEndpointKey = regexp.MustCompile(`^OTEL_EXPORTER_OTLP.*ENDPOINT$`)

// telemetryHosts returns the OTEL endpoint hosts in the env of the org's remote settings and, when it's
// shared, the host's settings.json
func telemetryHosts(cfg *config.Config) []string {
	files := []string{"remote-settings.json"}
	if cfg.Claude.HostShare.Config {
		files = append(files, "settings.json")
	}
	var hosts []string
	for _, f := range files {
		data, err := os.ReadFile(filepath.Join(config.HostClaudeDir(), f))
		if err != nil {
			continue
		}
		var conf struct {
			Env map[string]any `json:"env"`
		}
		if json.Unmarshal(data, &conf) != nil {
			continue
		}
		for k, v := range conf.Env {
			s, ok := v.(string)
			if !ok || !otelEndpointKey.MatchString(k) {
				continue
			}
			if u, err := url.Parse(s); err == nil && u.Hostname() != "" {
				hosts = config.AppendUnique(hosts, u.Hostname())
			}
		}
	}
	return hosts
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
