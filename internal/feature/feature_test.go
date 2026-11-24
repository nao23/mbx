package feature

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"mvdan.cc/sh/v3/syntax"

	"github.com/nao23/msbx/internal/config"
	"github.com/nao23/msbx/internal/msb"
	"github.com/nao23/msbx/internal/testutil"
)

// Tests run no external commands: git config finds nothing unless a test fakes it (fakeGit), and PATH
// is empty so any other command fails instead of running
func TestMain(m *testing.M) {
	os.Setenv("PATH", "")
	gitConfig = func(...string) ([]byte, error) { return nil, errors.New("git isn't run in tests") }
	os.Exit(m.Run())
}

// loadConfig loads yamlText as the config file (see testutil.WriteConfig), with an empty host ~/.claude.
func loadConfig(t *testing.T, yamlText string) *config.Config {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	testutil.WriteConfig(t, yamlText)
	cfg, err := config.Load("/work")
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// Enabled returns the enabled features in order (secrets and setup steps), and none when all are off
func TestEnabled(t *testing.T) {
	cfg := loadConfig(t, `
		docker:
		  ecr: {enabled: true, registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com}
		github: {enabled: true}
	`)
	features, err := Enabled(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The defaults enable emulation, docker and uv (git adds nothing with no host gitconfig)
	var envs, setups []string
	for _, f := range features {
		if f.Secret != nil {
			envs = append(envs, f.Secret.Env)
		}
		if f.Setup != nil {
			setups = append(setups, strings.Fields(f.Setup.Comment)[0])
		}
	}
	if want := []string{"ECR_TOKEN", "GH_TOKEN"}; !slices.Equal(envs, want) {
		t.Errorf("secrets = %v, want %v", envs, want)
	}
	// ECR logs in to Docker, so it comes after Docker starts
	if want := []string{"Run", "Start", "Keep", "Log", "Rewrite"}; !slices.Equal(setups, want) {
		t.Errorf("setup order (first words) = %v, want %v", setups, want)
	}

	cfg = loadConfig(t, "docker: {enabled: false, emulation: false}\nuv: {vm_venv: false}\n")
	if features, _ := Enabled(cfg); len(features) != 0 {
		t.Errorf("Enabled with nothing enabled = %d features, want none", len(features))
	}
}

// docker mounts a disk of the configured size at /var/lib/docker, and is nil when disabled
func TestDocker(t *testing.T) {
	f, _ := newDocker(loadConfig(t, "docker: {disk: 30G}\n"))
	if got := msb.FlagArgs(f.Flags); !slices.Equal(got, []string{"--mount-owned", "/var/lib/docker:kind=disk,size=30G"}) {
		t.Errorf("flags = %v", got)
	}
	if f, _ := newDocker(loadConfig(t, "docker: {enabled: false}\n")); f != nil {
		t.Errorf("newDocker with docker disabled = %+v, want nil", f)
	}
}

// github sends GH_TOKEN (from gh auth token) to github.com, and adds IP rules only with fast_ssh_keyscan
func TestGitHub(t *testing.T) {
	f, _ := newGitHub(loadConfig(t, "github: {enabled: true}\n"))
	if f.Secret.Env != "GH_TOKEN" || f.Secret.Host != "github.com" || f.Secret.Source.Command != "gh auth token" {
		t.Errorf("Secret = %+v", f.Secret)
	}
	if f.IPRules != nil {
		t.Error("IPRules set without fast_ssh_keyscan")
	}
	if f, _ := newGitHub(loadConfig(t, "github: {enabled: true, fast_ssh_keyscan: true}\n")); f.IPRules == nil {
		t.Error("IPRules not set with fast_ssh_keyscan")
	}
}

// docker.ecr derives the token command and S3 host from the registry's region, prefers an explicit
// secret_source, and is an error without docker, a registry, or a way to get the token
func TestECR(t *testing.T) {
	cfg := loadConfig(t, `
		agent: shell
		docker:
		  ecr: {enabled: true, registry: 123456789012.dkr.ecr.ap-northeast-1.amazonaws.com}
	`)
	f, err := newECR(cfg)
	if err != nil {
		t.Fatal(err)
	}
	// The token defaults to aws ecr get-login-password for the registry's region
	if s := f.Secret; s.Host != "123456789012.dkr.ecr.ap-northeast-1.amazonaws.com" ||
		s.Source.Command != "aws ecr get-login-password --region ap-northeast-1" {
		t.Errorf("Secret = %+v", s)
	}
	if got := f.Allow; !slices.Equal(got, []string{"prod-ap-northeast-1-starport-layer-bucket.s3.ap-northeast-1.amazonaws.com"}) {
		t.Errorf("Allow = %v", got)
	}

	// An explicit secret_source wins, and a registry without a region then needs nothing more
	cfg = loadConfig(t, `
		docker:
		  ecr: {enabled: true, registry: registry.example.com, secret_source: {env: ECR}}
	`)
	if f, err = newECR(cfg); err != nil {
		t.Fatal(err)
	}
	if src := f.Secret.Source; src.Env != "ECR" {
		t.Errorf("explicit secret_source not used: %+v", src)
	}
	if got := f.Allow; got != nil {
		t.Errorf("Allow without a region = %v, want none", got)
	}

	for _, yamlText := range []string{
		"docker: {enabled: false, ecr: {enabled: true, registry: 1.dkr.ecr.r.amazonaws.com}}\n",
		"docker: {ecr: {enabled: true}}\n",
		"docker: {ecr: {enabled: true, registry: registry.example.com}}\n",
	} {
		if _, err := newECR(loadConfig(t, yamlText)); err == nil {
			t.Errorf("newECR(%q): want an error", yamlText)
		}
	}
}

// The scripts go into msb create's config, where microsandbox expands ${VAR} from the host's environment
// (which holds the real tokens), so they must not use that form. They also must parse as POSIX sh
func TestScripts(t *testing.T) {
	parser := syntax.NewParser(syntax.Variant(syntax.LangPOSIX))
	for name, script := range map[string]string{
		"emulation.sh": emulationScript,
		"docker.sh":    dockerScript,
		"uv.sh":        uvScript,
		"ecr.sh":       ecrScript,
		"github.sh":    githubScript,
	} {
		if strings.Contains(script, "${") {
			t.Errorf("%s contains ${...}, which microsandbox expands on the host", name)
		}
		if _, err := parser.Parse(strings.NewReader(script), name); err != nil {
			t.Errorf("%s doesn't parse: %v", name, err)
		}
	}
}
