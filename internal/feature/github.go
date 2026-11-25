package feature

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strings"
	"time"

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
		Secret: &Secret{Env: "GH_TOKEN", Hosts: []string{"github.com", "api.github.com"}, Source: g.SecretSource},
		Allow:  []string{"codeload.github.com", "*.githubusercontent.com", "ghcr.io"},
		Setup:  &Setup{"Rewrite git's GitHub SSH URLs to HTTPS with $GH_TOKEN (a placeholder, swapped on send)", githubScript},
	}
	if g.FastSSHKeyscan {
		f.IPRules = githubSSHRules
	}
	return f, nil
}

// githubMetaURL lists GitHub's IP ranges. It needs no auth (60 requests an hour per IP). A variable for tests
var githubMetaURL = "https://api.github.com/meta"

// githubSSHRules allows SSH to GitHub's git IP ranges (IPv4 only). microsandbox waits up to 5s on a connection
// matching a domain rule to read its TLS SNI, which stalls clients that wait for the server first, like
// ssh-keyscan. Without the ranges, SSH still works, just with the wait
func githubSSHRules() []string {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(githubMetaURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var meta struct {
		Git []string `json:"git"`
	}
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&meta) != nil {
		return nil
	}
	var rules []string
	for _, cidr := range meta.Git {
		if !strings.Contains(cidr, ":") {
			rules = append(rules, cidr+":tcp:22")
		}
	}
	return rules
}
