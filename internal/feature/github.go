package feature

import (
	_ "embed"
	"os/exec"
	"strings"

	"github.com/nao23/msbx/internal/config"
)

//go:embed github.sh
var githubScript string

func newGitHub(cfg *config.Config) (*Feature, error) {
	g := cfg.GitHub
	if !g.Enabled {
		return nil, nil
	}
	f := &Feature{
		Name:   "github",
		Secret: &Secret{Env: "GH_TOKEN", Host: "github.com", Source: g.SecretSource},
		Allow:  []string{"api.github.com", "codeload.github.com", "*.githubusercontent.com", "ghcr.io"},
		Setup:  &Setup{"Rewrite git's GitHub SSH URLs to HTTPS with $GH_TOKEN (a placeholder, swapped on send)", githubScript},
	}
	if g.FastSSHKeyscan {
		f.IPRules = githubSSHRules
	}
	return f, nil
}

// githubSSHRules allows SSH to GitHub's git IP ranges. microsandbox waits up to 5s on a connection matching a
// domain rule to read its TLS SNI, which stalls clients that wait for the server first, like ssh-keyscan
func githubSSHRules() []string {
	out, err := exec.Command("gh", "api", "meta", "--jq", `.git[] | select(contains(":") | not) | . + ":tcp:22"`).Output()
	if err != nil {
		return nil
	}
	return strings.Fields(string(out))
}
