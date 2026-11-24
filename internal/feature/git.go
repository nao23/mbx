package feature

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/nao23/msbx/internal/config"
)

// newGit copies the [user] section of the host's global gitconfig (not the rest, which has host-only
// settings such as credential helpers) and the global ignore file
func newGit(cfg *config.Config) (*Feature, error) {
	var patches []any
	if cfg.Git.HostUser {
		if s := hostGitUser(); s != "" {
			patches = append(patches, map[string]any{"text": map[string]any{"path": "/root/.gitconfig", "content": s}})
		}
	}
	if cfg.Git.HostIgnore {
		if src := hostGitIgnore(); src != "" {
			patches = append(patches, map[string]any{"copy_file": map[string]any{
				"src": src, "dst": "/root/.config/git/ignore"}})
		}
	}
	if len(patches) == 0 {
		return nil, nil
	}
	return &Feature{Name: "git", Patches: patches}, nil
}

func hostGitUser() string {
	out, err := gitConfig("--get-regexp", `^user\.`)
	if err != nil {
		return ""
	}
	var b strings.Builder
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, value, _ := strings.Cut(line, " ")
		if name, ok := strings.CutPrefix(key, "user."); ok {
			b.WriteString("\t" + name + " = " + gitQuote(value) + "\n")
		}
	}
	if b.Len() == 0 {
		return ""
	}
	return "[user]\n" + b.String()
}

// A variable so tests don't run git
var gitConfig = func(args ...string) ([]byte, error) {
	return exec.Command("git", append([]string{"config", "--global"}, args...)...).Output()
}

func gitQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// hostGitIgnore returns the global ignore file (core.excludesfile, else git's default) with symlinks
// resolved, or ""
func hostGitIgnore() string {
	out, _ := gitConfig("--path", "core.excludesfile")
	path := strings.TrimSpace(string(out))
	if path == "" {
		dir := os.Getenv("XDG_CONFIG_HOME")
		if dir == "" {
			home, _ := os.UserHomeDir()
			dir = filepath.Join(home, ".config")
		}
		path = filepath.Join(dir, "git", "ignore")
	}
	path, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if fi, err := os.Stat(path); err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	return path
}
