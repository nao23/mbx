package wizard

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/nao23/msbx/internal/secret"
	"github.com/nao23/msbx/internal/testutil"
)

var file = testutil.Dedent(`
	# My config
	version: 1

	# The VM
	microsandbox_config:
	  memory: 4G   # enough
	  network:
	    allow: [a.example.com]

	github: {enabled: false}

	docker:
`)

// apply changes and adds keys, keeping comments and the blank lines between top-level keys
func TestApply(t *testing.T) {
	out, err := apply([]byte(file), []Change{
		{[]string{"directories"}, []string{"~/work"}},
		{[]string{"microsandbox_config", "memory"}, "8G"},
		{[]string{"github", "enabled"}, true},
		{[]string{"github", "secret_source"}, secret.Source{Command: "gh auth token"}},
		{[]string{"docker", "ecr", "enabled"}, true},
		{[]string{"agent"}, "shell"},
	}, "", []string{"version", "directories"})
	if err != nil {
		t.Fatal(err)
	}
	want := testutil.Dedent(`
		# My config
		version: 1

		directories: [~/work]

		# The VM
		microsandbox_config:
		  memory: 8G # enough
		  network:
		    allow: [a.example.com]

		github: {enabled: true, secret_source: {command: gh auth token}}

		docker:
		  ecr:
		    enabled: true

		agent: shell
	`)
	if string(out) != want {
		t.Errorf("apply =\n%s\nwant\n%s", out, want)
	}

	// A header replaces the top comment; an empty file gets a map
	out, _ = apply([]byte(file), nil, "For ~/work", nil)
	if !strings.HasPrefix(string(out), "# For ~/work\n\nversion: 1\n") {
		t.Errorf("with a header:\n%s", out)
	}
	if out, _ = apply(nil, []Change{{[]string{"version"}, 1}}, "", nil); string(out) != "version: 1\n" {
		t.Errorf("empty file: %q", out)
	}
}

// Changes lists the values that differ, for the settings shown
func TestChanges(t *testing.T) {
	settings := []Setting{
		{Path: "a.on", Input: Toggle{}},
		{Path: "a.list", Input: Member{Item: "x"}, Show: func(v Values) bool { return v.Bool("a.on") }},
		{Path: "b", Input: Lines{}},
		{Path: "s", Input: Secret{}},
	}
	from := ValuesOf(map[string]any{
		"a": map[string]any{"on": true, "list": []any{"p"}},
		"s": map[string]any{"keychain": "k"},
	}, settings)
	if want := (Values{"a.on": true, "a.list": []string{"p"}, "b": []string(nil), "s": secret.Source{Keychain: "k"}}); !same(from, want) {
		t.Errorf("ValuesOf = %v, want %v", from, want)
	}
	if got := Changes(settings, from, from); len(got) != 0 {
		t.Errorf("no changes: %v", got)
	}

	to := maps.Clone(from)
	to["a.list"], to["b"], to["s"] = []string{"p", "x"}, []string{}, secret.Source{Command: "c"}
	var got []string
	for _, c := range Changes(settings, from, to) {
		got = append(got, c.String())
	}
	if want := []string{"a.list: [p, x]", "s: {command: c}"}; !slices.Equal(got, want) {
		t.Errorf("Changes = %v, want %v", got, want)
	}

	// A hidden setting isn't written
	to["a.on"] = false
	got = nil
	for _, c := range Changes(settings, from, to) {
		got = append(got, c.String())
	}
	if want := []string{"a.on: false", "s: {command: c}"}; !slices.Equal(got, want) {
		t.Errorf("Changes with a.list hidden = %v, want %v", got, want)
	}
}
