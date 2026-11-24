// Package msb runs msb and changes host state, or in dry-run mode prints what it would do.
package msb

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/nao23/msbx/internal/ui"
)

// Runner runs msb and makes other changes to host state. In dry-run mode they're printed instead of
// done (state checks still run), so callers don't need to branch on DryRun.
type Runner struct {
	DryRun bool

	stepNo  int
	tmpDir  string          // shown as <tmpdir> in dry-run output
	written map[string]bool // files WriteFile pretended to write in dry-run mode
}

func (r *Runner) Run(args ...string) error {
	if r.DryRun {
		r.Show("msb", args...)
		return nil
	}
	cmd := exec.Command("msb", args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// Task runs msb as a progress item (see ui.Task).
func (r *Runner) Task(label string, args ...string) error {
	if r.DryRun {
		r.Show("msb", args...)
		return nil
	}
	return ui.Task(label, func(out io.Writer) error {
		cmd := exec.Command("msb", args...)
		cmd.Stdout, cmd.Stderr = out, out
		return cmd.Run()
	})
}

// TaskFlags runs msb <command> with flags as a progress item. Dry-run prints the flags grouped by purpose.
func (r *Runner) TaskFlags(label, command string, flags []Flag) error {
	if r.DryRun {
		r.ShowFlags("msb "+command, flags)
		return nil
	}
	return r.Task(label, append([]string{command, "--quiet"}, FlagArgs(flags)...)...)
}

// Do calls fn. Dry-run prints the equivalent command instead.
func (r *Runner) Do(fn func() error, name string, args ...string) error {
	if r.DryRun {
		r.Show(name, args...)
		return nil
	}
	return fn()
}

func (r *Runner) MkdirAll(dirs ...string) error {
	if r.DryRun {
		r.Show("mkdir", append([]string{"-p"}, dirs...)...)
		return nil
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		ui.Done(r.Disp(d), "created")
	}
	return nil
}

// Check reports whether path exists, showing the result as a progress item (in dry-run, as a command).
func (r *Runner) Check(path string) bool {
	_, err := os.Stat(path)
	ok := err == nil
	result := "exists"
	if !ok {
		result = "missing"
	}
	if r.DryRun {
		r.printf("test -e %s  # %s\n", r.quote(path), result)
	} else if ok {
		ui.Done(r.Disp(path), "")
	} else {
		ui.Item(ui.Err.Pending(), r.Disp(path), result)
	}
	return ok
}

// Touch creates an empty file if it's missing. Unlike touch, an existing file's mtime is kept
func (r *Runner) Touch(path string) error {
	if r.DryRun {
		p := r.quote(path)
		r.printf("[ -e %s ] || : >%s\n", p, p)
		return nil
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	ui.Done(r.Disp(path), "created")
	return nil
}

// WriteFile writes a 0600 file in a 0700 dir. Dry-run prints what instead, and RemoveFile then treats the
// file as existing.
func (r *Runner) WriteFile(path string, data []byte, what string) error {
	if r.DryRun {
		if r.written == nil {
			r.written = map[string]bool{}
		}
		r.written[path] = true
		r.printf("write %s (%s)\n", r.Disp(path), what)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func (r *Runner) RemoveFile(path string) {
	if !r.DryRun {
		os.Remove(path)
		return
	}
	if _, err := os.Stat(path); err == nil || r.written[path] {
		r.Show("rm", "-f", path)
	}
}

// Flag is an msb create flag; an empty Value is a bare flag. Note is shown by dry-run and config.
type Flag struct{ Name, Value, Note string }

func FlagArgs(flags []Flag) []string {
	var args []string
	for _, f := range flags {
		args = append(args, f.Name)
		if f.Value != "" {
			args = append(args, f.Value)
		}
	}
	return args
}

// Status returns false if the sandbox doesn't exist.
func Status(name string) (string, bool) {
	out, err := exec.Command("msb", "inspect", name, "--format", "json").Output()
	if err != nil {
		return "", false
	}
	var v struct {
		Status string `json:"status"`
	}
	_ = json.Unmarshal(out, &v)
	return v.Status, true
}

// Labels returns the sandbox's labels (with its image's), or nil if it doesn't exist.
func Labels(name string) map[string]string {
	return labels("inspect", name)
}

// ImageLabels returns false if the image doesn't exist.
func ImageLabels(image string) (map[string]string, bool) {
	l := labels("image", "inspect", image)
	return l, l != nil
}

func labels(args ...string) map[string]string {
	out, err := exec.Command("msb", append(args, "--format", "json")...).Output()
	if err != nil {
		return nil
	}
	var v struct {
		Config struct {
			Labels map[string]string `json:"labels"`
		} `json:"config"`
	}
	_ = json.Unmarshal(out, &v)
	if v.Config.Labels == nil {
		return map[string]string{}
	}
	return v.Config.Labels
}

type SandboxInfo struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// List returns the sandboxes carrying the label (KEY=VALUE).
func List(label string) ([]SandboxInfo, error) {
	out, err := exec.Command("msb", "ls", "--label", label, "--format", "json").Output()
	if err != nil {
		return nil, err
	}
	var list []SandboxInfo
	if err := json.Unmarshal(out, &list); err != nil {
		return nil, err
	}
	return list, nil
}
