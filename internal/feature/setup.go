package feature

import (
	"errors"
	"strings"

	"github.com/nao23/mbx/internal/config"
)

func newSetup(cfg *config.Config) (*Feature, error) {
	if strings.TrimSpace(cfg.Setup) == "" {
		return nil, nil
	}
	// The host's values could carry secrets into the VM
	if strings.Contains(cfg.Setup, "${") {
		return nil, errors.New("setup: write $VAR instead of ${VAR}; microsandbox expands ${...} in scripts from the host's environment")
	}
	return &Feature{Name: "setup", Setup: &Setup{"The config's setup script", cfg.Setup}}, nil
}
