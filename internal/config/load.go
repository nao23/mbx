package config

import (
	"bytes"
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed defaults.yaml
var defaultsYAML []byte

// Lists appended to instead of replaced
var appendPaths = map[string]bool{"microsandbox_config.network.allow": true}

// replaced reports whether a map is replaced whole instead of merged, so two secret sources don't mix
func replaced(path, key string) bool {
	return key == "secret_source" || path == "secret_sources"
}

// Load layers defaults -> config file -> projects: entries matching the workspace.
func Load(workspace string) (*Config, error) {
	merged, err := parseYAML(defaultsYAML)
	if err != nil {
		return nil, fmt.Errorf("defaults: %w", err)
	}
	path := Path()
	file, err := readConfigFile(path)
	if err != nil {
		return nil, err
	}
	projects, _ := file["projects"].(map[string]any)
	delete(file, "projects")
	merged = merge(merged, file, "")

	// Entries for parent directories match too; the shallowest first
	type match struct {
		dir string
		val map[string]any
	}
	var matches []match
	for key, v := range projects {
		dir := filepath.Clean(ExpandHome(key))
		if workspace != dir && !strings.HasPrefix(workspace, dir+string(filepath.Separator)) {
			continue
		}
		m, ok := v.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("%s: projects.%s must be a map", path, key)
		}
		matches = append(matches, match{dir, m})
	}
	sort.Slice(matches, func(i, j int) bool { return len(matches[i].dir) < len(matches[j].dir) })
	for _, m := range matches {
		merged = merge(merged, m.val, "")
	}

	cfg, err := decode(merged)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	cfg.dir = filepath.Dir(path)

	return cfg, nil
}

// readConfigFile reads the config file. A missing one is empty
func readConfigFile(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return map[string]any{}, nil
	}
	if err != nil {
		return nil, err
	}
	m, err := parseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return m, nil
}

func decode(merged map[string]any) (*Config, error) {
	// Back to YAML text, as KnownFields (rejecting unknown keys) works only when decoding text
	buf, err := yaml.Marshal(merged)
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(buf))
	dec.KnownFields(true)
	var cfg Config
	if err := dec.Decode(&cfg); err != nil {
		return nil, err
	}
	return &cfg, nil
}

func parseYAML(data []byte) (map[string]any, error) {
	m := map[string]any{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return m, nil
}

// merge layers maps key by key; scalars and lists are replaced. Maps, not Configs, as a struct can't tell
// an omitted key from a zero value
func merge(dst, src map[string]any, path string) map[string]any {
	out := make(map[string]any, len(dst)+len(src))
	for k, v := range dst {
		out[k] = v
	}
	for k, sv := range src {
		if sv == nil {
			continue
		}
		p := k
		if path != "" {
			p = path + "." + k
		}
		dv, ok := out[k]
		dm, dIsMap := dv.(map[string]any)
		sm, sIsMap := sv.(map[string]any)
		switch {
		case ok && dIsMap && sIsMap && !replaced(path, k):
			out[k] = merge(dm, sm, p)
		case ok && appendPaths[p]:
			out[k] = AppendUnique(StringList(dv), StringList(sv)...)
		default:
			out[k] = sv
		}
	}
	return out
}
