package sandbox

import (
	"fmt"
	"strings"

	"github.com/nao23/msbx/internal/feature"
	"github.com/nao23/msbx/internal/msb"
)

// setupScriptName is registered as a microsandbox script (/.msb/scripts/<name>, on PATH).
const setupScriptName = "msbx-setup"

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

// setupGuest runs the setup script on every start; its steps skip what's already done.
func setupGuest(r *msb.Runner, features []feature.Feature, name string) error {
	if setupScript(features) == "" {
		return nil
	}
	r.Step(msb.OnVM, "Set up the VM", "Runs "+setupScriptName+", registered in sandbox.yaml above, for the enabled features")
	if err := r.Task(setupScriptName, "exec", name, "--", setupScriptName); err != nil {
		return fmt.Errorf("%s: %w", setupScriptName, err)
	}
	return nil
}
