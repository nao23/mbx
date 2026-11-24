// Package feature holds what msbx adds to a sandbox for each enabled feature. The agent adds to it in the
// same form.
package feature

import (
	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/secret"
)

type Feature struct {
	Name   string  // the config key that enables it, or the agent's name
	Secret *Secret // the VM gets a placeholder, swapped for the real value only when sent to Host
	Allow  []string
	// IPRules are allow rules by IP, placed before the domains. A func, as fetching them may take a request
	IPRules func() []string
	Flags   []msb.Flag
	Patches []any // applied at creation, not on reconnect
	Setup   *Setup
}

type Secret struct {
	Env    string
	Host   string
	Source secret.Source
}

type Setup struct {
	Comment string
	Script  string
}

// constructors are in setup script order (ECR logs in to Docker, so it comes after it). Each returns nil
// when disabled.
var constructors = []func(cfg *config.Config) (*Feature, error){
	newEmulation,
	newDocker,
	newGit,
	newUV,
	newECR,
	newGitHub,
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
