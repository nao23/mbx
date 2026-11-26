package agent

import (
	"github.com/nao23/mbx/internal/feature"
	"github.com/nao23/mbx/internal/mcpgateway"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/shell"
)

type shellAgent struct {
	command []string // replaced by the command-line arguments when given
}

func (s *shellAgent) Feature(string, *mcpgateway.Registration) feature.Feature {
	return feature.Feature{Name: "shell"}
}

func (s *shellAgent) Prepare(*msb.Runner, string) error { return nil }

func (s *shellAgent) MCPServers() []string { return nil }

func (s *shellAgent) Command(r *msb.Runner, args []string) ([]string, string) {
	command := s.command
	if len(args) > 0 {
		command = args
	}
	r.Step(msb.OnVM, "Start "+command[0])
	return command, shell.Join(command)
}
