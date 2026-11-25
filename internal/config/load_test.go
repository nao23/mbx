package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nao23/msbx/internal/secret"
	"github.com/nao23/msbx/internal/testutil"
)

// writeConfig writes a config file (see testutil.WriteConfig) and returns its dir. HOME is a temp dir too.
func writeConfig(t *testing.T, yamlText string) string {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	return testutil.WriteConfig(t, yamlText)
}

func allowList(cfg *Config) []string {
	n, _ := cfg.Microsandbox["network"].(map[string]any)
	return StringList(n["allow"])
}

// Without a config file, the embedded defaults apply
func TestLoadDefaults(t *testing.T) {
	t.Setenv("MSBX_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	cfg, err := Load("/work")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != "claude" || cfg.Image() != "msbx-default:latest" || cfg.MCPGateway.Port != 41837 ||
		!cfg.Docker.Enabled || cfg.Claude.SecretSource.Env != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
}

// projects entries for the workspace and its parents are merged, deepest last; network.allow is appended
// and other lists replaced
func TestLoadProjects(t *testing.T) {
	writeConfig(t, `
		version: 1
		microsandbox_config:
		  memory: 4G
		  network:
		    allow: [a.example.com]
		projects:
		  /work/repo:                     # deepest, applied last
		    microsandbox_config:
		      memory: 16G
		      network: {allow: [c.example.com]}
		  /work:
		    agent: shell
		    microsandbox_config:
		      memory: 8G
		      network: {allow: [b.example.com, a.example.com]}
		  /work/repo2:                    # a sibling with a common prefix, not a parent
		    keep: true
	`)
	cfg, err := Load("/work/repo/sub")
	if err != nil {
		t.Fatal(err)
	}
	if got := cfg.Microsandbox["memory"]; got != "16G" {
		t.Errorf("memory = %v, want 16G (deepest project wins)", got)
	}
	if cfg.Agent != "shell" {
		t.Errorf("agent = %q, want shell (from the parent project)", cfg.Agent)
	}
	if cfg.Keep {
		t.Error("keep = true; projects./work/repo2 shouldn't match /work/repo/sub")
	}
	if got, want := allowList(cfg), []string{"a.example.com", "b.example.com", "c.example.com"}; !slices.Equal(got, want) {
		t.Errorf("network.allow = %v, want %v (appended without duplicates)", got, want)
	}

	// A list other than network.allow is replaced
	writeConfig(t, `
		command: [zsh, -l]
		projects:
		  /work: {command: [fish]}
	`)
	cfg, err = Load("/work")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cfg.Command, []string{"fish"}) {
		t.Errorf("command = %v, want [fish]", cfg.Command)
	}
}

// A projects key starting with ~ matches the workspace under the home dir
func TestLoadProjectsExpandsHome(t *testing.T) {
	writeConfig(t, `
		projects:
		  ~/src/repo: {keep: true}
	`)
	home, _ := os.UserHomeDir()
	cfg, err := Load(filepath.Join(home, "src", "repo"))
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Keep {
		t.Error("projects key with ~ didn't match")
	}
}

// A secret source replaces the default or a parent's whole, rather than merging with it
func TestSecretSourceIsReplaced(t *testing.T) {
	writeConfig(t, `
		github:
		  secret_source: {keychain: my-gh}      # the default is {env: GH_TOKEN}
		secret_sources:
		  MY_TOKEN: {keychain: my-token}
		projects:
		  /work:
		    secret_sources:
		      MY_TOKEN: {env: MY_TOKEN_VALUE}
	`)
	cfg, err := Load("/work")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.GitHub.SecretSource, (secret.Source{Keychain: "my-gh"}); got != want {
		t.Errorf("github secret_source = %+v, want %+v", got, want)
	}
	if got, want := cfg.SecretSources["MY_TOKEN"], (secret.Source{Env: "MY_TOKEN_VALUE"}); got != want {
		t.Errorf("secret_sources.MY_TOKEN = %+v, want %+v", got, want)
	}
}

// Unknown keys (nested too), a non-map project and broken YAML are errors naming the problem
func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"unknown key", "agnet: shell\n", "agnet"},
		{"unknown nested key", "docker: {enable: false}\n", "enable"},
		{"project not a map", "projects:\n  /work: true\n", "projects./work must be a map"},
		{"broken yaml", "agent: [\n", "config.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, tc.yaml)
			_, err := Load("/work")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// Relative paths in the config are resolved from the config dir; ~ and absolute paths as usual
func TestResolve(t *testing.T) {
	dir := writeConfig(t, "version: 1\n")
	cfg, err := Load("/work")
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	for in, want := range map[string]string{
		"":                  "",
		"sandbox-prompt.md": filepath.Join(dir, "sandbox-prompt.md"),
		"/abs/prompt.md":    "/abs/prompt.md",
		"~/prompt.md":       filepath.Join(home, "prompt.md"),
	} {
		if got := cfg.Resolve(in); got != want {
			t.Errorf("Resolve(%q) = %q, want %q", in, got, want)
		}
	}
}

// An MCP server's url, client_id and callback_port left out of mcp_gateway.servers come from the host
// claude's server of the same name, the client settings only when the url is the same
func TestMCPServer(t *testing.T) {
	writeConfig(t, `
		mcp_gateway:
		  servers:
		    secret: {client_secret_source: {keychain: s}}
		    moved: {url: https://new.example.com/mcp}
		    own: {url: https://own.example.com/mcp, client_id: mine}
	`)
	home, _ := os.UserHomeDir()
	hostJSON := `{"mcpServers": {
		"secret": {"type": "http", "url": "https://s.example.com/mcp", "oauth": {"clientId": "c1", "callbackPort": 13579}},
		"moved":  {"type": "http", "url": "https://old.example.com/mcp", "oauth": {"clientId": "c2", "callbackPort": 1}},
		"plain":  {"type": "http", "url": "https://p.example.com/mcp"},
		"stdio":  {"command": "npx"}
	}}`
	if err := os.WriteFile(filepath.Join(home, ".claude.json"), []byte(hostJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load("/work")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]MCPServer{
		"secret": {URL: "https://s.example.com/mcp", ClientID: "c1", CallbackPort: 13579, ClientSecretSource: secret.Source{Keychain: "s"}},
		"moved":  {URL: "https://new.example.com/mcp"},
		"plain":  {URL: "https://p.example.com/mcp"},
		"own":    {URL: "https://own.example.com/mcp", ClientID: "mine"},
	} {
		if got, ok := cfg.MCPServer(name); !ok || got != want {
			t.Errorf("MCPServer(%s) = %+v, %v; want %+v", name, got, ok, want)
		}
	}
	for _, name := range []string{"stdio", "missing"} {
		if _, ok := cfg.MCPServer(name); ok {
			t.Errorf("MCPServer(%s): ok, want none without a url", name)
		}
	}
}
