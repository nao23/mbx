package config

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nao23/mbx/internal/wizard"
)

// wizardSettings lists the settings in mbx config edit's menu, in its order. cur is the config being edited.
func wizardSettings(cur *Config) []wizard.Setting {
	github := func(v wizard.Values) bool { return v.Bool("github.enabled") }
	docker := func(v wizard.Values) bool { return v.Bool("docker.enabled") }
	ecr := func(v wizard.Values) bool { return docker(v) && v.Bool("docker.ecr.enabled") }
	suggest := func(m map[string]string) func(wizard.Values) map[string]string {
		return func(wizard.Values) map[string]string { return m }
	}

	servers := HostMCPServers()
	for name := range cur.MCPGateway.Servers {
		servers = append(servers, name)
	}
	slices.Sort(servers)
	servers = slices.Compact(servers)
	skills := hostSkills()

	_, ghErr := exec.LookPath("gh")
	var found []string
	if os.Getenv("GH_TOKEN") != "" {
		found = append(found, "GH_TOKEN is set")
	}
	ghSuggest := map[string]string{"env": "GH_TOKEN"}
	if ghErr == nil {
		found = append(found, "gh is installed")
		ghSuggest["command"] = "gh auth token"
	}
	ghDesc := "git and gh in the VM get a token for github.com"
	if len(found) > 0 {
		ghDesc += " (" + strings.Join(found, ", ") + ")"
	}

	return []wizard.Setting{
		{
			Name: "Claude token", Path: "claude.secret_source", Title: "Where does claude's token (claude setup-token) come from?",
			Input: wizard.Secret{Suggest: suggest(map[string]string{"env": "CLAUDE_CODE_OAUTH_TOKEN"})},
		},
		{
			Name: "MCP servers", Path: "claude.mcp_servers", Title: "MCP servers to use",
			Description: "Through the MCP gateway, with mbx's own logins (mbx mcp login). From the host's ~/.claude.json and mcp_gateway.servers",
			Show: func(v wizard.Values) bool {
				return len(servers) > 0 || len(v.List("claude.mcp_servers")) > 0
			},
			Input: wizard.Multi{Options: servers},
		},
		{
			Name: "Connectors", Path: "claude.claude_ai_connectors", Title: "claude.ai connectors to use",
			Description: "Display names, comma-separated (e.g. Slack, Notion). They use the host claude's login",
			Input:       wizard.List{},
		},
		{
			Name: "Skills", Path: "claude.host_share.skills", Title: "Host skills to share",
			Description: "Read-only, from ~/.claude/skills. Skills needing host tools won't work in the VM",
			Show: func(v wizard.Values) bool {
				return len(skills) > 0 || len(v.List("claude.host_share.skills")) > 0
			},
			Input: wizard.Multi{Options: skills, All: "*"},
		},
		{
			Name: "GitHub", Path: "github.enabled", Title: "Use GitHub?", Description: ghDesc,
			Input: wizard.Toggle{},
			Then:  []string{"github.secret_source"},
		},
		{
			Name: "GitHub token", Path: "github.secret_source", Title: "Where does the GitHub token come from?",
			Show:  github,
			Input: wizard.Secret{Suggest: suggest(ghSuggest)},
		},
		{
			Name: "Actions storage", Path: "github.allow", Title: "Allow GitHub Actions' artifact and log storage?",
			Description: "For gh run download and gh run view --log",
			Show:        github,
			Input:       wizard.Member{Item: "actions_storage"},
		},
		{
			Name: "Docker", Path: "docker.enabled", Title: "Use Docker in the VM?",
			Input: wizard.Toggle{},
		},
		{
			Name: "ECR", Path: "docker.ecr.enabled", Title: "Log Docker in to an ECR registry?",
			Show:  docker,
			Input: wizard.Toggle{},
			Then:  []string{"docker.ecr.registry", "docker.ecr.secret_source"},
		},
		{
			Name: "ECR registry", Path: "docker.ecr.registry", Title: "ECR registry",
			Show:  ecr,
			Input: wizard.Text{Placeholder: "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com", Required: true},
		},
		{
			Name: "ECR password", Path: "docker.ecr.secret_source", Title: "Where does the ECR password come from?",
			Show: ecr,
			Input: wizard.Secret{Suggest: func(v wizard.Values) map[string]string {
				m := map[string]string{"env": "ECR_TOKEN"}
				if region := ECRRegion(v.String("docker.ecr.registry")); region != "" {
					m["command"] = "aws ecr get-login-password --region " + region
				}
				return m
			}},
		},
		{
			Name: "Domains", Path: "microsandbox_config.network.allow", Title: "More domains to allow",
			Description: "One per line (*.example.com for subdomains). What the agent and the features above need is added by mbx",
			Input:       wizard.Lines{},
		},
	}
}

// hostSkills lists the skills in the host's ~/.claude/skills.
func hostSkills() []string {
	var skills []string
	if entries, err := os.ReadDir(filepath.Join(HostClaudeDir(), "skills")); err == nil {
		for _, e := range entries {
			if !strings.HasPrefix(e.Name(), ".") {
				skills = append(skills, e.Name())
			}
		}
	}
	return skills
}
