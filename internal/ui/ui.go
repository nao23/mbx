// Package ui prints progress, warnings and errors in one style, colored only on a terminal without NO_COLOR.
package ui

import (
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"
)

type Style struct {
	TTY   bool // the stream is a terminal, so a line can be rewritten in place
	color bool
}

func For(f *os.File) Style {
	tty := term.IsTerminal(int(f.Fd()))
	return Style{TTY: tty, color: tty && os.Getenv("NO_COLOR") == ""}
}

// Err is the style for stderr, where progress goes.
var Err = For(os.Stderr)

func (s Style) sgr(code, text string) string {
	if !s.color || text == "" {
		return text
	}
	return "\033[" + code + "m" + text + "\033[0m"
}

func (s Style) Bold(t string) string   { return s.sgr("1", t) }
func (s Style) Dim(t string) string    { return s.sgr("2", t) }
func (s Style) Red(t string) string    { return s.sgr("31", t) }
func (s Style) Green(t string) string  { return s.sgr("32", t) }
func (s Style) Yellow(t string) string { return s.sgr("33", t) }
func (s Style) Blue(t string) string   { return s.sgr("34", t) }
func (s Style) Cyan(t string) string   { return s.sgr("36", t) }

func (s Style) Pending() string { return s.Yellow("○") }
func (s Style) OK() string      { return s.Green("✓") }
func (s Style) Failed() string  { return s.Red("✗") }

func Step(title, note string) {
	line := Err.Blue("▸") + " " + Err.Bold(title)
	if note != "" {
		line += "  " + Err.Dim(note)
	}
	fmt.Fprintln(os.Stderr, line)
}

func Done(text, note string) {
	Item(Err.OK(), text, note)
}

func Item(mark, text, note string) {
	line := "  " + mark + " " + text
	if note != "" {
		line += "  " + Err.Dim(note)
	}
	fmt.Fprintln(os.Stderr, line)
}

// Task shows "○ label" while fn runs, rewritten in place to "✓ label" or "✗ label" on a terminal. What fn
// writes to out goes below it, indented.
func Task(label string, fn func(out io.Writer) error) error {
	if Err.TTY {
		fmt.Fprint(os.Stderr, "  "+Err.Pending()+" "+label)
	}
	out := &Indented{W: os.Stderr}
	err := fn(out)
	mark := Err.OK()
	if err != nil {
		mark = Err.Failed()
	}
	switch {
	case out.Started:
		if out.MidLine {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintln(os.Stderr, "  "+mark+" "+label)
	case Err.TTY:
		fmt.Fprintln(os.Stderr, "\r\033[K  "+mark+" "+label)
	default:
		fmt.Fprintln(os.Stderr, "  "+mark+" "+label)
	}
	return err
}

func Warn(msg string) {
	fmt.Fprintln(os.Stderr, Err.Yellow("! warning:")+" "+msg)
}

// Error prints an error that ends the command.
func Error(err error) {
	fmt.Fprintln(os.Stderr, Err.Red(Err.Bold("✗ error:"))+" "+err.Error())
}

// Pad pads to width columns. Pad before coloring, as ANSI codes take no columns.
func Pad(text string, width int) string {
	return text + strings.Repeat(" ", max(0, width-len([]rune(text))))
}

// Indented writes a command's output below a progress line, each line prefixed with a dim bar.
type Indented struct {
	W       io.Writer
	Started bool // something was written
	MidLine bool // the last write didn't end a line
}

func (d *Indented) Write(p []byte) (int, error) {
	if !d.Started {
		d.Started = true
		fmt.Fprintln(d.W)
	}
	for _, line := range strings.SplitAfter(string(p), "\n") {
		if line == "" || line == "\n" && !d.MidLine {
			continue
		}
		if !d.MidLine {
			fmt.Fprint(d.W, "    "+Err.Dim("│")+" ")
		}
		fmt.Fprint(d.W, line)
		d.MidLine = !strings.HasSuffix(line, "\n")
	}
	return len(p), nil
}
