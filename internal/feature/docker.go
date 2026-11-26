package feature

import (
	_ "embed"

	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/msb"
)

//go:embed emulation.sh
var emulationScript string

//go:embed docker.sh
var dockerScript string

func newEmulation(cfg *config.Config) (*Feature, error) {
	if !cfg.Docker.Emulation {
		return nil, nil
	}
	return &Feature{Name: "docker.emulation", Setup: &Setup{"Run amd64 and other binaries under QEMU (no effect on arm64)", emulationScript}}, nil
}

// newDocker gives Docker an ext4 disk, as it can't create containers on the overlay root filesystem
func newDocker(cfg *config.Config) (*Feature, error) {
	if !cfg.Docker.Enabled {
		return nil, nil
	}
	return &Feature{
		Name: "docker",
		Flags: []msb.Flag{{
			Name:  "--mount-owned",
			Value: "/var/lib/docker:kind=disk,size=" + cfg.Docker.Disk,
			Note:  "ext4 disk for Docker (removed with the sandbox)",
		}},
		Setup: &Setup{"Start Docker Engine, relaying container DNS through dnsmasq", dockerScript},
	}, nil
}
