package agent

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/testutil"
)

// loadConfig loads yamlText as the config file (see testutil.WriteConfig), with an empty host ~/.claude.
func loadConfig(t *testing.T, yamlText string) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	testutil.WriteConfig(t, yamlText)
	cfg, err := config.Load("/work", "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Each agent is created with its own secret (shell has none), and an unknown agent is an error
func TestNew(t *testing.T) {
	cfg := loadConfig(t, "version: 1\n")
	for name, wantEnv := range map[string]string{"claude": "CLAUDE_CODE_OAUTH_TOKEN", "shell": ""} {
		cfg.Agent = name
		a, err := New(cfg)
		if err != nil {
			t.Fatal(err)
		}
		env := ""
		if s := a.Feature("/work", nil).Secret; s != nil {
			env = s.Env
		}
		if env != wantEnv {
			t.Errorf("%s: Secret env = %q, want %q", name, env, wantEnv)
		}
	}
	cfg.Agent = "codex"
	if _, err := New(cfg); err == nil {
		t.Error("New with an unknown agent: want an error")
	}
}

// shell runs the configured command, or the args when given, shown quoted
func TestShellCommand(t *testing.T) {
	a := &shellAgent{command: []string{"bash"}}
	r := &msb.Runner{}
	if got, _ := a.Command(r, nil); !slices.Equal(got, []string{"bash"}) {
		t.Errorf("Command without args = %v, want the configured command", got)
	}
	if got, shown := a.Command(r, []string{"make", "test it"}); !slices.Equal(got, []string{"make", "test it"}) ||
		shown != "make 'test it'" {
		t.Errorf("Command with args = %v, %q", got, shown)
	}
}

// claude gets the built-in system prompt plus extra_system_prompt, and dry-run shows where they come from
func TestClaudeCommand(t *testing.T) {
	// The built-in prompt is always passed
	cfg := loadConfig(t, "version: 1\n")
	got, shown := newClaude(cfg).Command(&msb.Runner{}, []string{"-p", "hi there"})
	if want := []string{"claude", "--append-system-prompt", sandboxPrompt, "-p", "hi there"}; !slices.Equal(got, want) {
		t.Errorf("Command = %v, want %v", got, want)
	}
	if want := "claude --append-system-prompt <msbx's built-in prompt> -p 'hi there'"; shown != want {
		t.Errorf("shown = %q, want %q", shown, want)
	}

	// extra_system_prompt is appended to it
	extra := filepath.Join(t.TempDir(), "extra.md")
	if err := os.WriteFile(extra, []byte("- extra note\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg = loadConfig(t, "claude: {extra_system_prompt: "+extra+"}\n")
	if got, _ := newClaude(cfg).Command(&msb.Runner{}, nil); len(got) != 3 || got[2] != sandboxPrompt+"\n- extra note\n" {
		t.Errorf("Command with extra_system_prompt = %q", got)
	}

	// A missing extra prompt only warns
	cfg = loadConfig(t, "claude: {extra_system_prompt: /nonexistent/prompt.md}\n")
	got, shown = newClaude(cfg).Command(&msb.Runner{}, nil)
	if want := []string{"claude", "--append-system-prompt", sandboxPrompt}; !slices.Equal(got, want) {
		t.Errorf("Command with a missing extra prompt = %v, want the built-in prompt only", got)
	}
	if want := "claude --append-system-prompt <msbx's built-in prompt + contents of /nonexistent/prompt.md>"; shown != want {
		t.Errorf("shown = %q, want %q", shown, want)
	}
}

// host_share.skills mounts the named skills (or all with "*") read-only, from where their symlinks point;
// directories without SKILL.md and unknown names are skipped
func TestClaudeSkills(t *testing.T) {
	cfg := loadConfig(t, "version: 1\n") // sets HOME
	skills := filepath.Join(config.HostClaudeDir(), "skills")
	elsewhere := t.TempDir()
	for _, dir := range []string{filepath.Join(skills, "local"), filepath.Join(elsewhere, "linked"), filepath.Join(skills, "synced", "x")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, dir := range []string{filepath.Join(skills, "local"), filepath.Join(elsewhere, "linked")} {
		if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte("---\nname: x\n---\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(elsewhere, "linked"), filepath.Join(skills, "linked")); err != nil {
		t.Fatal(err)
	}
	linked, _ := filepath.EvalSymlinks(filepath.Join(elsewhere, "linked"))
	local, _ := filepath.EvalSymlinks(filepath.Join(skills, "local"))

	volumes := func(share ...string) []string {
		cfg.Claude.HostShare.Skills = share
		var out []string
		for _, f := range newClaude(cfg).flags("/work") {
			if strings.Contains(f.Value, "/skills/") {
				out = append(out, f.Value)
			}
		}
		return out
	}
	all := []string{linked + ":/root/.claude-state/skills/linked:ro", local + ":/root/.claude-state/skills/local:ro"}
	if got := volumes("*"); !slices.Equal(got, all) {
		t.Errorf("skills [*] = %v, want %v", got, all)
	}
	if got := volumes("linked", "missing", "synced"); !slices.Equal(got, all[:1]) {
		t.Errorf("skills [linked missing synced] = %v, want %v", got, all[:1])
	}
	if got := volumes(); len(got) != 0 {
		t.Errorf("no skills = %v, want none", got)
	}
}

// claude uses mcp_servers by name and claude_ai_connectors as claudeai:<display name>
func TestClaudeMCPServers(t *testing.T) {
	cfg := loadConfig(t, "claude: {mcp_servers: [example], claude_ai_connectors: [Slack, Google Drive]}\n")
	if got, want := newClaude(cfg).MCPServers(), []string{"example", "claudeai:Slack", "claudeai:Google Drive"}; !slices.Equal(got, want) {
		t.Errorf("MCPServers = %v, want %v", got, want)
	}
}

// A workspace is trusted if the host claude trusts it or a parent
func TestHostTrusts(t *testing.T) {
	host := map[string]any{"projects": map[string]any{
		"/work":       map[string]any{"hasTrustDialogAccepted": true},
		"/other/repo": map[string]any{"hasTrustDialogAccepted": false},
	}}
	for dir, want := range map[string]bool{
		"/work":          true,
		"/work/repo/sub": true,
		"/other/repo":    false,
		"/workspace":     false,
		"/":              false,
	} {
		if got := hostTrusts(host, dir); got != want {
			t.Errorf("hostTrusts(%s) = %v, want %v", dir, got, want)
		}
	}
}

// OTEL endpoint hosts come from the org's remote settings, and from settings.json only when it's shared
func TestTelemetryHosts(t *testing.T) {
	cfg := loadConfig(t, "claude: {host_share: {config: true}}\n")
	home, _ := os.UserHomeDir()
	dir := filepath.Join(home, ".claude")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"remote-settings.json": `{"env": {"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example.com:4318", "OTHER": "https://x.example.com"}}`,
		"settings.json":        `{"env": {"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT": "https://logs.example.com/v1/logs"}}`,
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got := telemetryHosts(cfg)
	slices.Sort(got)
	if want := []string{"logs.example.com", "otel.example.com"}; !slices.Equal(got, want) {
		t.Errorf("telemetryHosts = %v, want %v", got, want)
	}

	// settings.json counts only when it's shared
	cfg.Claude.HostShare.Config = false
	if got := telemetryHosts(cfg); !slices.Equal(got, []string{"otel.example.com"}) {
		t.Errorf("telemetryHosts without sharing settings.json = %v", got)
	}
}

// Prepare creates only the shared parts of ~/.claude that are missing, never the workspace's own
// session directory (claude in the VM creates it in the mounted projects)
func TestClaudePrepare(t *testing.T) {
	cfg := loadConfig(t, "version: 1\n")
	if err := newClaude(cfg).Prepare(&msb.Runner{}, "/work/repo"); err != nil {
		t.Fatal(err)
	}
	host := config.HostClaudeDir()
	for _, p := range []string{"projects", "file-history", "history.jsonl"} {
		if _, err := os.Stat(filepath.Join(host, p)); err != nil {
			t.Errorf("%s not created: %v", p, err)
		}
	}
	if entries, _ := os.ReadDir(filepath.Join(host, "projects")); len(entries) != 0 {
		t.Errorf("projects has %d entries, want none", len(entries))
	}
}
