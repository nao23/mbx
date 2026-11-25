package feature

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
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
		f.Allow = append(f.Allow, githubSSHRules()...)
	}
	for _, group := range g.Allow {
		switch group {
		case "actions_storage":
			f.Allow = append(f.Allow, githubActionsStorage()...)
		default:
			return nil, fmt.Errorf("github.allow: unknown group %q (known: actions_storage)", group)
		}
	}
	return f, nil
}

// githubMetaURL lists GitHub's hosts and IP ranges. It needs no auth (60 requests an hour per IP). A variable
// for tests
var githubMetaURL = "https://api.github.com/meta"

// githubMeta fetches /meta once per process, as features are built more than once per run
var githubMeta = sync.OnceValue(fetchGitHubMeta)

type gitHubMeta struct {
	Git     []string `json:"git"`
	Domains struct {
		Actions []string `json:"actions"`
	} `json:"domains"`
}

func fetchGitHubMeta() *gitHubMeta {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(githubMetaURL)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var meta gitHubMeta
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&meta) != nil {
		return nil
	}
	return &meta
}

// githubSSHRules allows SSH to GitHub's git IP ranges (IPv4 only). microsandbox waits up to 5s on a connection
// matching a domain rule to read its TLS SNI, which stalls clients that wait for the server first, like
// ssh-keyscan. Without the ranges, SSH still works, just with the wait
func githubSSHRules() []string {
	meta := githubMeta()
	if meta == nil {
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

// githubActionsStorage returns the Actions artifact and log storage hosts (gh run download, gh run view --log).
// They're Azure Blob Storage accounts, so only GitHub's are allowed, not *.blob.core.windows.net. The rest of
// /meta's actions domains are for runners, and mostly under *.githubusercontent.com already
func githubActionsStorage() []string {
	meta := githubMeta()
	if meta == nil {
		return nil
	}
	var hosts []string
	for _, h := range meta.Domains.Actions {
		if strings.HasPrefix(h, "productionresultssa") {
			hosts = append(hosts, h)
		}
	}
	return hosts
}
