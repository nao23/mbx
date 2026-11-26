// Package wizard edits a YAML config file from a menu of settings: the menu shows each setting's value, and
// picking one asks questions to change it. What's written is the changed values, keeping the file's comments
// and other keys. Which settings there are is up to the caller.
package wizard

import (
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/nao23/mbx/internal/ui"
)

// Setting is a menu item: the value at Path (keys joined with dots) in the config, changed with Input.
type Setting struct {
	Name        string // in the menu
	Path        string
	Title       string // the question
	Description string
	Input       Input
	// Show tells whether the setting applies, given the values; nil is always. Hidden ones aren't written
	Show func(v Values) bool
	// Then has the paths of settings to ask right after this one, a Toggle, is turned on
	Then []string
}

// Values has the settings' values by path, as their Inputs take them.
type Values map[string]any

func (v Values) Bool(path string) bool     { b, _ := v[path].(bool); return b }
func (v Values) String(path string) string { s, _ := v[path].(string); return s }
func (v Values) List(path string) []string { l, _ := v[path].([]string); return l }

// ValuesOf takes the settings' values from a config (as decoded from YAML).
func ValuesOf(config map[string]any, settings []Setting) Values {
	v := Values{}
	for _, s := range settings {
		var raw any = config
		for _, k := range strings.Split(s.Path, ".") {
			m, _ := raw.(map[string]any)
			raw = m[k]
		}
		v[s.Path] = s.Input.value(raw)
	}
	return v
}

// Changes returns the values in to that differ from those in from, for the settings that apply.
func Changes(settings []Setting, from, to Values) []Change {
	var out []Change
	for _, s := range settings {
		if s.Show != nil && !s.Show(to) || same(from[s.Path], to[s.Path]) {
			continue
		}
		value := to[s.Path]
		if l, ok := value.([]string); ok && l == nil {
			value = []string{} // written as [] instead of null
		}
		out = append(out, Change{strings.Split(s.Path, "."), value})
	}
	return out
}

// same tells whether two values are equal, taking empty lists as equal to nil.
func same(a, b any) bool {
	if l, ok := a.([]string); ok && len(l) == 0 {
		a = []string(nil)
	}
	if l, ok := b.([]string); ok && len(l) == 0 {
		b = []string(nil)
	}
	return reflect.DeepEqual(a, b)
}

// File is the config file to edit.
type File struct {
	Path   string
	Text   []byte   // its text, empty for a new file
	Header string   // replaces the comment at the top, when set
	Top    []string // top-level keys that go at the top when added, in this order
	Fixed  []Change // made whatever the answers
	Check  func(text []byte) error
}

// Edit shows the menu of settings with their values in config until Save or Quit is picked. On Save, it
// shows the changes and writes them after a confirmation (No goes back to the menu). It returns the changes
// written, or none when nothing was written.
func Edit(f File, config map[string]any, settings []Setting) ([]Change, error) {
	from := ValuesOf(config, settings)
	v := maps.Clone(from)
	st := ui.For(os.Stdout)
	pick := ""
	for {
		action, err := menu(f, settings, from, v, &pick)
		if err != nil {
			return nil, err
		}
		if action == quit {
			fmt.Println(st.Dim("Nothing written"))
			return nil, nil
		}
		changes := append(slices.Clone(f.Fixed), Changes(settings, from, v)...)
		if len(changes) == 0 {
			fmt.Println(st.Dim("No changes to " + tilde(f.Path)))
			return nil, nil
		}
		out, err := apply(f.Text, changes, f.Header, f.Top)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Path, err)
		}
		if f.Check != nil {
			if err := f.Check(out); err != nil {
				return nil, fmt.Errorf("the new config doesn't load: %w", err)
			}
		}

		fmt.Println(st.Bold("Changes to " + tilde(f.Path)))
		for _, c := range changes {
			fmt.Println("  " + c.String())
		}
		write := true
		err = Ask("Write "+tilde(f.Path)+"?", huh.NewConfirm().Description("No goes back to the settings").Value(&write))
		if err != nil && !errors.Is(err, errAborted) {
			return nil, err
		}
		if err != nil || !write {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(f.Path), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(f.Path, out, 0o600); err != nil {
			return nil, err
		}
		fmt.Println(st.OK() + " Wrote " + tilde(f.Path))
		return changes, nil
	}
}

const save, quit = "\x00save", "\x00quit"

// menu lists the settings that apply with their values and asks about the one picked, until Save or Quit is
// picked, and returns which. pick has the item the cursor starts on, and gets the one picked. Aborting a
// setting's questions leaves the values as they were.
func menu(f File, settings []Setting, from, v Values, pick *string) (string, error) {
	for {
		var shown []*Setting
		width := 0
		for i := range settings {
			if s := &settings[i]; s.Show == nil || s.Show(v) {
				shown = append(shown, s)
				width = max(width, len(s.Name))
			}
		}
		var options []huh.Option[string]
		for _, s := range shown {
			mark := "  "
			if !same(from[s.Path], v[s.Path]) {
				mark = "* "
			}
			options = append(options, huh.NewOption(fmt.Sprintf("%s%-*s  %s", mark, width, s.Name, clip(s.Input.show(v[s.Path]), 50)), s.Path))
		}
		n := len(f.Fixed) + len(Changes(settings, from, v))
		saveLabel := "Save"
		if n > 0 {
			saveLabel = fmt.Sprintf("Save (%d %s)", n, map[bool]string{true: "change", false: "changes"}[n == 1])
		}
		options = append(options, huh.NewOption(saveLabel, save), huh.NewOption("Quit without saving", quit))
		err := run(huh.NewForm(huh.NewGroup(huh.NewSelect[string]().
			Title(tilde(f.Path)).
			Description("Pick a setting to change; * marks the changed ones").
			Options(options...).
			Value(pick))))
		if err != nil {
			return "", err
		}
		if *pick == save || *pick == quit {
			return *pick, nil
		}
		was := maps.Clone(v)
		if err := v.ask(settings, *pick, false); errors.Is(err, errAborted) {
			clear(v)
			maps.Copy(v, was)
		} else if err != nil {
			return "", err
		}
	}
}

// ask asks about the setting at path, and when it's a Toggle turned on, about those in its Then. fresh says
// it's asked as part of turning another on.
func (v Values) ask(settings []Setting, path string, fresh bool) error {
	i := slices.IndexFunc(settings, func(s Setting) bool { return s.Path == path })
	if i < 0 {
		return fmt.Errorf("no setting %s", path)
	}
	s := &settings[i]
	was := v[path]
	value, err := s.Input.ask(s, was, v, fresh)
	if err != nil {
		return err
	}
	v[path] = value
	if _, ok := s.Input.(Toggle); ok && value == true && was == false {
		for _, then := range s.Then {
			if err := v.ask(settings, then, true); err != nil {
				return err
			}
		}
	}
	return nil
}

var errAborted = errors.New("aborted; nothing written")

// Ask runs one question; Esc or Ctrl+C returns an error.
func Ask(title string, field huh.Field) error {
	keys := huh.NewDefaultKeyMap()
	keys.Quit = key.NewBinding(key.WithKeys("esc", "ctrl+c"), key.WithHelp("esc", "back"))
	switch f := field.(type) {
	case *huh.Select[string]:
		f.Title(title)
	case *huh.Input:
		f.Title(title)
	case *huh.Confirm:
		f.Title(title)
	case *huh.MultiSelect[string]:
		f.Title(title)
	case *huh.Text:
		f.Title(title).Lines(8)
	}
	return run(huh.NewForm(huh.NewGroup(field)).WithKeyMap(keys))
}

// run runs a form in the Catppuccin theme, returning errAborted when it's aborted. Unlike Form.Run, it quits
// normally on aborting too, so the form is cleared from the screen instead of left there.
func run(f *huh.Form) error {
	f.SubmitCmd, f.CancelCmd = tea.Quit, tea.Quit
	f.WithTheme(huh.ThemeCatppuccin())
	if _, err := tea.NewProgram(f).Run(); err != nil {
		return err
	}
	if f.State == huh.StateAborted {
		return errAborted
	}
	return nil
}

// clip shortens s to n characters, ending it with … when cut.
func clip(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// tilde shortens a path under the home dir to ~/...
func tilde(p string) string {
	home, _ := os.UserHomeDir()
	if rel, err := filepath.Rel(home, p); err == nil && home != "" && !strings.HasPrefix(rel, "..") {
		return filepath.Join("~", rel)
	}
	return p
}
