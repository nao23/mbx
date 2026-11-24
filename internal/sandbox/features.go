package sandbox

import (
	"github.com/nao23/msbx/internal/agent"
	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/feature"
	"github.com/nao23/msbx/internal/mcpgateway"
)

// features returns the agent's and the enabled features' additions, the agent's first. gateway may be nil.
func features(cfg *config.Config, workspace string, gateway *mcpgateway.Registration) []feature.Feature {
	ag, _ := agent.New(cfg)       // checked by validate
	fs, _ := feature.Enabled(cfg) // checked by validate
	return append([]feature.Feature{ag.Feature(workspace, gateway)}, fs...)
}
