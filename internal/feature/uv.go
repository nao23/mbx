package feature

import (
	_ "embed"

	"github.com/nao23/mbx/internal/config"
)

//go:embed uv.sh
var uvScript string

func newUV(cfg *config.Config) (*Feature, error) {
	if !cfg.UV.VMVenv {
		return nil, nil
	}
	return &Feature{Name: "uv", Setup: &Setup{"Keep uv's project virtualenvs in the VM (/root/.venvs/<project path>)", uvScript}}, nil
}
