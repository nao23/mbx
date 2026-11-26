package msb

import (
	"fmt"
	"os/exec"
	"strings"
)

// installHints says how to get each host command mbx needs.
var installHints = map[string]string{
	"msb":    "install microsandbox (https://docs.microsandbox.dev)",
	"docker": "install Docker on the host (Docker Desktop, Lima, etc.) and start it",
}

// Require returns an error naming the commands missing from PATH, with how to get each.
func Require(names ...string) error {
	var missing []string
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			missing = append(missing, fmt.Sprintf("%s isn't found in PATH: %s", name, installHints[name]))
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return fmt.Errorf("%s", strings.Join(missing, "; "))
}
