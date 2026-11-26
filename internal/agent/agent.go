// Package agent holds what msbx runs in a sandbox: claude or a shell.
package agent

import (
	"fmt"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/feature"
	"github.com/nao23/msbx/internal/mcpgateway"
	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/ui"
)

type Agent interface {
	// Feature returns what the agent adds to the sandbox. gateway is nil without MCP servers
	Feature(workspace string, gateway *mcpgateway.Registration) feature.Feature
	// Prepare runs on the host before the sandbox is created
	Prepare(r *msb.Runner, workspace string) error
	MCPServers() []string
	// Command returns the command to start in the VM and how dry-run shows it. args come after --
	Command(r *msb.Runner, args []string) (command []string, shown string)
}

// AllSecrets returns the agents' secrets with their sources in the config, whichever agent the config sets
func AllSecrets(cfg *config.Config) []feature.Feature {
	return []feature.Feature{{Name: "claude", Secret: claudeSecret(cfg)}}
}

func New(cfg *config.Config) (Agent, error) {
	switch cfg.Agent {
	case "claude":
		return newClaude(cfg), nil
	case "shell":
		return &shellAgent{command: cfg.Command}, nil
	}
	return nil, fmt.Errorf("unknown agent %q (claude or shell)", cfg.Agent)
}

func warn(msg string) { ui.Warn(msg) }
