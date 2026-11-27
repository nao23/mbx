package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/nao23/mbx/internal/secret"
	"github.com/nao23/mbx/internal/testutil"
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
	t.Setenv("MBX_CONFIG_DIR", t.TempDir())
	cfg, err := Load("/work", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Agent != "claude" || cfg.Image() != "mbx-default:latest" || cfg.MCPGateway.Port != 41837 ||
		!cfg.Docker.Enabled || cfg.Claude.SecretSource.Env != "CLAUDE_CODE_OAUTH_TOKEN" {
		t.Errorf("unexpected defaults: %+v", cfg)
	}
	if _, why := cfg.File(); !strings.Contains(why, "the defaults alone") {
		t.Errorf("why = %q, want it to say the defaults alone apply", why)
	}
}

// addConfig writes another config file into the config dir.
func addConfig(t *testing.T, dir, name, yamlText string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(testutil.Dedent(yamlText)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The file whose directories has the workspace or the deepest of its parents is used, else none. Only that
// one is layered over the defaults: network.allow is appended to the defaults', other lists replaced
func TestLoadChoosesByDirectory(t *testing.T) {
	dir := writeConfig(t, "directories: [/home]\ncommand: [zsh]\n")
	work := addConfig(t, dir, "work.yaml", `
		directories: [/work]
		command: [fish]
		microsandbox_config: {network: {allow: [a.example.com]}}
	`)
	repo := addConfig(t, dir, "repo.yaml", "directories: [/work/repo, ~/src/repo]\nkeep: true\n")
	addConfig(t, dir, "notes.md", "not a config file\n")
	home, _ := os.UserHomeDir()

	for workspace, want := range map[string]string{
		"/work/repo/sub":                   repo,
		"/work/repo2":                      work, // a sibling with a common prefix, not a subdirectory
		"/work":                            work,
		filepath.Join(home, "src", "repo"): repo,
		"/home/u":                          filepath.Join(dir, "default.yaml"),
		"/other":                           "",
	} {
		cfg, err := Load(workspace, "")
		if err != nil {
			t.Fatal(err)
		}
		if got, why := cfg.File(); got != want {
			t.Errorf("%s: file = %s (%s), want %s", workspace, got, why, want)
		}
	}

	cfg, _ := Load("/work/repo", "")
	if !slices.Equal(cfg.Command, []string{"bash"}) || !cfg.Keep {
		t.Errorf("command = %v, keep = %v; want [bash] (default.yaml isn't layered) and true", cfg.Command, cfg.Keep)
	}
	cfg, _ = Load("/work", "")
	if !slices.Equal(cfg.Command, []string{"fish"}) || !slices.Equal(allowList(cfg), []string{"a.example.com"}) {
		t.Errorf("command = %v, network.allow = %v", cfg.Command, allowList(cfg))
	}
}

// --config uses the file given, wherever it is and whatever its directories; a missing one is an error
func TestLoadGivenFile(t *testing.T) {
	writeConfig(t, "command: [zsh]\n")
	given := addConfig(t, t.TempDir(), "other.yaml", "directories: [/elsewhere]\nkeep: true\n")
	cfg, err := Load("/work", given)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := cfg.File(); got != given || !cfg.Keep || !slices.Equal(cfg.Command, []string{"bash"}) {
		t.Errorf("file = %s, keep = %v, command = %v", got, cfg.Keep, cfg.Command)
	}
	if _, err := Load("/work", filepath.Join(t.TempDir(), "missing.yaml")); err == nil {
		t.Error("missing --config file: want an error")
	}
}

// Ambiguous or misplaced directories are errors whichever the workspace
func TestChooseErrors(t *testing.T) {
	for _, tc := range []struct{ name, config, other, want string }{
		{"in two files", "", "directories: [/a/b]\n", "/a/b is in both"},
		{"a file without them", "", "keep: true\n", "list the directories"},
		{"a relative directory", "", "directories: [repo]\n", "isn't an absolute path"},
		{"a bare ~ (null in YAML)", "", "directories: [~]\n", `write "~"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeConfig(t, tc.config)
			addConfig(t, dir, "a.yaml", "directories: [/a/b]\n")
			if tc.other != "" {
				addConfig(t, dir, "b.yaml", tc.other)
			}
			if _, err := Load("/elsewhere", ""); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// A secret source replaces the default whole, rather than merging with it
func TestSecretSourceIsReplaced(t *testing.T) {
	writeConfig(t, `
		github:
		  secret_source: {keyring: my-gh}      # the default is {env: GH_TOKEN}
	`)
	cfg, err := Load("/work", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cfg.GitHub.SecretSource, (secret.Source{Keyring: "my-gh"}); got != want {
		t.Errorf("github secret_source = %+v, want %+v", got, want)
	}
}

// Unknown keys (nested too) and broken YAML are errors naming the problem
func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"unknown key", "kep: true\n", "kep"},
		{"unknown nested key", "docker: {enable: false}\n", "enable"},
		{"broken yaml", "keep: [\n", "default.yaml"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeConfig(t, tc.yaml)
			_, err := Load("/work", "")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one mentioning %q", err, tc.want)
			}
		})
	}
}

// Relative paths in the config are resolved from the config dir; ~ and absolute paths as usual
func TestResolve(t *testing.T) {
	dir := writeConfig(t, "version: 1\n")
	cfg, err := Load("/work", "")
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
		    secret: {client_secret_source: {keyring: s}}
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
	cfg, err := Load("/work", "")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]MCPServer{
		"secret": {URL: "https://s.example.com/mcp", ClientID: "c1", CallbackPort: 13579, ClientSecretSource: secret.Source{Keyring: "s"}},
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
