package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/term"

	"github.com/nao23/mbx/internal/ui"
)

// Defaults is the built-in defaults.yaml, which lists every setting with a description.
func Defaults() []byte { return defaultsYAML }

// List is mbx config ls: the config files with their directories, marking the one the workspace uses.
func List(workspace string) error {
	files, err := Files()
	if err != nil {
		return err
	}
	st := ui.For(os.Stdout)
	if len(files) == 0 {
		fmt.Println(st.Dim("No config files in " + tilde(Dir()) + ": the defaults alone apply"))
		return nil
	}
	used, _, chooseErr := Choose(workspace)
	width := 0
	for _, f := range files {
		width = max(width, len(filepath.Base(f.Path)))
	}
	for _, f := range files {
		mark, name := "  ", ui.Pad(filepath.Base(f.Path), width)
		if f.Path == used {
			mark, name = st.Green("* "), st.Bold(name)
		}
		var dirs []string
		for _, d := range f.Directories {
			dirs = append(dirs, tilde(d))
		}
		desc := st.Cyan(strings.Join(dirs, ", "))
		if f.Err != nil {
			desc = st.Red(f.Err.Error())
		}
		fmt.Println(mark + name + "   " + desc)
	}
	switch {
	case chooseErr != nil:
		ui.Error(chooseErr)
	case used == "":
		fmt.Println(st.Dim("\nNo file is used in " + tilde(workspace) + ": the defaults alone apply"))
	}
	return nil
}

// Edit is mbx config edit: it opens a config file in the editor and checks it once the editor exits,
// offering to open it again until it loads. The file is path if given, else name in the config dir,
// else the one the workspace uses; with newFile, a new one for the workspace alone (named name,
// by default after the workspace).
func Edit(workspace, name, path string, newFile bool) error {
	if !term.IsTerminal(int(os.Stdin.Fd())) || !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("config edit opens an editor on a terminal; without one, edit the config file directly")
	}
	if path == "" {
		var err error
		if path, err = chooseFile(workspace, name, newFile); err != nil {
			return err
		}
	}
	st := ui.For(os.Stdout)
	for {
		if err := openEditor(path); err != nil {
			return err
		}
		err := check(workspace, path)
		if err == nil {
			fmt.Println(st.Dim("  Check it with mbx run claude --show-config or --dry-run"))
			return nil
		}
		ui.Error(err)
		if !confirm("Edit it again?") {
			return fmt.Errorf("%s is saved, but the config has the error above", tilde(path))
		}
	}
}

// chooseFile returns the file to edit: name in the config dir, or without a name the one the workspace
// uses. With newFile, it writes a new one for the workspace first.
func chooseFile(workspace, name string, newFile bool) (string, error) {
	st := ui.For(os.Stdout)
	// A named file opens even when the config files don't load together (a directory in two of them),
	// so it can be fixed
	if name != "" && !newFile {
		path, err := namedFile(name)
		if err != nil {
			return "", err
		}
		if _, err := os.Stat(path); err != nil {
			n := strings.TrimSuffix(filepath.Base(path), ".yaml")
			return "", fmt.Errorf("%s doesn't exist; make it for %s with mbx config edit --new %s", tilde(path), tilde(workspace), n)
		}
		fmt.Println(st.Bold("Editing " + tilde(path)))
		return path, nil
	}

	used, matched, err := Choose(workspace)
	if err != nil {
		return "", fmt.Errorf("%w (fix it with mbx config edit NAME)", err)
	}
	if name == "" && !newFile {
		if used == "" {
			return "", fmt.Errorf("no config file is used in %s; make one for it with mbx config edit --new", tilde(workspace))
		}
		fmt.Println(st.Bold("Editing "+tilde(used)) + st.Dim(" (its directories has "+tilde(matched)+")"))
		return used, nil
	}

	if name == "" {
		name = filepath.Base(workspace)
	}
	path, err := namedFile(name)
	if err != nil {
		return "", err
	}
	if matched == workspace {
		return "", fmt.Errorf("%s is already for %s; edit it with mbx config edit", tilde(used), tilde(workspace))
	}
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("%s exists; give another name: mbx config edit --new NAME", tilde(path))
	}
	var base []byte
	from := ""
	if used != "" {
		if base, err = os.ReadFile(used); err != nil {
			return "", err
		}
		from = ", with the settings of " + filepath.Base(used)
	}
	if err := os.MkdirAll(Dir(), 0o700); err != nil {
		return "", err
	}
	fmt.Println(st.Bold("Making "+tilde(path)) + st.Dim(" (for "+tilde(workspace)+from+")"))
	return path, os.WriteFile(path, newFileText(workspace, base), 0o600)
}

// namedFile is the config dir's file for name, with .yaml added if left out.
func namedFile(name string) (string, error) {
	if strings.ContainsRune(name, filepath.Separator) {
		return "", fmt.Errorf("%s: give a file name in %s (--config takes a path)", name, tilde(Dir()))
	}
	if !strings.HasSuffix(name, ".yaml") {
		name += ".yaml"
	}
	return filepath.Join(Dir(), name), nil
}

// newFileText is a config file for the workspace, with the settings in base
// (another config file; its header, version and directories dropped).
func newFileText(workspace string, base []byte) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# mbx's config for %s and below. All settings and their defaults: mbx config defaults\n\n", tilde(workspace))
	fmt.Fprintf(&b, "version: 1\n\ndirectories: [%q]\n\n", tilde(workspace)) // quoted, as a bare ~ is null
	lines := strings.Split(string(base), "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
		lines = lines[1:]
	}
	var kept []string
	skipping := false // in the directories block, whose items are indented
	for _, l := range lines {
		if l == "" || l[0] != ' ' && l[0] != '-' {
			skipping = strings.HasPrefix(l, "directories:")
		}
		if !skipping && !strings.HasPrefix(l, "version:") {
			kept = append(kept, l)
		}
	}
	for len(kept) > 0 && strings.TrimSpace(kept[0]) == "" {
		kept = kept[1:]
	}
	b.WriteString(strings.Join(kept, "\n"))
	return []byte(strings.TrimRight(b.String(), "\n") + "\n")
}

// openEditor runs $VISUAL or $EDITOR (vi if neither is set) on path. The variable may hold arguments,
// so it's run by the shell.
func openEditor(path string) error {
	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}
	cmd := exec.Command("sh", "-c", editor+` "$1"`, "sh", path)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor (%s): %w", editor, err)
	}
	return nil
}

// check loads the edited file and the config dir's files, and says which file the workspace now uses
// if it's another (directories may have been changed).
func check(workspace, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if _, err := Parse(data); err != nil {
		return fmt.Errorf("%s: %w", tilde(path), err)
	}
	used, _, err := Choose(workspace)
	if err != nil {
		return err
	}
	if used != path {
		now := "no file (the defaults alone)"
		if used != "" {
			now = tilde(used)
		}
		fmt.Println(ui.For(os.Stdout).Yellow("  " + tilde(workspace) + " uses " + now))
	}
	return nil
}

func confirm(question string) bool {
	fmt.Print(question + " [Y/n] ")
	answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	answer = strings.ToLower(strings.TrimSpace(answer))
	return answer == "" || answer == "y" || answer == "yes"
}

// tilde shortens a path under the home dir to ~/...
func tilde(p string) string {
	home, _ := os.UserHomeDir()
	if rel, err := filepath.Rel(home, p); err == nil && home != "" && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}
