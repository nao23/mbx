// Package feature holds what mbx adds to a sandbox for each enabled feature. The agent adds to it in the
// same form.
package feature

import (
	"github.com/nao23/mbx/internal/config"
	"github.com/nao23/mbx/internal/msb"
	"github.com/nao23/mbx/internal/secret"
)

type Feature struct {
	Name    string   // the config key that enables it, or the agent's name
	Secret  *Secret  // the VM gets a placeholder, swapped for the real value only when sent to one of Hosts
	Allow   []string // domains, and rules by IP (CIDR:proto:port), which go before the domains
	Flags   []msb.Flag
	Patches []any // applied at creation, not on reconnect
	Setup   *Setup
}

type Secret struct {
	Env    string
	Hosts  []string
	Source secret.Source
}

type Setup struct {
	Comment string
	Script  string
}

// constructors are in setup script order (ECR logs in to Docker, so it comes after it; the config's setup
// runs last, with everything ready). Each returns nil when disabled.
var constructors = []func(cfg *config.Config) (*Feature, error){
	newEmulation,
	newDocker,
	newGit,
	newUV,
	newECR,
	newGitHub,
	newSetup,
}

func Enabled(cfg *config.Config) ([]Feature, error) {
	var out []Feature
	for _, newFeature := range constructors {
		f, err := newFeature(cfg)
		if err != nil {
			return nil, err
		}
		if f != nil {
			out = append(out, *f)
		}
	}
	return out, nil
}

// AllSecrets returns the features' secrets with their sources in the config, whether or not the config
// enables the features
func AllSecrets(cfg *config.Config) []Feature {
	return []Feature{
		{Name: "docker.ecr", Secret: ecrSecret(cfg)},
		{Name: "github", Secret: githubSecret(cfg)},
	}
}
