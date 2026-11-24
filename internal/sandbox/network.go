package sandbox

import (
	"slices"
	"strings"

	"github.com/nao23/msbx/internal/config"
)

// allowHost is a destination msbx allows. intercept turns on TLS interception, which secret substitution
// requires.
type allowHost struct {
	name      string
	intercept bool
}

// buildNetwork adds msbx's secret destinations and hosts to microsandbox_config.network. ipRules go first, as
// the first matching rule wins and IP rules skip microsandbox's wait on domain rules. It's a single config,
// as microsandbox replaces lists when layering configs.
func buildNetwork(cfg *config.Config, hosts []allowHost, ipRules []string) map[string]any {
	nm := map[string]any{}
	if n, ok := cfg.Microsandbox["network"].(map[string]any); ok {
		for k, v := range n {
			nm[k] = v
		}
	}

	allow := config.StringList(nm["allow"])
	intercept := append([]string{}, cfg.NetworkExtras.TLSIntercept...)
	// Secret substitution requires TLS interception
	for _, s := range secretAllowHosts(cfg) {
		allow = config.AppendUnique(allow, s)
		intercept = config.AppendUnique(intercept, s)
	}
	for _, h := range hosts {
		allow = config.AppendUnique(allow, h.name)
		if h.intercept {
			intercept = config.AppendUnique(intercept, h.name)
		}
	}

	nm["allow"] = append(append([]string{}, ipRules...), allow...)

	// Containers in the VM don't trust microsandbox's CA, so intercept nothing else
	tls := map[string]any{}
	if t, ok := nm["tls"].(map[string]any); ok {
		for k, v := range t {
			tls[k] = v
		}
	}
	if _, ok := tls["enabled"]; !ok {
		tls["enabled"] = true
	}
	bypass := config.StringList(tls["bypass"])
	for _, d := range allow {
		if isDomain(d) && !slices.Contains(intercept, d) {
			bypass = config.AppendUnique(bypass, d)
		}
	}
	tls["bypass"] = bypass
	nm["tls"] = tls
	return nm
}

func secretAllowHosts(cfg *config.Config) []string {
	var hosts []string
	secrets, _ := cfg.Microsandbox["secrets"].(map[string]any)
	for _, v := range secrets {
		if s, ok := v.(map[string]any); ok {
			hosts = config.AppendUnique(hosts, config.StringList(s["allow"])...)
		}
	}
	return hosts
}

// Rules with a port or CIDR (e.g. 140.82.112.0/20:tcp:22) aren't domains
func isDomain(s string) bool { return !strings.ContainsAny(s, ":/") }
