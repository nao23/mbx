package msb

import (
	"reflect"
	"testing"
)

// Secrets come from the active config, with exact and wildcard hosts in order, and tell values from host
// environment variables apart from values written in the config
func TestParseSecrets(t *testing.T) {
	got, err := parseSecrets([]byte(`{
		"config": {"network": {"secrets": {"secrets": [{"env_var": "OLD"}]}}},
		"active_config": {"network": {"secrets": {"secrets": [
			{"env_var": "GH_TOKEN", "allowed_hosts": [{"exact": "github.com"}, {"wildcard": "*.github.com"}],
				"source": {"kind": "env", "var": "GH_TOKEN"}},
			{"env_var": "FIXED", "allowed_hosts": [{"exact": "example.com"}], "source": {"kind": "value"}}
		]}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}
	want := []Secret{
		{Env: "GH_TOKEN", Hosts: []string{"github.com", "*.github.com"}, FromEnv: true},
		{Env: "FIXED", Hosts: []string{"example.com"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("parseSecrets = %+v, want %+v", got, want)
	}
}
