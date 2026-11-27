package feature

import (
	"strings"

	"github.com/nao23/mbx/internal/config"
)

func newSetup(cfg *config.Config) (*Feature, error) {
	if strings.TrimSpace(cfg.Setup) == "" {
		return nil, nil
	}
	return &Feature{Name: "setup", Setup: &Setup{"The config's setup script", cfg.Setup}}, nil
}
