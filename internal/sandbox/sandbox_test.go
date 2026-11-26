package sandbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/mcpgateway"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/testutil"
)

// The host's ~/.claude is empty unless a test creates files with hostClaude. PATH is empty so no external
// command runs (git config, called by the git feature, then finds nothing)
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "mbx-test-home")
	if err != nil {
		panic(err)
	}
	os.Setenv("HOME", home)
	os.Setenv("PATH", "")
	os.Unsetenv("XDG_CONFIG_HOME")
	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// loadConfig loads yamlText as the config file (see testutil.WriteConfig).
func loadConfig(t *testing.T, yamlText, workspace string) *config.Config {
	t.Helper()
	testutil.WriteConfig(t, yamlText)
	cfg, err := config.Load(workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// hostClaude creates files in the host's ~/.claude, except .claude.json, which goes in the home dir.
func hostClaude(t *testing.T, files map[string]string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	for name, content := range files {
		p := filepath.Join(home, ".claude", name)
		if name == ".claude.json" {
			p = filepath.Join(home, name)
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func flagValues(flags []msb.Flag, name string) []string {
	var out []string
	for _, f := range flags {
		if f.Name == name {
			out = append(out, f.Value)
		}
	}
	return out
}

// validate rejects bad ECR settings, an empty image, MCP servers not in mcp_gateway.servers (or connectors
// listed there), flags mbx sets itself, and the mbx- script prefix
func TestValidate(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"ok", "version: 1\n", ""},
		{"ecr without docker", "docker: {enabled: false, ecr: {enabled: true, registry: 1.dkr.ecr.r.amazonaws.com}}\n", "requires docker"},
		{"ecr without registry", "docker: {ecr: {enabled: true}}\n", "registry is not set"},
		{"ecr without secret source", "docker: {ecr: {enabled: true, registry: registry.example.com, secret_source: {}}}\n", "secret_source is not set"},
		{"no image", "microsandbox_config: {image: \"\"}\n", "image is not set"},
		{"mcp server defined", "claude: {mcp_servers: [g]}\nmcp_gateway: {servers: {g: {url: https://g.example.com/mcp}}}\n", ""},
		{"mcp server not defined", "claude: {mcp_servers: [g]}\n", "has no url"},
		{"connector in mcp_servers", "claude: {mcp_servers: [\"claudeai:Slack\"]}\n", "claude.claude_ai_connectors (as Slack)"},
		{"reserved flag", "microsandbox_flags: {workdir: /tmp}\n", "--workdir is set by mbx"},
		{"reserved flag with dashes", "microsandbox_flags: {--name: x}\n", "--name is set by mbx"},
		{"reserved script", "microsandbox_config: {scripts: {mbx-mine: echo}}\n", "mbx- prefix is reserved"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validate(loadConfig(t, tc.yaml, "/work"))
			if tc.want == "" && err != nil {
				t.Errorf("err = %v, want nil", err)
			}
			if tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// microsandbox_flags become msb flags, sorted: lists repeat the flag, true is a bare flag, false is dropped
func TestMicrosandboxFlags(t *testing.T) {
	cfg := loadConfig(t, `
		microsandbox_flags:
		  root-disk: 16G
		  --max-memory: 8G
		  publish: [8080:80, 8443:443]
		  some-switch: true
		  off-switch: false
	`, "/work")
	flags, err := microsandboxFlags(cfg)
	if err != nil {
		t.Fatal(err)
	}
	got := msb.FlagArgs(flags)
	want := []string{"--max-memory", "8G", "--publish", "8080:80", "--publish", "8443:443", "--root-disk", "16G", "--some-switch"}
	if !slices.Equal(got, want) {
		t.Errorf("flags = %v, want %v", got, want)
	}
}

// The setup script holds the enabled features' parts only, and is empty with none
func TestSetupScript(t *testing.T) {
	cfg := loadConfig(t, "docker: {enabled: false, emulation: false}\nuv: {vm_venv: false}\n", "/work")
	if s := setupScript(features(cfg, "/work", nil)); s != "" {
		t.Errorf("setupScript with nothing enabled = %q, want empty", s)
	}

	cfg = loadConfig(t, `
		docker: {enabled: true, emulation: false, ecr: {enabled: true, registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com}}
		github: {enabled: true}
	`, "/work")
	s := setupScript(features(cfg, "/work", nil))
	for _, want := range []string{
		"set -e",
		"dockerd",
		"ECR_REGISTRY=123456789012.dkr.ecr.ap-northeast-1.amazonaws.com\n",
		`docker login -u AWS --password-stdin "$ECR_REGISTRY"`,
		`insteadOf "git@github.com:"`,
		`UV_PROJECT_ENVIRONMENT="/root/.venvs$d"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("setupScript lacks %q:\n%s", want, s)
		}
	}
	if strings.Contains(s, "binfmt") {
		t.Errorf("setupScript has the emulation part with emulation off:\n%s", s)
	}
}

// microsandbox expands ${VAR} in msb create's config from the host's environment, which holds the real
// tokens, so the setup script must not use that form
func TestSetupScriptHasNoBracedVars(t *testing.T) {
	cfg := loadConfig(t, `
		docker: {enabled: true, emulation: true, ecr: {enabled: true, registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com}}
		github: {enabled: true}
	`, "/work")
	if s := setupScript(features(cfg, "/work", nil)); strings.Contains(s, "${") {
		t.Errorf("setupScript contains ${...}, which microsandbox expands on the host:\n%s", s)
	}
}

// The shell plan: the user's scripts plus setup, labels, only the workspace mounted, the features'
// secrets, flags and hosts, and nothing of claude's
func TestBuildPlanShell(t *testing.T) {
	cfg := loadConfig(t, `
		microsandbox_config:
		  scripts: {hello: echo hello}
		  network: {allow: [pypi.org]}
		github: {enabled: true}
	`, "/work/repo")
	cfg.Agent = "shell"
	p := buildPlan(cfg, "/work/repo", "shell-repo", nil)

	if _, ok := p.conf["network"]; ok {
		t.Error("--conf has network; it goes to --net-conf")
	}
	scripts := p.conf["scripts"].(map[string]any)
	if scripts["hello"] != "echo hello" || scripts[setupScriptName] == nil {
		t.Errorf("scripts = %v, want the user's and %s", scripts, setupScriptName)
	}
	if _, ok := p.conf["patches"]; ok {
		t.Error("shell got claude's patches")
	}

	if got := flagValues(p.flags, "--name"); !slices.Equal(got, []string{"shell-repo"}) {
		t.Errorf("--name = %v", got)
	}
	if got := flagValues(p.flags, "--label"); !slices.Equal(got, []string{"mbx=1", "mbx.agent=shell", "mbx.workspace=/work/repo"}) {
		t.Errorf("--label = %v", got)
	}
	if got := flagValues(p.flags, "--volume"); !slices.Equal(got, []string{"/work/repo:/work/repo"}) {
		t.Errorf("--volume = %v, want only the workspace", got)
	}
	if got := flagValues(p.flags, "--secret"); !slices.Equal(got, []string{"GH_TOKEN@github.com,api.github.com"}) {
		t.Errorf("--secret = %v", got)
	}
	if got := flagValues(p.flags, "--mount-owned"); !slices.Equal(got, []string{"/var/lib/docker:kind=disk,size=20G"}) {
		t.Errorf("--mount-owned = %v", got)
	}
	if got := flagValues(p.flags, "--env"); len(got) != 0 {
		t.Errorf("--env = %v, want none for shell", got)
	}

	allow := p.net["allow"].([]string)
	for _, h := range []string{"pypi.org", "github.com", "api.github.com", "ghcr.io"} {
		if !slices.Contains(allow, h) {
			t.Errorf("allow lacks %s: %v", h, allow)
		}
	}
	if slices.Contains(allow, "api.anthropic.com") {
		t.Errorf("shell allows claude's API: %v", allow)
	}
}

// The claude plan: the host ~/.claude mounts that exist, the token secret, allowed hosts (OTEL, gateway),
// the .claude.json seed (onboarding, trust, MCP servers) and the org settings copy
func TestBuildPlanClaude(t *testing.T) {
	hostClaude(t, map[string]string{
		".claude.json": `{
			"hasCompletedOnboarding": true,
			"numStartups": 42,
			"projects": {"/work": {"hasTrustDialogAccepted": true}}
		}`,
		"CLAUDE.md":                    "# host",
		"remote-settings.json":         `{"env": {"OTEL_EXPORTER_OTLP_ENDPOINT": "https://otel.example.com:4318", "OTHER": "https://x.example.com"}}`,
		"remote-settings-consent.json": `{}`,
	})
	cfg := loadConfig(t, "docker: {enabled: false}\n", "/work/repo")
	gateway := &mcpgateway.Registration{ID: "ab12cd34", Servers: []string{"example", "claudeai:Google Drive"}}
	p := buildPlan(cfg, "/work/repo", "claude-repo", gateway)

	home, _ := os.UserHomeDir()
	host := filepath.Join(home, ".claude")
	wantVolumes := []string{
		"/work/repo:/work/repo",
		host + "/projects:/root/.claude-state/projects",
		host + "/file-history:/root/.claude-state/file-history",
		host + "/history.jsonl:/root/.claude-state/history.jsonl",
		host + "/CLAUDE.md:/root/.claude-state/CLAUDE.md:ro", // settings.json doesn't exist, so it isn't mounted
		host + "/remote-settings-consent.json:/root/.claude-state/remote-settings-consent.json:ro",
	}
	if got := flagValues(p.flags, "--volume"); !slices.Equal(got, wantVolumes) {
		t.Errorf("--volume =\n%v\nwant\n%v", got, wantVolumes)
	}
	if got := flagValues(p.flags, "--secret"); !slices.Equal(got, []string{"CLAUDE_CODE_OAUTH_TOKEN@api.anthropic.com"}) {
		t.Errorf("--secret = %v", got)
	}
	if got := flagValues(p.flags, "--mount-owned"); len(got) != 0 {
		t.Errorf("--mount-owned = %v, want none with docker disabled", got)
	}
	// The MCP gateway is reached through its socket for this sandbox, relayed by socat in the VM, not the network
	if got, want := flagValues(p.flags, "--vsock"), []string{home + "/.local/state/mbx/mcp-gateway/s/ab12cd34.sock:41837"}; !slices.Equal(got, want) {
		t.Errorf("--vsock = %v, want %v", got, want)
	}
	if s := p.conf["scripts"].(map[string]any)[setupScriptName].(string); !strings.Contains(s, "VSOCK-CONNECT:2:41837") {
		t.Errorf("setup script lacks the MCP bridge:\n%s", s)
	}

	allow := p.net["allow"].([]string)
	for _, h := range []string{"api.anthropic.com", "platform.claude.com", "otel.example.com"} {
		if !slices.Contains(allow, h) {
			t.Errorf("allow lacks %s: %v", h, allow)
		}
	}
	if slices.ContainsFunc(allow, func(s string) bool { return strings.HasPrefix(s, "host:") }) {
		t.Errorf("allow reaches the host: %v", allow)
	}
	if slices.Contains(allow, "x.example.com") {
		t.Errorf("allow has a non-OTEL env value: %v", allow)
	}

	// Patches: .claude.json, then a copy of the org settings cache
	patches := p.conf["patches"].([]any)
	if len(patches) != 2 {
		t.Fatalf("patches = %v, want .claude.json and remote-settings.json", patches)
	}
	text := patches[0].(map[string]any)["text"].(map[string]any)
	if text["path"] != "/root/.claude-state/.claude.json" {
		t.Errorf("patch path = %v", text["path"])
	}
	var seed struct {
		HasCompletedOnboarding bool                       `json:"hasCompletedOnboarding"`
		NumStartups            *int                       `json:"numStartups"`
		Projects               map[string]map[string]bool `json:"projects"`
		MCPServers             map[string]struct {
			Type, URL string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(text["content"].(string)), &seed); err != nil {
		t.Fatal(err)
	}
	if !seed.HasCompletedOnboarding || seed.NumStartups != nil {
		t.Errorf("seed should copy only the onboarding state: %s", text["content"])
	}
	if !seed.Projects["/work/repo"]["hasTrustDialogAccepted"] {
		t.Errorf("workspace not trusted though the host trusts its parent: %s", text["content"])
	}
	wantServers := map[string]string{
		"example":                "http://127.0.0.1:41837/example",
		"claude_ai_Google_Drive": "http://127.0.0.1:41837/claudeai:Google%20Drive",
	}
	for name, url := range wantServers {
		if s := seed.MCPServers[name]; s.Type != "http" || s.URL != url {
			t.Errorf("mcpServers[%s] = %+v, want http %s", name, s, url)
		}
	}
	copied := patches[1].(map[string]any)["copy_file"].(map[string]any)
	if copied["dst"] != "/root/.claude-state/remote-settings.json" {
		t.Errorf("copy_file = %v", copied)
	}
}

// With host_share off and no gateway, nothing of the host's ~/.claude reaches the VM
func TestBuildPlanClaudeWithoutHostState(t *testing.T) {
	cfg := loadConfig(t, `
		claude:
		  host_share: {sessions: false, config: false}
	`, "/work/repo")
	p := buildPlan(cfg, "/work/repo", "claude-repo", nil)
	if got := flagValues(p.flags, "--volume"); !slices.Equal(got, []string{"/work/repo:/work/repo"}) {
		t.Errorf("--volume = %v, want only the workspace", got)
	}
	if got := flagValues(p.flags, "--env"); !slices.Equal(got, []string{"CLAUDE_CONFIG_DIR=/root/.claude-state"}) {
		t.Errorf("--env = %v", got)
	}
	if slices.ContainsFunc(p.net["allow"].([]string), func(s string) bool { return strings.HasPrefix(s, "host:") }) {
		t.Error("gateway port allowed without a gateway registration")
	}
	text := p.conf["patches"].([]any)[0].(map[string]any)["text"].(map[string]any)
	if text["content"] != "{}\n" {
		t.Errorf(".claude.json seed = %q, want {} without host state", text["content"])
	}
}

// The default name is the agent and the workspace's base name, with other characters made hyphens
func TestDefaultName(t *testing.T) {
	cfg := &config.Config{Agent: "claude"}
	if got := defaultName(cfg, "/work/my repo+1"); got != "claude-my-repo-1" {
		t.Errorf("defaultName = %q", got)
	}
}

// Dry-run output collapses the features' IP rules into one comment line, keeping the user's own rules
func TestCollapseRules(t *testing.T) {
	rules := []string{"140.82.112.0/20:tcp:22", "192.30.252.0/22:tcp:22"}
	in := strings.Join([]string{
		"allow:",
		"    - 140.82.112.0/20:tcp:22",
		"    - 192.30.252.0/22:tcp:22",
		"    - 10.0.0.0/8:tcp:22", // the user's own rule stays
		"    - github.com",
		"policy: none",
	}, "\n")
	want := strings.Join([]string{
		"allow:",
		"    # (2 IP rules from the features, placed before the domains)",
		"    - 10.0.0.0/8:tcp:22",
		"    - github.com",
		"policy: none",
	}, "\n")
	if got := collapseRules(in, rules); got != want {
		t.Errorf("collapseRules =\n%s\nwant\n%s", got, want)
	}
	if got := collapseRules("allow:\n    - github.com\n", nil); got != "allow:\n    - github.com\n" {
		t.Errorf("changed without rules: %q", got)
	}
}

// Rules by IP or CIDR (with or without a port) go before the domains; domains, with ports or not, don't
func TestIsIPRule(t *testing.T) {
	for rule, want := range map[string]bool{
		"140.82.112.0/20:tcp:22":  true,
		"10.0.0.0/8":              true,
		"192.0.2.1":               true,
		"github.com":              false,
		"github.com:tcp:443":      false,
		"*.githubusercontent.com": false,
		"host:tcp:41837":          false,
	} {
		if got := isIPRule(rule); got != want {
			t.Errorf("isIPRule(%q) = %v, want %v", rule, got, want)
		}
	}
}

// The default image in defaults.yaml is the one mbx builds
func TestDefaultImage(t *testing.T) {
	if got := loadConfig(t, "version: 1\n", "/work").Image(); got != DefaultImage {
		t.Errorf("default image = %q, want %q", got, DefaultImage)
	}
}

// Editing comments in the Dockerfile doesn't change its hash, so it doesn't trigger a rebuild
func TestDockerfileHash(t *testing.T) {
	orig := dockerfile
	t.Cleanup(func() { dockerfile = orig })
	dockerfile = "FROM ubuntu\n# one comment\nRUN true\n"
	h := dockerfileHash()
	dockerfile = "# header\nFROM ubuntu\n\n  # another comment\nRUN true\n"
	if dockerfileHash() != h {
		t.Error("hash changed with only comments and blank lines changed")
	}
	dockerfile = "FROM ubuntu\nRUN false\n"
	if dockerfileHash() == h {
		t.Error("hash unchanged with an instruction changed")
	}
}
