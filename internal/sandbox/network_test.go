package sandbox

import (
	"slices"
	"testing"

	"github.com/nao23/msbx/internal/config"
)

// The network config merges the user's with the features' hosts and rules in order, bypasses TLS only for
// hosts that needn't be intercepted, and leaves the config unmodified
func TestBuildNetwork(t *testing.T) {
	cfg := &config.Config{
		Microsandbox: map[string]any{
			"network": map[string]any{
				"policy": "none",
				"allow":  []any{"pypi.org", "github.com:tcp:443", "10.0.0.0/8"},
				"tls":    map[string]any{"bypass": []any{"preset.example.com"}},
			},
			"secrets": map[string]any{
				"MY_TOKEN": map[string]any{"allow": []any{"api.example.com"}},
			},
		},
		NetworkExtras: config.NetworkExtras{TLSIntercept: []string{"pypi.org"}},
	}
	net := buildNetwork(cfg, []allowHost{
		{name: "api.anthropic.com", intercept: true},
		{name: "platform.claude.com"},
		{name: "api.example.com"}, // already added as a secret destination
		{name: "host:tcp:41837"},
	}, []string{"140.82.112.0/20:tcp:22"})

	// IP rules first, then the user's, the secret destinations, and hosts
	if got, want := net["allow"], []string{"140.82.112.0/20:tcp:22", "pypi.org", "github.com:tcp:443", "10.0.0.0/8",
		"api.example.com", "api.anthropic.com", "platform.claude.com", "host:tcp:41837"}; !slices.Equal(got.([]string), want) {
		t.Errorf("allow = %v, want %v", got, want)
	}
	if net["policy"] != "none" {
		t.Errorf("policy = %v, want the user's value kept", net["policy"])
	}
	tls := net["tls"].(map[string]any)
	if tls["enabled"] != true {
		t.Errorf("tls.enabled = %v, want true", tls["enabled"])
	}
	// Only domains that aren't intercepted: not secret destinations, tls_intercept, or rules with a port/CIDR
	if got, want := tls["bypass"], []string{"preset.example.com", "platform.claude.com"}; !slices.Equal(got.([]string), want) {
		t.Errorf("tls.bypass = %v, want %v", got, want)
	}

	// The config itself isn't modified
	orig := cfg.Microsandbox["network"].(map[string]any)
	if len(orig["allow"].([]any)) != 3 || len(orig["tls"].(map[string]any)) != 1 {
		t.Errorf("config was modified: %v", orig)
	}
}

// The user's tls.enabled: false is kept
func TestBuildNetworkKeepsTLSEnabled(t *testing.T) {
	cfg := &config.Config{Microsandbox: map[string]any{
		"network": map[string]any{"tls": map[string]any{"enabled": false}},
	}}
	if got := buildNetwork(cfg, nil, nil)["tls"].(map[string]any)["enabled"]; got != false {
		t.Errorf("tls.enabled = %v, want the user's false kept", got)
	}
}
