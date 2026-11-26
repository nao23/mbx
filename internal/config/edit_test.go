package config

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/nao23/mbx/internal/secret"
	"github.com/nao23/mbx/internal/testutil"
	"github.com/nao23/mbx/internal/wizard"
)

// The settings take their values from the config, and changing them changes the right keys
func TestSettings(t *testing.T) {
	cur, err := Parse([]byte(testutil.Dedent(`
		version: 1
		microsandbox_config:
		  network:
		    allow: [a.example.com]
		github: {enabled: false}
	`)))
	if err != nil {
		t.Fatal(err)
	}
	m, err := toMap(cur)
	if err != nil {
		t.Fatal(err)
	}
	s := wizardSettings(cur)
	from := wizard.ValuesOf(m, s)
	if got := from.List("microsandbox_config.network.allow"); !slices.Equal(got, []string{"a.example.com"}) {
		t.Errorf("domains = %v", got)
	}
	if got := wizard.Changes(s, from, from); len(got) != 0 {
		t.Errorf("unchanged values: changes = %v", got)
	}

	to := maps.Clone(from)
	to["github.enabled"], to["github.secret_source"] = true, secret.Source{Command: "gh auth token"}
	to["github.allow"] = append(slices.Clone(to.List("github.allow")), "actions_storage")
	to["docker.enabled"], to["docker.ecr.enabled"] = false, true // ECR applies only with Docker
	to["microsandbox_config.network.allow"] = []string{"a.example.com", "*.b.example.com"}
	var got []string
	for _, c := range wizard.Changes(s, from, to) {
		got = append(got, c.String())
	}
	want := []string{
		"github.enabled: true",
		"github.secret_source: {command: gh auth token}",
		"github.allow: [actions_storage]",
		"docker.enabled: false",
		"microsandbox_config.network.allow: [a.example.com, '*.b.example.com']",
	}
	if !slices.Equal(got, want) {
		t.Errorf("changes =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
