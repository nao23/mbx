package msb

import (
	"fmt"
	"os"
	"strings"

	"github.com/nao23/mbx/internal/shell"
	"github.com/nao23/mbx/internal/ui"
)

// Dry-run output favors readability; it isn't necessarily runnable as a shell script

const (
	OnHost   = "Host"
	OnVM     = "VM"
	HostToVM = "Host -> VM"
)

// Step prints a heading for the commands that follow: in dry-run with their purpose and location, otherwise
// as a progress line.
func (r *Runner) Step(where, title string, details ...string) {
	if !r.DryRun {
		ui.Step(title, "")
		return
	}
	r.stepNo++
	fmt.Printf("\n### %d. [%s] %s\n", r.stepNo, where, title)
	for _, d := range details {
		fmt.Printf("#    %s\n", d)
	}
}

func (r *Runner) Show(name string, args ...string) {
	words := []string{name}
	for _, a := range args {
		words = append(words, r.quote(a))
	}
	fmt.Println(strings.Join(words, " "))
}

func (r *Runner) printf(format string, a ...any) {
	if r.DryRun {
		fmt.Printf(format, a...)
	}
}

func (r *Runner) quote(s string) string { return shell.Quote(r.Disp(s)) }

func (r *Runner) HideTmpDir(dir string) { r.tmpDir = dir }

// Disp shows the temp dir as <tmpdir> and a leading home dir as ~ (only a leading one: the VM side of
// --volume is shown as-is).
func (r *Runner) Disp(s string) string {
	if r.tmpDir != "" {
		s = strings.ReplaceAll(s, r.tmpDir, "<tmpdir>")
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(s, home+"/") {
		s = "~" + s[len(home):]
	}
	return s
}

func (r *Runner) ShowFlags(command string, flags []Flag) {
	fmt.Println(command)
	note := ""
	for _, f := range flags {
		if f.Note != note {
			fmt.Printf("    # %s\n", f.Note)
			note = f.Note
		}
		if f.Value == "" {
			fmt.Printf("    %s\n", f.Name)
		} else {
			fmt.Printf("    %s %s\n", f.Name, r.quote(f.Value))
		}
	}
}
