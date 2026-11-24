package testutil

import "testing"

// The code's tab indentation is removed, keeping the YAML's own space indentation and blank lines
func TestDedent(t *testing.T) {
	got := Dedent(`
		docker:
		  disk: 30G

		github: {enabled: true}
	`)
	if want := "docker:\n  disk: 30G\n\ngithub: {enabled: true}\n"; got != want {
		t.Errorf("Dedent = %q, want %q", got, want)
	}
	if got := Dedent("version: 1\n"); got != "version: 1\n" {
		t.Errorf("Dedent of an unindented line = %q", got)
	}
}
