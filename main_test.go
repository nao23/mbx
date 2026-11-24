package main

import (
	"strings"
	"testing"
)

// run accepts only the agent before --, so a stray word isn't passed to claude as a prompt
func TestRunArgs(t *testing.T) {
	for args, want := range map[string]string{
		"":                       "",
		"claude":                 "",
		"shell -- make test":     "",
		"-- -p hi":               "",
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
