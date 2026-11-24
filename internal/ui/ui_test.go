package ui

import (
	"fmt"
	"strings"
	"testing"
)

// A command's output goes on its own prefixed lines below the progress line (no color in tests)
func TestIndented(t *testing.T) {
	var b strings.Builder
	d := &Indented{W: &b}
	fmt.Fprint(d, "\nfirst line\n\nsecond ")
	fmt.Fprint(d, "part\nprompt: ")
	if want := "\n    │ first line\n    │ second part\n    │ prompt: "; b.String() != want || !d.MidLine {
		t.Errorf("output = %q (MidLine %v), want %q", b.String(), d.MidLine, want)
	}
}

// Padding counts runes, not bytes, so multibyte text lines up
func TestPad(t *testing.T) {
	if got := Pad("→ é", 5); got != "→ é  " {
		t.Errorf("Pad = %q", got)
	}
}
