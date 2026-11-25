// Package config loads msbx's config. Each field is described in defaults.yaml.
package config

import (
	"cmp"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nao23/msbx/internal/secret"
)

// Config is the merged config. Microsandbox and MicrosandboxFlags are passed to msb uninterpreted
type Config struct {
	Version           int                      `yaml:"version"`
	Agent             string                   `yaml:"agent"`
	Command           []string                 `yaml:"command"`
	Keep              bool                     `yaml:"keep"`
	Microsandbox      map[string]any           `yaml:"microsandbox_config"`
	MicrosandboxFlags map[string]any           `yaml:"microsandbox_flags"`
	SecretSources     map[string]secret.Source `yaml:"secret_sources"`
	Claude            ClaudeConfig             `yaml:"claude"`
	Docker            DockerConfig             `yaml:"docker"`
	Git               struct {
		HostUser   bool `yaml:"host_user"`
		HostIgnore bool `yaml:"host_ignore"`
	} `yaml:"git"`
	GitHub GitHubConfig `yaml:"github"`
	UV     struct {
		VMVenv bool `yaml:"vm_venv"`
	} `yaml:"uv"`
	NetworkExtras NetworkExtras `yaml:"network_extras"`
	MCPGateway    struct {
		Port    int                  `yaml:"port"`
		Servers map[string]MCPServer `yaml:"servers"`
	} `yaml:"mcp_gateway"`

	dir string // relative paths in the config resolve against it
}

type ClaudeConfig struct {
	SecretSource       secret.Source `yaml:"secret_source"`
	ExtraSystemPrompt  string        `yaml:"extra_system_prompt"`
	ConfigDir          string        `yaml:"config_dir"`
	MCPServers         []string      `yaml:"mcp_servers"`
	ClaudeAIConnectors []string      `yaml:"claude_ai_connectors"`
	HostShare          struct {
		Sessions bool     `yaml:"sessions"`
		Config   bool     `yaml:"config"`
		Skills   []string `yaml:"skills"`
	} `yaml:"host_share"`
}

type DockerConfig struct {
	Enabled   bool      `yaml:"enabled"`
	Disk      string    `yaml:"disk"`
	Emulation bool      `yaml:"emulation"`
	ECR       ECRConfig `yaml:"ecr"`
}

type MCPServer struct {
	URL                string        `yaml:"url"`
	ClientID           string        `yaml:"client_id"`
	ClientSecretSource secret.Source `yaml:"client_secret_source"`
	CallbackPort       int           `yaml:"callback_port"`
	Scope              string        `yaml:"scope"`
}

type NetworkExtras struct {
	TLSIntercept []string `yaml:"tls_intercept"`
}

type ECRConfig struct {
	Enabled      bool          `yaml:"enabled"`
	Registry     string        `yaml:"registry"`
	SecretSource secret.Source `yaml:"secret_source"`
}

type GitHubConfig struct {
	Enabled        bool          `yaml:"enabled"`
	SecretSource   secret.Source `yaml:"secret_source"`
	FastSSHKeyscan bool          `yaml:"fast_ssh_keyscan"`
	Allow          []string      `yaml:"allow"`
}

func Path() string {
	if p := os.Getenv("MSBX_CONFIG"); p != "" {
		return p
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "msbx", "config.yaml")
}

func (c *Config) Image() string {
	s, _ := c.Microsandbox["image"].(string)
	return s
}

// Resolve resolves ~, and relative paths against the config file's dir
func (c *Config) Resolve(p string) string {
	p = ExpandHome(p)
	if p == "" || filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.dir, p)
}

func HostClaudeDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude")
}

func HostClaudeJSON() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".claude.json")
}

// MCPServer returns the mcp_gateway.servers entry, filled in from the host claude's server of the same name
// (url, and client_id and callback_port if the url matches). ok is false without a url
func (c *Config) MCPServer(name string) (s MCPServer, ok bool) {
	s = c.MCPGateway.Servers[name]
	var host struct {
		MCPServers map[string]struct {
			URL   string `json:"url"`
			OAuth struct {
				ClientID     string `json:"clientId"`
				CallbackPort int    `json:"callbackPort"`
			} `json:"oauth"`
		} `json:"mcpServers"`
	}
	if data, err := os.ReadFile(HostClaudeJSON()); err == nil && json.Unmarshal(data, &host) == nil {
		if h, found := host.MCPServers[name]; found && h.URL != "" {
			if s.URL == "" {
				s.URL = h.URL
			}
			if s.URL == h.URL {
				s.ClientID = cmp.Or(s.ClientID, h.OAuth.ClientID)
				s.CallbackPort = cmp.Or(s.CallbackPort, h.OAuth.CallbackPort)
			}
		}
	}
	return s, s.URL != ""
}

func ExpandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[1:])
	}
	return p
}

// StringList returns a YAML list or []string as strings.
func StringList(v any) []string {
	items, _ := v.([]any)
	out := make([]string, 0, len(items))
	for _, it := range items {
		out = append(out, fmt.Sprint(it))
	}
	if ss, ok := v.([]string); ok {
		out = append(out, ss...)
	}
	return out
}

func AppendUnique(list []string, items ...string) []string {
	out := append([]string{}, list...)
	for _, it := range items {
		if !slices.Contains(out, it) {
			out = append(out, it)
		}
	}
	return out
}
