package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/huh"
	"golang.org/x/term"
	"gopkg.in/yaml.v3"

	"github.com/nao23/msbx/internal/ui"
	"github.com/nao23/msbx/internal/wizard"
)

// Edit is msbx config edit: it edits the workspace's config file, or with newFile makes a new one for the
// workspace starting from config.yaml, from a menu of the settings in wizard_settings.go (the menu is the
// wizard package's).
func Edit(workspace string, newFile bool) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("config edit shows a menu on a terminal; without one, write the config file by hand (see the README)")
	}
	target, base, forDir, err := chooseFile(workspace, newFile)
	if err != nil {
		return err
	}
	cur, err := Parse(base)
	if err != nil {
		return fmt.Errorf("%s: %w", target, err)
	}
	values, err := toMap(cur)
	if err != nil {
		return err
	}

	f := wizard.File{
		Path:  target,
		Text:  base,
		Top:   []string{"version", "directories"},
		Check: func(text []byte) error { _, err := Parse(text); return err },
	}
	if len(base) == 0 {
		f.Fixed = append(f.Fixed, wizard.Change{Path: []string{"version"}, Value: 1})
	}
	if forDir {
		f.Fixed = append(f.Fixed, wizard.Change{Path: []string{"directories"}, Value: []string{tilde(workspace)}})
		f.Header = fmt.Sprintf("msbx's config for %s and below, used there instead of config.yaml (made by msbx config\nedit from config.yaml). Check it with msbx run --show-config", tilde(workspace))
	}
	changes, err := wizard.Edit(f, values, wizardSettings(cur))
	if err != nil || changes == nil {
		return err
	}
	if _, _, err := Choose(workspace); err != nil {
		return err
	}

	st := ui.For(os.Stdout)
	fmt.Println(st.Dim("  Check it with msbx run --show-config or --dry-run"))
	for _, c := range changes {
		if strings.Join(c.Path, ".") != "claude.mcp_servers" {
			continue
		}
		for _, s := range c.Value.([]string) {
			if !slices.Contains(cur.Claude.MCPServers, s) {
				fmt.Println(st.Dim("  Log in to " + s + " with: msbx mcp login " + s))
			}
		}
	}
	return nil
}

// chooseFile returns the file to write, the text to start from, and whether it's a new file for the
// workspace: the workspace's config file (config.yaml, made if missing), or with newFile a new one for the
// workspace starting from config.yaml.
func chooseFile(workspace string, newFile bool) (target string, base []byte, forDir bool, err error) {
	path, matched, err := Choose(workspace)
	if err != nil {
		return "", nil, false, err
	}
	st := ui.For(os.Stdout)
	if !newFile {
		why := "used where no other config file's directories has the directory"
		if matched != "" {
			why = "its directories has " + tilde(matched)
		}
		switch base, err = os.ReadFile(path); {
		case err == nil:
			fmt.Println(st.Bold("Editing "+tilde(path)) + st.Dim(" ("+why+"; msbx config edit --new makes one for this directory)"))
		case os.IsNotExist(err):
			err = nil
			fmt.Println(st.Bold("Making "+tilde(path)) + st.Dim(" ("+why+")"))
		}
		return path, base, false, err
	}
	if matched == workspace {
		return "", nil, false, fmt.Errorf("%s is already for %s; edit it with msbx config edit", tilde(path), tilde(workspace))
	}

	dir := Dir()
	name := filepath.Base(workspace) + ".yaml"
	err = wizard.Ask("File name (in "+tilde(dir)+")", huh.NewInput().
		Value(&name).
		Validate(func(s string) error {
			switch _, err := os.Stat(filepath.Join(dir, s)); {
			case !strings.HasSuffix(s, ".yaml") || strings.ContainsRune(s, filepath.Separator):
				return errors.New("a file name ending in .yaml")
			case s == DefaultFile:
				return errors.New(DefaultFile + " takes no directories")
			case err == nil:
				return errors.New(s + " exists")
			}
			return nil
		}))
	if err != nil {
		return "", nil, false, err
	}
	base, err = os.ReadFile(filepath.Join(dir, DefaultFile))
	if os.IsNotExist(err) {
		err = nil
	}
	return filepath.Join(dir, name), base, true, err
}

// toMap turns the config into a map as decoded from YAML, which the settings' values are taken from.
func toMap(cfg *Config) (map[string]any, error) {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	var m map[string]any
	return m, yaml.Unmarshal(data, &m)
}

// tilde shortens a path under the home dir to ~/...
func tilde(p string) string {
	home, _ := os.UserHomeDir()
	if rel, err := filepath.Rel(home, p); err == nil && home != "" && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}
