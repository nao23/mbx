package feature

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
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

var githubHosts = []string{"codeload.github.com", "*.githubusercontent.com", "ghcr.io"}

// github sends GH_TOKEN (from the host's GH_TOKEN) to github.com (git) and api.github.com (gh), and allows
// only its fixed hosts without fast_ssh_keyscan
func TestGitHub(t *testing.T) {
	f, _ := newGitHub(loadConfig(t, "github: {enabled: true}\n"))
	if f.Secret.Env != "GH_TOKEN" || !slices.Equal(f.Secret.Hosts, []string{"github.com", "api.github.com"}) ||
		f.Secret.Source.Env != "GH_TOKEN" {
		t.Errorf("Secret = %+v", f.Secret)
	}
	if !slices.Equal(f.Allow, githubHosts) {
		t.Errorf("Allow = %v, want %v", f.Allow, githubHosts)
	}
}

// fast_ssh_keyscan also allows port 22 to GitHub's IPv4 git ranges from /meta, fetched once per process, and
// nothing more if it can't be read
func TestGitHubSSHRules(t *testing.T) {
	requests := 0
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.Write([]byte(`{"git": ["192.30.252.0/22", "2a0a:a440::/29", "140.82.112.0/20"], "web": ["1.2.3.4/32"]}`))
	}))
	defer meta.Close()
	origURL, origMeta := githubMetaURL, githubMeta
	t.Cleanup(func() { githubMetaURL, githubMeta = origURL, origMeta })
	useMeta := func(url string) {
		githubMetaURL = url
		githubMeta = sync.OnceValue(fetchGitHubMeta)
	}
	cfg := loadConfig(t, "github: {enabled: true, fast_ssh_keyscan: true}\n")

	useMeta(meta.URL)
	want := append(slices.Clone(githubHosts), "192.30.252.0/22:tcp:22", "140.82.112.0/20:tcp:22")
	for range 2 {
		if f, _ := newGitHub(cfg); !slices.Equal(f.Allow, want) {
			t.Errorf("Allow = %v, want %v", f.Allow, want)
		}
	}
	if requests != 1 {
		t.Errorf("/meta fetched %d times, want once", requests)
	}

	useMeta(meta.URL + "/missing\x00") // an invalid URL: the request fails
	if f, _ := newGitHub(cfg); !slices.Equal(f.Allow, githubHosts) {
		t.Errorf("Allow when /meta can't be read = %v, want %v", f.Allow, githubHosts)
	}
}

// docker.ecr takes the token from the host's ECR_TOKEN unless secret_source says otherwise, allows the
// region's S3 host, and is an error without docker, a registry, or a secret source
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
	// The token defaults to the host's ECR_TOKEN; the region gives the S3 host
	if s := f.Secret; !slices.Equal(s.Hosts, []string{"123456789012.dkr.ecr.ap-northeast-1.amazonaws.com"}) ||
		s.Source.Env != "ECR_TOKEN" {
		t.Errorf("Secret = %+v", s)
	}
	if got := f.Allow; !slices.Equal(got, []string{"prod-ap-northeast-1-starport-layer-bucket.s3.ap-northeast-1.amazonaws.com"}) {
		t.Errorf("Allow = %v", got)
	}

	// An explicit secret_source wins, and a registry without a region allows no S3 host
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
		"docker: {ecr: {enabled: true, registry: registry.example.com, secret_source: {}}}\n",
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
