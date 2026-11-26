package feature

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nao23/mbx/internal/config"
)

// fakeGit points HOME at a new temp dir and makes gitConfig answer as git would for a global gitconfig
// with the given user.* settings (as git config --get-regexp prints them) and core.excludesfile
// (relative to HOME; git config --path returns it expanded). Empty means unset.
func fakeGit(t *testing.T, userLines, excludesfile string) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	if excludesfile != "" {
		excludesfile = filepath.Join(home, excludesfile)
	}
	orig := gitConfig
	t.Cleanup(func() { gitConfig = orig })
	gitConfig = func(args ...string) ([]byte, error) {
		out := map[string]string{"--get-regexp": userLines, "--path": excludesfile}[args[0]]
		if out == "" {
			return nil, errors.New("exit status 1") // git config's exit status when nothing is set
		}
		return []byte(out + "\n"), nil
	}
	return home
}

// git copies only the [user] section (escaped) and the excludesfile's target (symlink resolved), and
// nothing when both are off
func TestGit(t *testing.T) {
	cfg := loadConfig(t, "version: 1\n") // before fakeGit, as it sets HOME too
	home := fakeGit(t, "user.name Jane \"J\" Doe\nuser.email jane@example.com", ".gitignore")
	// ~/.gitignore is a symlink, as with dotfiles
	if err := os.WriteFile(filepath.Join(home, "dotfiles-gitignore"), []byte(".DS_Store\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "dotfiles-gitignore"), filepath.Join(home, ".gitignore")); err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"text": map[string]any{"path": "/root/.gitconfig",
			"content": "[user]\n\tname = \"Jane \\\"J\\\" Doe\"\n\temail = \"jane@example.com\"\n"}},
		map[string]any{"copy_file": map[string]any{
			"src": filepath.Join(home, "dotfiles-gitignore"), "dst": "/root/.config/git/ignore"}},
	}
	// The temp dir may be under a symlink (/var -> /private/var on macOS)
	want[1].(map[string]any)["copy_file"].(map[string]any)["src"], _ = filepath.EvalSymlinks(filepath.Join(home, "dotfiles-gitignore"))
	if got := gitPatches(t, cfg); !reflect.DeepEqual(got, want) {
		t.Errorf("gitPatches =\n%v\nwant\n%v", got, want)
	}

	cfg = loadConfig(t, "git: {host_user: false, host_ignore: false}\n")
	if got := gitPatches(t, cfg); len(got) != 0 {
		t.Errorf("gitPatches with both off = %v, want none", got)
	}
}

// Without core.excludesfile, git's default ~/.config/git/ignore is copied if it exists
func TestGitDefaultIgnore(t *testing.T) {
	cfg := loadConfig(t, "version: 1\n") // before fakeGit, as it sets HOME too
	home := fakeGit(t, "", "")
	if got := gitPatches(t, cfg); len(got) != 0 {
		t.Errorf("gitPatches with no user or ignore file = %v, want none", got)
	}

	ignore := filepath.Join(home, ".config", "git", "ignore")
	if err := os.MkdirAll(filepath.Dir(ignore), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ignore, []byte("*.swp\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := gitPatches(t, cfg)
	if len(got) != 1 || got[0].(map[string]any)["copy_file"].(map[string]any)["dst"] != "/root/.config/git/ignore" {
		t.Errorf("gitPatches = %v, want ~/.config/git/ignore copied", got)
	}
}

// gitPatches returns the patches newGit adds, or nil if it adds nothing.
func gitPatches(t *testing.T, cfg *config.Config) []any {
	t.Helper()
	f, err := newGit(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if f == nil {
		return nil
	}
	return f.Patches
}
