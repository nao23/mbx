package sandbox

import (
	"context"
	"fmt"
	"strings"

	"github.com/nao23/mbx/internal/feature"
	"github.com/nao23/mbx/internal/msb"
)

// setupLabel names the setup script in progress and errors.
const setupLabel = "setup script"

// setupScript joins the features' parts, each in a subshell so an early exit in one doesn't skip the rest.
func setupScript(features []feature.Feature) string {
	var parts []string
	for _, f := range features {
		if f.Setup != nil {
			parts = append(parts, "# "+f.Setup.Comment+"\n(\n"+strings.TrimSpace(f.Setup.Script)+"\n)")
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "set -e\n\n" + strings.Join(parts, "\n\n") + "\n"
}

// postStart starts the features' host tasks alongside the agent. The returned func stops those still running.
func postStart(r *msb.Runner, features []feature.Feature, name string) (stop func(), err error) {
	ctx, cancel := context.WithCancel(context.Background())
	for _, f := range features {
		if f.PostStart == nil {
			continue
		}
		r.Step(msb.HostToVM, f.PostStart.Title, f.PostStart.Detail)
		run, err := f.PostStart.Prepare(r, name)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		if run != nil {
			go run(ctx)
		}
	}
	return cancel, nil
}

// setupGuest runs the setup script on every start; its steps skip what's already done. The script is passed
// on stdin each time rather than registered when the sandbox is created, so a sandbox kept from an older mbx
// runs the current one.
func setupGuest(r *msb.Runner, features []feature.Feature, name string) error {
	s := setupScript(features)
	if s == "" {
		return nil
	}
	r.Step(msb.OnVM, "Set up the VM", "Runs the setup script for the enabled features, passed on stdin")
	if err := r.TaskInput(setupLabel, s, "exec", "--stream", name, "--", "sh", "-s"); err != nil {
		return fmt.Errorf("%s: %w", setupLabel, err)
	}
	return nil
}
