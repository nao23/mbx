package main

import (
	"strings"
	"testing"
)

// run takes the agent, and only it, before --, so a stray word isn't passed to claude as a prompt
func TestRunArgs(t *testing.T) {
	for args, want := range map[string]string{
		"":                       "which agent",
		"-- -p hi":               "which agent",
		"claude":                 "",
		"shell -- make test":     "",
		"claude -- -p hi":        "",
		"nope":                   `unknown agent "nope"`,
		"claude extra":           `unexpected arguments ["extra"]`,
		"shell extra -- make it": `unexpected arguments ["extra"]`,
	} {
		cmd := newRunCmd()
		if err := cmd.ParseFlags(strings.Fields(args)); err != nil {
			t.Fatal(err)
		}
		err := cmd.ValidateArgs(cmd.Flags().Args())
		if want == "" && err != nil || want != "" && (err == nil || !strings.Contains(err.Error(), want)) {
			t.Errorf("run %s: err = %v, want %q", args, err, want)
		}
	}
}
