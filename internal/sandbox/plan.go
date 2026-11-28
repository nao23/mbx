package sandbox

import (
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/mcpgateway"
	"github.com/nao23/mbx/internal/msb"
)

type plan struct {
	conf    map[string]any // --conf (microsandbox_config without network)
	net     map[string]any // --net-conf
	flags   []msb.Flag
	ipRules []string // in net, collapsed in dry-run output
}

// Single-valued flags mbx sets itself; microsandbox_flags can't override them
var reservedFlags = []string{"name", "conf", "net-conf", "workdir"}

// microsandboxFlags passes microsandbox_flags uninterpreted: a list repeats the flag,
// true is a bare flag, false or null omits it.
func microsandboxFlags(cfg *config.Config) ([]msb.Flag, error) {
	names := make([]string, 0, len(cfg.MicrosandboxFlags))
	for k := range cfg.MicrosandboxFlags {
		names = append(names, k)
	}
	sort.Strings(names)
	const note = "microsandbox_flags in the config"
	var flags []msb.Flag
	for _, k := range names {
		name := strings.TrimPrefix(k, "--")
		if slices.Contains(reservedFlags, name) {
			return nil, fmt.Errorf("microsandbox_flags: --%s is set by mbx and can't be overridden", name)
		}
		switch v := cfg.MicrosandboxFlags[k].(type) {
		case nil:
		case bool:
			if v {
				flags = append(flags, msb.Flag{Name: "--" + name, Note: note})
			}
		case []any:
			for _, it := range v {
				flags = append(flags, msb.Flag{Name: "--" + name, Value: fmt.Sprint(it), Note: note})
			}
		default:
			flags = append(flags, msb.Flag{Name: "--" + name, Value: fmt.Sprint(v), Note: note})
		}
	}
	return flags, nil
}

// buildPlan builds what's passed to msb create. gateway may be nil.
func buildPlan(cfg *config.Config, workspace, name string, gateway *mcpgateway.Registration) plan {
	conf := map[string]any{}
	for k, v := range cfg.Microsandbox {
		if k != "network" {
			conf[k] = v
		}
	}
	features := features(cfg, workspace, gateway)
	// The features' patches go after the user's own
	for _, f := range features {
		if len(f.Patches) > 0 {
			patches, _ := conf["patches"].([]any)
			conf["patches"] = append(append([]any{}, patches...), f.Patches...)
		}
	}

	// The workspace is mounted at the host's path, so its session directory (projects/<path>) matches the host's
	flags := []msb.Flag{
		{Name: "--name", Value: name, Note: "Sandbox"},
		{Name: "--label", Value: labelMbx + "=1", Note: "Labels that mark it as mbx's, for mbx ls"},
		{Name: "--label", Value: labelAgent + "=" + cfg.Agent, Note: "Labels that mark it as mbx's, for mbx ls"},
		{Name: "--label", Value: labelWorkspace + "=" + workspace, Note: "Labels that mark it as mbx's, for mbx ls"},
		{Name: "--volume", Value: workspace + ":" + workspace, Note: "Workspace (same path as on the host)"},
		{Name: "--workdir", Value: workspace, Note: "Workspace (same path as on the host)"},
	}
	extra, _ := microsandboxFlags(cfg) // checked by validate
	flags = append(flags, extra...)
	for _, f := range features {
		if s := f.Secret; s != nil {
			flags = append(flags, msb.Flag{
				Name:  "--secret",
				Value: s.Env + "@" + strings.Join(s.Hosts, ","),
				Note:  "Agent and feature tokens (the VM gets a placeholder; the real value is swapped in on send)",
			})
		}
	}
	for _, f := range features {
		flags = append(flags, f.Flags...)
	}

	var hosts []allowHost
	var ipRules []string
	for _, f := range features {
		if s := f.Secret; s != nil {
			for _, h := range s.Hosts {
				hosts = append(hosts, allowHost{name: h, intercept: true})
			}
		}
		for _, h := range f.Allow {
			if isIPRule(h) {
				ipRules = append(ipRules, h)
			} else {
				hosts = append(hosts, allowHost{name: h})
			}
		}
	}
	return plan{conf: conf, net: buildNetwork(cfg, hosts, ipRules), flags: flags, ipRules: ipRules}
}

// isIPRule reports whether an allow rule is by IP (e.g. 140.82.112.0/20:tcp:22), not by domain
func isIPRule(rule string) bool {
	addr, _, _ := strings.Cut(rule, ":")
	_, prefixErr := netip.ParsePrefix(addr)
	_, addrErr := netip.ParseAddr(addr)
	return prefixErr == nil || addrErr == nil
}

// collapseRules shows the given allow rules (such as GitHub's 100+ SSH IP ranges) as a one-line count.
func collapseRules(yamlText string, rules []string) string {
	var out []string
	n, at := 0, -1
	for _, line := range strings.Split(yamlText, "\n") {
		if rule, ok := strings.CutPrefix(strings.TrimSpace(line), "- "); ok && slices.Contains(rules, rule) {
			if at < 0 {
				at = len(out)
				out = append(out, "")
			}
			n++
			continue
		}
		out = append(out, line)
	}
	if at >= 0 {
		out[at] = fmt.Sprintf("    # (%d IP rules from the features, placed before the domains)", n)
	}
	return strings.Join(out, "\n")
}
